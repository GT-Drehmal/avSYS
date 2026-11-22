package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/mojang"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A Minecraft name is 3 to 16 letters, digits and underscores; a few old
// accounts have two.
var minecraftNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{2,16}$`)

// listedNamePattern is a player's name as a server's lists take it: a Java
// account's, or a Bedrock one's as Floodgate shows it, behind a prefix such as
// ".". It is safe to put in a command: no spaces, and no "@", which would make
// it a selector such as @a.
var listedNamePattern = regexp.MustCompile(`^[.*!~+-]?[A-Za-z0-9_]{2,16}$`)

// A player's UUID, which the image also takes in place of a name.
var playerUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// minecraftProfile asks Mojang which account a name belongs to now.
func (s *Server) minecraftProfile(ctx context.Context, name string) (*store.MinecraftAccount, error) {
	if !minecraftNamePattern.MatchString(name) {
		return nil, errBadRequest("%q is not a Minecraft username.", name)
	}
	body, err := s.catalog.getOr(ctx, profileURL(name), profileFor, noContent)
	if errors.Is(err, errNotFound) {
		return nil, &Error{Status: http.StatusNotFound, Code: "no_such_player", Message: "There is no Minecraft account named " + name + "."}
	}
	if err != nil {
		return nil, err
	}
	var profile struct{ ID, Name string }
	if err := json.Unmarshal(body, &profile); err != nil || profile.ID == "" || !minecraftNamePattern.MatchString(profile.Name) {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not check that name with Mojang."}
	}
	return &store.MinecraftAccount{UUID: profile.ID, Name: profile.Name}, nil
}

// profileURL is where Mojang says who a name belongs to, and what is kept
// of its answer is kept by: in one case, so that a name typed in another is
// not asked again.
func profileURL(name string) string {
	return "https://api.mojang.com/users/profiles/minecraft/" + url.PathEscape(strings.ToLower(name))
}

// profileFor is how long who a name belongs to is kept.
const profileFor = 5 * time.Minute

// noContent takes Mojang's 204, which it has answered for a name or UUID
// nobody has, as 404.
func noContent(status int, _ []byte) bool { return status == http.StatusNoContent }

const (
	// bulkByName is where Mojang says who several names belong to at once,
	// and nameBatch how many it takes at a time.
	bulkByName = "https://api.minecraftservices.com/minecraft/profile/lookup/bulk/byname"
	nameBatch  = 10
	// nameBatches is how many batches are asked at once, and nameChecks
	// how long checking a list of names takes at most: a batch not
	// answered by then is left unchecked.
	nameBatches = 4
	nameChecks  = 45 * time.Second
)

// minecraftProfiles asks Mojang who each name belongs to, nameBatch at a
// time, each batch tried again as anything of Mojang's is. Names are given
// checked and once each. What it says is each name's account (found), the
// names no account has (missing), and those of batches it never answered
// (unchecked), each in the order given. Names looked up a moment ago are
// not asked again.
func (s *Server) minecraftProfiles(ctx context.Context, names []string) (found map[string]store.MinecraftAccount, missing, unchecked []string) {
	found = map[string]store.MinecraftAccount{}
	var ask []string
	for _, name := range names {
		var profile struct{ ID, Name string }
		if body, ok := s.catalog.cached(profileURL(name), false); ok && json.Unmarshal(body, &profile) == nil && profile.ID != "" {
			found[strings.ToLower(name)] = store.MinecraftAccount{UUID: profile.ID, Name: profile.Name}
		} else {
			ask = append(ask, name)
		}
	}
	var batches [][]string
	for batch := range slices.Chunk(ask, nameBatch) {
		batches = append(batches, batch)
	}
	ctx, cancel := context.WithTimeout(ctx, nameChecks)
	defer cancel()
	answers := make([][]store.MinecraftAccount, len(batches))
	errs := make([]error, len(batches))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(nameBatches, len(batches)) {
		wg.Go(func() {
			for i := range next {
				answers[i], errs[i] = s.profileBatch(ctx, batches[i])
			}
		})
	}
	for i := range batches {
		next <- i
	}
	close(next)
	wg.Wait()
	var failed error
	for i, batch := range batches {
		if errs[i] != nil {
			failed = errs[i]
			unchecked = append(unchecked, batch...)
			continue
		}
		for _, a := range answers[i] {
			found[strings.ToLower(a.Name)] = a
			// Kept as a lookup of the one name would keep it.
			if body, err := json.Marshal(map[string]string{"id": a.UUID, "name": a.Name}); err == nil {
				s.catalog.keep(profileURL(a.Name), body, profileFor)
			}
		}
	}
	if failed != nil {
		s.Log.Warn("Mojang did not answer for some names", "names", len(unchecked), "error", failed)
	}
	for _, name := range ask {
		if _, ok := found[strings.ToLower(name)]; !ok && !hasName(unchecked, name) {
			missing = append(missing, name)
		}
	}
	return found, missing, unchecked
}

// profileBatch asks Mojang who a few names belong to. A name it does not
// answer with is one no account has.
func (s *Server) profileBatch(ctx context.Context, names []string) ([]store.MinecraftAccount, error) {
	body, err := s.catalog.postMojang(ctx, bulkByName, names)
	if err != nil {
		return nil, err
	}
	var profiles []struct{ ID, Name string }
	if err := json.Unmarshal(body, &profiles); err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not read Mojang's answer."}
	}
	var out []store.MinecraftAccount
	for _, p := range profiles {
		// Only what was asked: anything else is not an answer to it.
		if p.ID != "" && minecraftNamePattern.MatchString(p.Name) && hasName(names, p.Name) {
			out = append(out, store.MinecraftAccount{UUID: p.ID, Name: p.Name})
		}
	}
	return out, nil
}

// setMinecraftAccount links the Minecraft account of the given name to the
// caller. That it is theirs is taken on their word: see autoOps for why
// that is enough for what it is used for.
func (s *Server) setMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		return err
	}
	account, err := s.minecraftProfile(r.Context(), strings.TrimSpace(in.Name))
	if err != nil {
		return err
	}
	if err := s.Store.SetMinecraftAccount(r.Context(), u.ID, account); errors.Is(err, store.ErrTooManyAccounts) {
		return errBadRequest("Up to %d Minecraft accounts can be linked. Unlink one first.", store.MaxMinecraftAccounts)
	} else if err != nil {
		return err
	}
	s.syncOpsOf(u)
	writeJSON(w, http.StatusOK, account)
	return nil
}

// addMinecraftAccount links another account to the user, beside their others.
func (s *Server) addMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct {
		Name string
		Main bool
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	account, err := s.minecraftProfile(r.Context(), strings.TrimSpace(in.Name))
	if err != nil {
		return err
	}
	if err := s.Store.AddMinecraftAccount(r.Context(), u.ID, account, in.Main); errors.Is(err, store.ErrTooManyAccounts) {
		return errBadRequest("Up to %d Minecraft accounts can be linked. Unlink one first.", store.MaxMinecraftAccounts)
	} else if err != nil {
		return err
	}
	s.syncOpsOf(u)
	return s.writeMinecraftAccounts(w, r, u)
}

// removeMinecraftAccount unlinks one of the user's accounts.
func (s *Server) removeMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.RemoveMinecraftAccount(r.Context(), u.ID, r.PathValue("uuid")); err != nil {
		return err
	}
	s.syncOpsOf(u)
	return s.writeMinecraftAccounts(w, r, u)
}

// mainMinecraftAccount makes one of the user's accounts their main one.
func (s *Server) mainMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.SetMainMinecraftAccount(r.Context(), u.ID, r.PathValue("uuid")); err != nil {
		return err
	}
	return s.writeMinecraftAccounts(w, r, u)
}

func (s *Server) writeMinecraftAccounts(w http.ResponseWriter, r *http.Request, u *store.User) error {
	all, err := s.Store.MinecraftAccounts(r.Context(), u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, all)
	return nil
}

func (s *Server) clearMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.ClearMinecraftAccount(r.Context(), u.ID); err != nil {
		return err
	}
	s.syncOpsOf(u)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// autoOps keeps the operators of running servers in step with who holds
// what role in their organization.
//
// A member with the operator role can already run any command at a server's
// console, "op" among them. Making the player they say they are an operator
// gives them nothing they could not give themselves or anyone else, which is
// why the link between a user and a Minecraft account needs no proof for
// this. It must not be used for anything a role does not already allow.
type autoOps struct {
	// locks holds one lock per server, so that two looks at the same server
	// do not interleave their commands.
	locks sync.Map
	// missing is when the game last said it knows no player of a name the
	// panel put on a list, by server, list and name: not asked again for a
	// while, since the next look would find them still not on it.
	missing sync.Map
}

var (
	// missingFor is how long a name no account has, as Mojang says, is
	// left before it is tried again, and missingSoon one the game could not
	// look up while Mojang did not answer the panel either.
	missingFor  = time.Hour
	missingSoon = 2 * time.Minute
	// opTries is how many more times a player Mojang knows is made an
	// operator where the game could not look them up, opRetryWait apart.
	opTries     = 3
	opRetryWait = 2 * time.Second
)

// leaveOut has a name not tried again on a list for a while.
func (o *autoOps) leaveOut(list, name string, d time.Duration) {
	o.missing.Store(list+"/"+strings.ToLower(name), time.Now().Add(d))
}

// notMissing is the names to add less those left out for now.
func (o *autoOps) notMissing(list string, names []string) []string {
	var out []string
	for _, name := range names {
		if until, ok := o.missing.Load(list + "/" + strings.ToLower(name)); ok && time.Now().Before(until.(time.Time)) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// knownUUIDs is the UUIDs the panel has of the Java players a server's
// lists may name, by lowercase name in game: those of its organization's
// saved lists and its members' linked accounts.
func knownUUIDs(members []store.Member, saved map[string]*store.PlayerList, fg floodgate) map[string]string {
	out := map[string]string{}
	for _, l := range saved {
		for _, p := range l.Players {
			if !p.Bedrock() {
				out[strings.ToLower(fg.inGame(p))] = p.UUID
			}
		}
	}
	for _, m := range members {
		for _, a := range m.Minecraft {
			if a.UUID != "" && a.Name != "" {
				out[strings.ToLower(a.Name)] = a.UUID
			}
		}
	}
	return out
}

// lookAgain sorts out the names the game said it knows no player of. The
// game asks Mojang itself, once, and Mojang does not always answer it (see
// package mojang). So each name is looked up by the panel too: one it
// knows the account of is put on a whitelist by its file, with its UUID
// (no game takes a UUID in its commands), and made an operator by asking
// the game again a few times. A name no account has, as Mojang says, is
// missing; one that still could not be added is tried again soon. Names of
// Bedrock players (bedrock) are missing, as they were.
func (s *Server) lookAgain(ctx context.Context, mc *avsysv1.MinecraftServer, list roleList, names []string, known map[string]string, bedrock func(string) bool) (onFile []listedPlayer, added, missing, soon []string, err error) {
	uuids := map[string]string{}
	var ask []string
	for _, name := range names {
		switch uuid := known[strings.ToLower(name)]; {
		case bedrock(name) || !minecraftNamePattern.MatchString(name):
			missing = append(missing, name)
		case uuid != "":
			uuids[strings.ToLower(name)] = uuid
		default:
			ask = append(ask, name)
		}
	}
	if len(ask) > 0 {
		lookup, cancel := context.WithTimeout(ctx, 20*time.Second)
		found, none, unchecked := s.minecraftProfiles(lookup, ask)
		cancel()
		for _, name := range ask {
			if a, ok := found[strings.ToLower(name)]; ok {
				uuids[strings.ToLower(name)] = a.UUID
			}
		}
		missing = append(missing, none...)
		soon = append(soon, unchecked...)
	}
	for _, name := range names {
		uuid := uuids[strings.ToLower(name)]
		if uuid == "" {
			continue
		}
		if list.key == "whitelist" {
			onFile = append(onFile, listedPlayer{Name: name, UUID: dashedUUID(uuid)})
			continue
		}
		ok, err := s.tryAgain(ctx, mc, list.add+name)
		if err != nil {
			return onFile, added, missing, soon, err
		}
		if ok {
			added = append(added, name)
		} else {
			soon = append(soon, name)
		}
	}
	return onFile, added, missing, soon, nil
}

// tryAgain runs a command that named a player the game could not look up,
// opTries more times, until it can.
func (s *Server) tryAgain(ctx context.Context, mc *avsysv1.MinecraftServer, command string) (bool, error) {
	for range opTries {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(opRetryWait):
		}
		out, err := s.listCommand(ctx, mc, command)
		if err != nil {
			return false, err
		}
		if !noSuchPlayer.MatchString(plain(out)) {
			return true, nil
		}
	}
	return false, nil
}

// A roleList is one of the lists of players a server keeps that the panel
// fills by role: its operators, and its whitelist.
type roleList struct {
	// what names it in the log.
	what string
	// key is what the API calls it, as the server's settings do.
	key string
	// file is where the game keeps it, in the server's folder.
	file string
	// query asks the game who is on it, where it can be asked.
	query string
	// note is the annotation recording which players the panel put on it.
	note string
	// add and remove are the commands, less the player's name.
	add, remove string
	// from is the role from which a member is on it, or "" for nobody.
	from func(*avsysv1.MinecraftServerSpec) store.Role
	// picks are members put on it, or left off it, one by one.
	picks func(*avsysv1.MinecraftServerSpec) *avsysv1.MemberPicks
	// kept is who is on it by the server's own settings.
	kept func(*avsysv1.MinecraftServerSpec) []string
	// saved are the organization's saved lists of players whose players are
	// on it, by ID.
	saved func(*avsysv1.MinecraftServerSpec) []string
}

var roleLists = []roleList{
	{
		what: "operators", key: "ops", file: "ops.json", note: avsysv1.AnnotationAutoOps, add: "op ", remove: "deop ", from: opRole,
		picks: func(spec *avsysv1.MinecraftServerSpec) *avsysv1.MemberPicks { return spec.OpMembers },
		kept:  func(spec *avsysv1.MinecraftServerSpec) []string { return spec.Ops },
		saved: func(spec *avsysv1.MinecraftServerSpec) []string { return spec.OpLists },
	},
	{
		what: "whitelist", key: "whitelist", file: "whitelist.json", query: "whitelist list", note: avsysv1.AnnotationAutoWhitelist, add: "whitelist add ", remove: "whitelist remove ", from: whitelistRole,
		picks: func(spec *avsysv1.MinecraftServerSpec) *avsysv1.MemberPicks { return spec.WhitelistMembers },
		kept:  func(spec *avsysv1.MinecraftServerSpec) []string { return spec.Whitelist },
		saved: func(spec *avsysv1.MinecraftServerSpec) []string { return spec.WhitelistLists },
	},
}

// opRole is the role from which a server's members are operators in game,
// or "" when nobody is made one for their role.
func opRole(spec *avsysv1.MinecraftServerSpec) store.Role {
	switch spec.AutoOp {
	case "":
		return store.RoleOperator
	case "off":
		return ""
	}
	return store.Role(spec.AutoOp)
}

// whitelistRole is the role from which a server's members are on its
// whitelist, or "" when none are put there for being members.
//
// Unlike operators this reaches members who could not do it themselves: a
// viewer cannot whitelist anyone. What it gives each of them is one place,
// for the one account they have linked, which nothing proves is theirs. A
// member can so let one other player in, in their own stead. That is why it
// is off unless a server asks for it.
func whitelistRole(spec *avsysv1.MinecraftServerSpec) store.Role {
	switch spec.AutoWhitelist {
	case "", "off":
		return ""
	}
	return store.Role(spec.AutoWhitelist)
}

// wantedFrom is who should be on a list for being a member: the linked
// players of every member at or above min (with no min, nobody), and of
// those picked, less those left out. By name.
func wantedFrom(min store.Role, picks *avsysv1.MemberPicks, members []store.Member) []string {
	var out []string
	also, except := map[string]bool{}, map[string]bool{}
	if picks != nil {
		for _, id := range picks.Also {
			also[id] = true
		}
		for _, id := range picks.Except {
			except[id] = true
		}
	}
	seen := map[string]bool{}
	for _, m := range members {
		byRole := min != "" && m.Role.AtLeast(min) && !except[m.UserID]
		if !byRole && !also[m.UserID] {
			continue
		}
		// Every account they have linked: a main and an alt are both theirs.
		for _, name := range accountNames(m, m.Role.AtLeast(store.RoleOperator)) {
			if key := strings.ToLower(name); !seen[key] {
				seen[key] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// accountNames is the names of the accounts a member has linked that a list
// takes: every one for someone who could put any player on it themselves,
// as an operator can at the console; for anyone else, only those they have
// proven are theirs. Otherwise a viewer could have anyone let in by linking
// their name.
func accountNames(m store.Member, trusted bool) []string {
	if len(m.Minecraft) == 0 && m.MinecraftName != "" {
		if trusted {
			return []string{m.MinecraftName}
		}
		return nil
	}
	out := make([]string, 0, len(m.Minecraft))
	for _, a := range m.Minecraft {
		if a.Name != "" && (trusted || a.Verified) {
			out = append(out, a.Name)
		}
	}
	return out
}

// savedPlayers is the players of the saved lists named, each once. A list
// that is not there any more, deleted since, is nobody.
func savedPlayers(ids []string, saved map[string]*store.PlayerList) []store.ListPlayer {
	var out []store.ListPlayer
	seen := map[string]bool{}
	for _, id := range ids {
		l := saved[id]
		if l == nil {
			continue
		}
		for _, p := range l.Players {
			if !seen[p.UUID] {
				seen[p.UUID] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// fromSaved is the players of the saved lists named, by their names in
// game, each once: a Bedrock player's as the server's Floodgate makes it.
func fromSaved(ids []string, saved map[string]*store.PlayerList, fg floodgate) []string {
	var out []string
	for _, p := range savedPlayers(ids, saved) {
		if name := fg.inGame(p); listedNamePattern.MatchString(name) && !hasName(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// savedBedrock is the Bedrock players of the saved lists named, by their
// names in game, lowercased. Two gamertags can make one name in game, cut
// to 16 characters: each is kept.
func savedBedrock(ids []string, saved map[string]*store.PlayerList, fg floodgate) map[string][]store.ListPlayer {
	out := map[string][]store.ListPlayer{}
	for _, p := range savedPlayers(ids, saved) {
		if p.Bedrock() {
			name := strings.ToLower(fg.inGame(p))
			out[name] = append(out[name], p)
		}
	}
	return out
}

// wantedOn is who the panel should have on one of a server's lists, but for
// the names in its settings: members, by role and picked, and the players of
// the saved lists it uses.
func wantedOn(list roleList, spec *avsysv1.MinecraftServerSpec, members []store.Member, saved map[string]*store.PlayerList, fg floodgate) []string {
	out := wantedFrom(list.from(spec), list.picks(spec), members)
	for _, name := range fromSaved(list.saved(spec), saved, fg) {
		if !hasName(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// savedLists is an organization's saved lists of players by ID.
func (s *Server) savedLists(ctx context.Context, orgID string) (map[string]*store.PlayerList, error) {
	lists, err := s.Store.PlayerLists(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*store.PlayerList, len(lists))
	for i := range lists {
		out[lists[i].ID] = &lists[i]
	}
	return out, nil
}

// wantedOps is who should be an operator on a server for their role.
func wantedOps(spec *avsysv1.MinecraftServerSpec, members []store.Member) []string {
	return wantedFrom(opRole(spec), spec.OpMembers, members)
}

// splitNames reads a note of player names. Anything in it that could not
// be a player's name is dropped: these end up in commands typed at a
// server's console, and the note travels with snapshots, which a server's
// own workspace can write.
func splitNames(s string) []string {
	var out []string
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); listedNamePattern.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}

// withKept is wanted with the names set by hand in a server's settings
// added. The image puts those on its lists when the server starts, and never
// takes one off: the panel notes them as its own, so that one taken out of
// the settings is taken off in game too. Anything there that is not a
// player's name, such as a UUID, is left to the image. A Bedrock player's
// name is not given to the image (see the controller's forImage): Settings
// keeps one by UUID.
func withKept(wanted, kept []string) []string {
	out := append([]string{}, wanted...)
	for _, name := range kept {
		if listedNamePattern.MatchString(name) && !hasName(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// hasName is whether a list has a player, whose names go regardless of case.
func hasName(list []string, name string) bool {
	for _, n := range list {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// listChanges is what to do to a list on a server to get the players the
// panel should have put on it there, from those it has (made).
//
// With who is on it in game known (on), that is gone by: a player it should
// have who is not on it, taken off by hand, is put back, and one it put
// there that is gone already needs nothing done. Without, the note is
// taken to be true of the game. Either way only players the panel put on
// it are ever taken off: one added by hand is not its to remove.
func listChanges(made, wanted, on []string, known bool) (add, remove []string) {
	if !known {
		on = made
	}
	for _, name := range wanted {
		if !hasName(on, name) {
			add = append(add, name)
		}
	}
	for _, name := range made {
		if !hasName(wanted, name) && hasName(on, name) {
			remove = append(remove, name)
		}
	}
	return add, remove
}

// stillOn is who of the players the panel has put on a list, or should
// have, are on it in game: the note as it is true of the game now.
func stillOn(made, wanted, on []string) []string {
	var out []string
	for _, name := range on {
		if (hasName(made, name) || hasName(wanted, name)) && !hasName(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// sameNames is whether two lists have the same players, in any order.
func sameNames(a, b []string) bool {
	for _, n := range a {
		if !hasName(b, n) {
			return false
		}
	}
	for _, n := range b {
		if !hasName(a, n) {
			return false
		}
	}
	return true
}

// syncOps makes one running server's operators and whitelist what its
// organization's roles and its settings say, as they are in game. It runs
// no command when they already are.
func (s *Server) syncOps(ctx context.Context, mc *avsysv1.MinecraftServer, members []store.Member) error {
	if mc.Status.Phase != avsysv1.PhaseRunning {
		// One that is not running is put right when it next is: the note
		// of who was made an operator stays as it is until then.
		return nil
	}
	key := client.ObjectKeyFromObject(mc)
	lock := s.autoOps.lock(key)
	lock.Lock()
	defer lock.Unlock()

	// As it is now that the lock is held: another look may have changed
	// the note of who was made an operator while this one waited. Read from
	// the cluster itself, since what Client has lags a change by a moment
	// and the caller may have just made one.
	fresh := mc.DeepCopy()
	if s.Reader != nil {
		if err := s.Reader.Get(ctx, key, fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fresh.Status.Phase != avsysv1.PhaseRunning {
			return nil
		}
	}
	// The members too: a look that read them before an account was linked
	// or unlinked, and got the lock after one that read them since, would
	// otherwise undo it. And the saved lists, for the same reason.
	var saved map[string]*store.PlayerList
	if slug, ok := s.orgOfNamespace(fresh.Namespace); ok {
		org, err := s.Store.OrgBySlug(ctx, slug)
		if err != nil {
			return err
		}
		if members, err = s.Store.Members(ctx, org.ID); err != nil {
			return err
		}
		if saved, err = s.savedLists(ctx, org.ID); err != nil {
			return err
		}
	}
	orig := fresh.DeepCopy()
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	changed := false
	var failed error
	fg := s.floodgateOf(ctx, fresh)
	uuids := knownUUIDs(members, saved, fg)
	for _, list := range roleLists {
		made := splitNames(fresh.Annotations[list.note])
		wanted := withKept(wantedOn(list, &fresh.Spec, members, saved, fg), list.kept(&fresh.Spec))
		bedrock := savedBedrock(list.saved(&fresh.Spec), saved, fg)
		// Who is on it in game: anyone at the console may have changed it.
		// Where that cannot be read, the note is gone by as it is.
		players, err := s.readList(ctx, fresh, list)
		known := err == nil
		if !known {
			s.Log.Debug("could not read a server's list", "server", key.String(), "list", list.what, "error", err)
		}
		on := playerNames(players)
		// A player named in the settings by UUID, as Settings keeps a
		// Bedrock player, is put on by the image as the server starts; the
		// panel notes them as its own by the name the game has for them, so
		// that taken out of the settings they are taken off. Where the list
		// cannot be read, who they are cannot be told: nothing the panel
		// put there is taken off until it can.
		if kept := keptUUIDs(list.kept(&fresh.Spec)); len(kept) > 0 {
			if !known {
				wanted = withKept(wanted, made)
			}
			for _, p := range players {
				if keptByUUID(kept, p.UUID) && !hasName(wanted, p.Name) {
					wanted = append(wanted, p.Name)
				}
			}
		}
		add, remove := listChanges(made, wanted, on, known)
		listKey := key.String() + "/" + list.key
		add = s.autoOps.notMissing(listKey, add)
		from := made
		if known {
			from = stillOn(made, wanted, on)
		}
		isBedrock := func(name string) bool {
			if i := slices.IndexFunc(players, func(p listedPlayer) bool { return strings.EqualFold(p.Name, name) }); i >= 0 {
				return players[i].Bedrock
			}
			return bedrockListed(listedPlayer{Name: name})
		}
		// Bedrock players of saved lists go on a whitelist by its file, with
		// the UUID Floodgate gives them, and come off it so: the game cannot
		// look up their names (see bedrock.go).
		var fileAdd, fileRemove []listedPlayer
		if list.key == "whitelist" {
			add = slices.DeleteFunc(add, func(name string) bool {
				ps, ok := bedrock[strings.ToLower(name)]
				for _, p := range ps {
					fileAdd = append(fileAdd, listedPlayer{Name: name, UUID: dashedUUID(p.UUID)})
				}
				return ok
			})
			remove = slices.DeleteFunc(remove, func(name string) bool {
				if isBedrock(name) {
					fileRemove = append(fileRemove, listedPlayer{Name: name})
					return true
				}
				return false
			})
			if known {
				more, gone := bedrockByUUID(bedrock, players, made)
				fileAdd, fileRemove = append(fileAdd, more...), append(fileRemove, gone...)
			}
		}
		// With the game's whitelist off, nobody is taken off it and its file
		// is not read again: with enforce-whitelist on, vanilla up to 1.21.8,
		// and the loaders built on it, put out everyone not on it for either,
		// the whitelist on or not. Who is to come off is still noted, and
		// comes off once the game has it on.
		hold := list.key == "whitelist" && s.gameWhitelistOff(ctx, fresh)
		if hold {
			remove, fileAdd, fileRemove = nil, nil, nil
		}
		if len(add) == 0 && len(remove) == 0 && len(fileAdd) == 0 && len(fileRemove) == 0 && sameNames(from, made) {
			continue
		}
		// The note follows each command as it is carried out, so that a
		// failure part of the way leaves it true of the game. A player put
		// on a list and not noted would never be taken off again.
		done, missing, err := applyNames(from, add, remove, func(command string) error {
			out, err := s.listCommand(ctx, fresh, command)
			if err == nil && noSuchPlayer.MatchString(plain(out)) {
				return errNoSuchPlayer
			}
			return err
		}, list.add, list.remove, isBedrock)
		var soon []string
		if err == nil && len(missing) > 0 {
			isSaved := func(name string) bool { _, ok := bedrock[strings.ToLower(name)]; return ok || isBedrock(name) }
			var onFile []listedPlayer
			var added []string
			onFile, added, missing, soon, err = s.lookAgain(ctx, fresh, list, missing, uuids, isSaved)
			if hold {
				// Put on by the file once the whitelist is on.
				for _, p := range onFile {
					s.autoOps.leaveOut(listKey, p.Name, missingSoon)
				}
				onFile = nil
			}
			fileAdd = append(fileAdd, onFile...)
			for _, name := range added {
				if !hasName(done, name) {
					done = append(done, name)
				}
			}
		}
		if err == nil && (len(fileAdd) > 0 || len(fileRemove) > 0) {
			if err = s.changeBedrockWhitelist(ctx, fresh, fileAdd, fileRemove); err == nil {
				for _, p := range fileAdd {
					if !hasName(done, p.Name) {
						done = append(done, p.Name)
					}
				}
				for _, p := range fileRemove {
					// One taken off by UUID leaves the name to another.
					if p.Name != "" {
						done = slices.DeleteFunc(done, func(n string) bool { return strings.EqualFold(n, p.Name) })
					}
				}
				sort.Strings(done)
				add = append(add, playerNames(fileAdd)...)
				remove = append(remove, playerNames(fileRemove)...)
			}
		}
		for _, name := range missing {
			// The game knows a Bedrock player once they have joined: one
			// to be made an operator is tried again at the next look.
			if _, ok := bedrock[strings.ToLower(name)]; ok {
				continue
			}
			s.autoOps.leaveOut(listKey, name, missingFor)
			s.Log.Warn("the game knows no player of a name for its "+list.what, "server", key.String(), "player", name)
		}
		for _, name := range soon {
			s.autoOps.leaveOut(listKey, name, missingSoon)
			s.Log.Warn("could not look up a player for its "+list.what+"; trying again soon", "server", key.String(), "player", name)
		}
		if note := strings.Join(done, ","); note == "" {
			delete(fresh.Annotations, list.note)
		} else {
			fresh.Annotations[list.note] = note
		}
		changed = true
		if err != nil {
			failed = err
			break
		}
		if len(add) > 0 || len(remove) > 0 {
			s.Log.Info(list.what+" brought in step with roles", "server", key.String(), "added", add, "removed", remove)
		}
	}
	if changed {
		if err := s.Client.Patch(ctx, fresh, client.MergeFrom(orig)); err != nil {
			return err
		}
	}
	return failed
}

// bedrockByUUID is who of a list's Bedrock players are on a whitelist by
// their name in game but not by their UUID, as when two gamertags cut to 16
// characters make one name, or one account has taken another's place on a
// list: those to put on (more), and the entries of that name the panel put
// there that are no list's player now (gone), by UUID.
func bedrockByUUID(bedrock map[string][]store.ListPlayer, on []listedPlayer, made []string) (more, gone []listedPlayer) {
	for _, entry := range on {
		ps := bedrock[strings.ToLower(entry.Name)]
		if len(ps) == 0 || xuidOf(entry.UUID) == "" || !hasName(made, entry.Name) {
			continue
		}
		if !slices.ContainsFunc(ps, func(p store.ListPlayer) bool { return p.UUID == bareUUID(entry.UUID) }) {
			gone = append(gone, listedPlayer{UUID: entry.UUID})
		}
	}
	for name, ps := range bedrock {
		i := slices.IndexFunc(on, func(e listedPlayer) bool { return strings.ToLower(e.Name) == name })
		if i < 0 {
			// Not on by name: put on as any other.
			continue
		}
		for _, p := range ps {
			if !slices.ContainsFunc(on, func(e listedPlayer) bool { return bareUUID(e.UUID) == p.UUID }) {
				more = append(more, listedPlayer{Name: on[i].Name, UUID: dashedUUID(p.UUID)})
			}
		}
	}
	slices.SortFunc(more, func(a, b listedPlayer) int { return strings.Compare(a.UUID, b.UUID) })
	return more, gone
}

// applyNames carries out the additions and removals of one list with run,
// and returns who the panel has put on it afterwards: from, as changed by
// the commands that went through, in order if all did. A player the game
// says it knows nothing of is not noted as added, but is among missing; one
// it cannot take off for that reason is not on it to take off, unless the
// game may just not know them yet (unknown), as it does not know a Bedrock
// player who has not joined: they stay noted, to be taken off later.
func applyNames(from, add, remove []string, run func(command string) error, adding, removing string, unknown func(name string) bool) (noted, missing []string, err error) {
	now := append([]string{}, from...)
	for _, name := range add {
		if err := run(adding + name); errors.Is(err, errNoSuchPlayer) {
			missing = append(missing, name)
			continue
		} else if err != nil {
			return now, missing, err
		}
		if !hasName(now, name) {
			now = append(now, name)
		}
	}
	for _, name := range remove {
		err := run(removing + name)
		if errors.Is(err, errNoSuchPlayer) && unknown != nil && unknown(name) {
			continue
		}
		if err != nil && !errors.Is(err, errNoSuchPlayer) {
			return now, missing, err
		}
		now = slices.DeleteFunc(now, func(n string) bool { return strings.EqualFold(n, name) })
	}
	sort.Strings(now)
	return now, missing, nil
}

// syncOrgOps does syncOps for every server of an organization.
func (s *Server) syncOrgOps(ctx context.Context, org *store.Org) {
	var servers avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &servers, client.InNamespace(s.Namespace(org.Slug))); err != nil {
		s.Log.Error("listing servers to set their operators", "org", org.Slug, "error", err)
		return
	}
	if len(servers.Items) == 0 {
		return
	}
	members, err := s.Store.Members(ctx, org.ID)
	if err != nil {
		s.Log.Error("listing members to set operators", "org", org.Slug, "error", err)
		return
	}
	s.refreshNames(ctx, members)
	for i := range servers.Items {
		if err := s.syncOps(ctx, &servers.Items[i], members); err != nil {
			// Tried again with the next look.
			s.Log.Warn("could not set a server's operators", "server", servers.Items[i].Name, "org", org.Slug, "error", err)
		}
	}
}

// refreshNames brings each member's linked player up to its current name.
// An account keeps its UUID when it is renamed, and the game's commands go
// by name: the old one would stop meaning that player, and may come to mean
// someone else. Mojang is asked a few times a day per account at most, and
// a name that cannot be looked up is left as it is.
func (s *Server) refreshNames(ctx context.Context, members []store.Member) {
	// Each is asked again where Mojang does not answer, for a while: not
	// so long that the servers' lists wait on it. Those left are looked up
	// with the next look, which starts with those looked up longest ago, so
	// that one that never answers does not keep the others from their turn.
	type account struct{ member, account int }
	var turns []account
	for i := range members {
		for k := range members[i].Minecraft {
			turns = append(turns, account{i, k})
		}
	}
	last := func(a account) time.Time {
		at, _ := s.memberNameChecks.Load(members[a.member].Minecraft[a.account].UUID)
		t, _ := at.(time.Time)
		return t
	}
	slices.SortStableFunc(turns, func(a, b account) int { return last(a).Compare(last(b)) })
	lookups, cancel := context.WithTimeout(ctx, refreshFor)
	defer cancel()
	for _, turn := range turns {
		if lookups.Err() != nil {
			return
		}
		m := &members[turn.member]
		a := &m.Minecraft[turn.account]
		s.memberNameChecks.Store(a.UUID, time.Now())
		body, err := s.catalog.getOr(lookups, sessionProfileURL(a.UUID), 6*time.Hour, noContent)
		if err != nil {
			continue
		}
		var profile struct{ Name string }
		if json.Unmarshal(body, &profile) != nil || !minecraftNamePattern.MatchString(profile.Name) || profile.Name == a.Name {
			continue
		}
		if err := s.Store.RenameMinecraftAccount(ctx, a.UUID, profile.Name); err != nil {
			continue
		}
		s.Log.Info("a linked Minecraft account was renamed", "user", m.Username, "from", a.Name, "to", profile.Name)
		if a.Main {
			m.MinecraftName = profile.Name
		}
		a.Name = profile.Name
	}
}

// listNamesEvery is how often each player on a saved list is looked up for
// a new name, and listNamesPerLook how many are looked up at most in one
// look, over every organization: a list can have a thousand players, and
// Mojang can be slow or away.
const (
	listNamesEvery   = 6 * time.Hour
	listNamesPerLook = 30
)

// refreshListNames brings players on an organization's saved lists up to
// their current name, as refreshNames does for members, and for the same
// reason: those not looked up for a while, as many as budget has left.
// Each is asked of Mojang on its own, not through the catalog's cache,
// which a thousand of them would empty: a Bedrock player's gamertag of
// GeyserMC.
func (s *Server) refreshListNames(ctx context.Context, orgID string, budget *int) {
	lists, err := s.Store.PlayerLists(ctx, orgID)
	if err != nil {
		return
	}
	for _, l := range lists {
		for _, p := range l.Players {
			if *budget <= 0 {
				return
			}
			if at, ok := s.listNameChecks.Load(p.UUID); ok && time.Since(at.(time.Time)) < listNamesEvery {
				continue
			}
			s.listNameChecks.Store(p.UUID, time.Now())
			*budget--
			current := s.currentName
			if p.Bedrock() {
				current = func(ctx context.Context, _ string) (string, error) { return s.currentGamertag(ctx, p.XUID) }
			}
			name, err := current(ctx, p.UUID)
			if err != nil || name == p.Name {
				continue
			}
			if err := s.Store.RenameListPlayer(ctx, p.UUID, name); err == nil {
				s.Log.Info("a player on a saved list was renamed", "from", p.Name, "to", name)
			}
		}
	}
}

// refreshFor is how long the names of an organization's members are looked
// up for at most, in one look.
var refreshFor = 20 * time.Second

// sessionProfileURL is where Mojang has an account's name and skin.
func sessionProfileURL(uuid string) string {
	return "https://sessionserver.mojang.com/session/minecraft/profile/" + url.PathEscape(uuid)
}

// currentName asks Mojang what an account is called now: again where it
// does not answer, for a few seconds.
func (s *Server) currentName(ctx context.Context, uuid string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body []byte
	err := mojang.Do(ctx, func(ctx context.Context, _ int) (bool, error) {
		u := sessionProfileURL(uuid)
		status, b, err := s.catalog.send(ctx, http.MethodGet, u, nil)
		body, err = answerOf(u, status, b, err, noContent)
		return mojang.Again(status), err
	})
	if err != nil {
		return "", err
	}
	var profile struct{ Name string }
	if err := json.Unmarshal(body, &profile); err != nil || !minecraftNamePattern.MatchString(profile.Name) {
		return "", errNotFound
	}
	return profile.Name, nil
}

// syncOpsFor brings one server in step, as it is in mc, without making
// whoever changed it wait.
func (s *Server) syncOpsFor(org *store.Org, mc *avsysv1.MinecraftServer) {
	mc = mc.DeepCopy()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		members, err := s.Store.Members(ctx, org.ID)
		if err != nil {
			return
		}
		if err := s.syncOps(ctx, mc, members); err != nil {
			s.Log.Warn("could not set a server's operators", "server", mc.Name, "org", org.Slug, "error", err)
		}
	}()
}

// syncOpsSoon brings an organization's servers in step without making
// whoever caused the change wait for it.
func (s *Server) syncOpsSoon(org *store.Org) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.syncOrgOps(ctx, org)
	}()
}

// syncOpsOf does that for every organization a user is in, after they have
// linked or unlinked an account.
func (s *Server) syncOpsOf(u *store.User) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		orgs, err := s.Store.OrgsForUser(ctx, u)
		if err != nil {
			return
		}
		for i := range orgs {
			// An instance administrator is listed every organization there is, and is
			// a member of few; the sync goes by membership either way.
			s.syncOrgOps(ctx, &orgs[i].Org)
		}
	}()
}

// RunOps looks once a minute for servers whose operators are out of step
// with their organization's roles, and for content left to remove, until
// ctx ends. Changes made in the panel
// are acted on at once; this is what catches a server that has just
// started, and anything that failed the first time.
func (s *Server) RunOps(ctx context.Context) {
	for {
		s.opsTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (s *Server) opsTick(ctx context.Context) {
	var all avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &all); err != nil {
		return
	}
	// Packs the panel keeps that no server has any more, once an hour.
	if time.Since(s.packsPruned) > time.Hour {
		s.packsPruned = time.Now()
		s.prunePacks(ctx, all.Items)
	}
	// Content taken off a server that could not be deleted then.
	for i := range all.Items {
		if all.Items[i].Annotations[avsysv1.AnnotationContentLeft] != "" {
			s.finishContentLeft(ctx, &all.Items[i])
		}
	}
	// Resource packs joined for another game version than their server's.
	for i := range all.Items {
		if s.stackOutdated(ctx, &all.Items[i]) {
			go s.restackFor(context.WithoutCancel(ctx), client.ObjectKeyFromObject(&all.Items[i]))
		}
	}
	running := map[string]bool{}
	for i := range all.Items {
		if all.Items[i].Status.Phase == avsysv1.PhaseRunning {
			running[all.Items[i].Namespace] = true
		}
	}
	names := listNamesPerLook
	for ns := range running {
		slug, ok := s.orgOfNamespace(ns)
		if !ok {
			continue
		}
		org, err := s.Store.OrgBySlug(ctx, slug)
		if err != nil {
			continue
		}
		s.syncOrgOps(ctx, org)
		// Players on its saved lists who were renamed: looked at once its
		// servers are in step, a few at a time, and not each time a list is
		// changed. A name changed here is acted on with the next look.
		s.refreshListNames(ctx, org.ID, &names)
	}
}

// noteRoleLists sets a server's notes of which players the panel put on
// its lists, to what a snapshot recorded of them. recorded is by annotation.
func (s *Server) noteRoleLists(ctx context.Context, key client.ObjectKey, recorded map[string]string) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	changed := false
	for _, list := range roleLists {
		made := strings.Join(splitNames(recorded[list.note]), ",")
		if mc.Annotations[list.note] == made {
			continue
		}
		if made == "" {
			delete(mc.Annotations, list.note)
		} else {
			mc.Annotations[list.note] = made
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// started brings a server's lists in step the moment it has come up, not
// with the next look: someone who has just started a whitelisted server
// wants to walk into it.
func (s *Server) started(old, now any) {
	was, _ := old.(*avsysv1.MinecraftServer)
	mc, _ := now.(*avsysv1.MinecraftServer)
	if was == nil || mc == nil || was.Status.Phase == avsysv1.PhaseRunning || mc.Status.Phase != avsysv1.PhaseRunning {
		return
	}
	slug, ok := s.orgOfNamespace(mc.Namespace)
	if !ok {
		return
	}
	mc = mc.DeepCopy()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		org, err := s.Store.OrgBySlug(ctx, slug)
		if err != nil {
			return
		}
		members, err := s.Store.Members(ctx, org.ID)
		if err != nil {
			return
		}
		// With the names as they are known, at once. Asking Mojang whether
		// any has changed can take a while and is left to the next look: a
		// server that has just come up is being let into already.
		if err := s.syncOps(ctx, mc, members); err != nil {
			// The look that runs every minute tries again.
			s.Log.Warn("could not set a started server's operators and whitelist", "server", mc.Name, "org", slug, "error", err)
		}
	}()
}
