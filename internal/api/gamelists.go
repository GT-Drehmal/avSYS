package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// The whitelist and operators of a server as the game has them. Anyone at
// the console, or in game, can change them: the panel reads them to keep
// the players it puts there on, and to show who was put there otherwise.

// listedPlayer is a player on one of a server's lists.
type listedPlayer struct {
	Name string `json:"name"`
	UUID string `json:"uuid,omitempty"`
	// Bedrock is whether they play Bedrock Edition, through Floodgate.
	Bedrock bool `json:"bedrock,omitempty"`
}

// markBedrock says which players on a list are of Bedrock.
func markBedrock(players []listedPlayer) []listedPlayer {
	for i := range players {
		players[i].Bedrock = bedrockListed(players[i])
	}
	return players
}

func playerNames(players []listedPlayer) []string {
	out := make([]string, 0, len(players))
	for _, p := range players {
		out = append(out, p.Name)
	}
	return out
}

// readList is who is on one of a server's lists now. A running server is
// asked, where it can say; otherwise its file is read, which the game
// writes at each change and reads when it starts.
func (s *Server) readList(ctx context.Context, mc *avsysv1.MinecraftServer, list roleList) ([]listedPlayer, error) {
	if mc.Status.Phase == avsysv1.PhaseRunning && list.query != "" {
		out, err := s.listCommand(ctx, mc, list.query)
		if err != nil {
			return nil, err
		}
		if names, ok := parseWhitelist(out); ok {
			players := make([]listedPlayer, 0, len(names))
			for _, name := range names {
				players = append(players, listedPlayer{Name: name})
			}
			// The file has who each is: kept beside the names, where it can be read.
			// Two entries can have one name, as two Bedrock players can: each
			// entry goes to one name.
			if inFile, err := s.readListFile(ctx, mc, list); err == nil {
				used := make([]bool, len(inFile))
				for i := range players {
					for k := range inFile {
						if !used[k] && strings.EqualFold(inFile[k].Name, players[i].Name) {
							players[i].UUID, used[k] = inFile[k].UUID, true
							break
						}
					}
				}
			}
			return markBedrock(players), nil
		}
		// Said in words not known here: its file says the same.
	}
	players, err := s.readListFile(ctx, mc, list)
	return markBedrock(players), err
}

// listCommand runs a command for a running server's lists on its game,
// over the connection the panel keeps open to it to ask how fast it ticks.
// The game logs each connection made to it: one each minute for this would
// fill its log.
func (s *Server) listCommand(ctx context.Context, mc *avsysv1.MinecraftServer, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st := s.ticks.state(mc.Namespace + "/" + mc.Name)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.forGame(mc)
	return s.tickCommand(ctx, st, mc, command)
}

// readListFile reads one of a server's lists from its file. A workspace
// that is not up is not waited for: the look every minute goes through
// every running server, and Settings shows nothing more until it is.
func (s *Server) readListFile(ctx context.Context, mc *avsysv1.MinecraftServer, list roleList) ([]listedPlayer, error) {
	sc, err := s.workspaces.getNow(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return nil, err
	}
	return listFile(sc, path.Join(dataRoot, list.file))
}

// listFile reads a list of players the game keeps, such as ops.json. One
// not there yet is nobody.
func listFile(sc *sftp.Client, file string) ([]listedPlayer, error) {
	f, err := sc.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return []listedPlayer{}, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return nil, err
	}
	var entries []listedPlayer
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("%s: %w", path.Base(file), err)
		}
	}
	out := make([]listedPlayer, 0, len(entries))
	for _, e := range entries {
		// What could not go in a command is nobody the panel can act on.
		if listedNamePattern.MatchString(e.Name) {
			out = append(out, e)
		}
	}
	return out, nil
}

// whitelistAnswer is what the game answers to "whitelist list": "There are
// 2 whitelisted player(s): a, b" now, "There is/are 2 ..." in 1.19, and
// "There are 2 (out of 5 seen) whitelisted players:a and b" in 1.12.
var whitelistAnswer = regexp.MustCompile(`(?s)^There (?:are|is/are|is) (\d+) (?:\(out of \d+ seen\) )?whitelisted players?(?:\(s\))?:(.*)$`)

// parseWhitelist reads the names in the game's answer to "whitelist list",
// and whether it was such an answer. One whose count and names do not agree
// is not taken for one.
func parseWhitelist(out string) ([]string, bool) {
	out = strings.TrimSpace(plain(out))
	if out == "There are no whitelisted players" {
		return []string{}, true
	}
	m := whitelistAnswer.FindStringSubmatch(out)
	if m == nil {
		return nil, false
	}
	count, err := strconv.Atoi(m[1])
	if err != nil {
		return nil, false
	}
	names := []string{}
	for _, part := range strings.Split(m[2], ",") {
		for _, name := range strings.Split(part, " and ") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if !listedNamePattern.MatchString(name) {
				return nil, false
			}
			names = append(names, name)
		}
	}
	if len(names) != count {
		return nil, false
	}
	return names, true
}

// noSuchPlayer is the game saying it knows no player of the name it was
// given: "That player does not exist", or "Could not de-op x" in 1.12.
var noSuchPlayer = regexp.MustCompile(`(?i)does not exist|could not|unknown player|no player was found`)

// errNoSuchPlayer is a command that named a player the game does not know.
var errNoSuchPlayer = errors.New("no such player")

// addedInGame is who is on a list other than by the panel: neither put
// there for being a member nor named in the server's settings, nor noted as
// put there by the panel. A player of a saved list the server uses is the
// panel's by UUID too (savedUUIDs), whatever name the game has for them.
func addedInGame(on []listedPlayer, made, wanted, kept, savedUUIDs []string) []listedPlayer {
	out := []listedPlayer{}
	for _, p := range on {
		if hasName(made, p.Name) || hasName(wanted, p.Name) || hasName(kept, p.Name) || keptByUUID(kept, p.UUID) || keptByUUID(savedUUIDs, p.UUID) {
			continue
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b listedPlayer) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// keptUUIDs are the players named in a server's settings by UUID.
func keptUUIDs(kept []string) []string {
	var out []string
	for _, k := range kept {
		if playerUUIDPattern.MatchString(k) {
			out = append(out, k)
		}
	}
	return out
}

// keptByUUID is whether a player is named in a server's settings by UUID.
func keptByUUID(kept []string, uuid string) bool {
	if uuid == "" {
		return false
	}
	bare := strings.ReplaceAll(uuid, "-", "")
	for _, k := range kept {
		if playerUUIDPattern.MatchString(k) && strings.EqualFold(strings.ReplaceAll(k, "-", ""), bare) {
			return true
		}
	}
	return false
}

// roleListByKey is the list a request names: "whitelist" or "ops".
func roleListByKey(key string) (roleList, bool) {
	for _, list := range roleLists {
		if list.key == key {
			return list, true
		}
	}
	return roleList{}, false
}

// gameListView is one of a server's lists as Settings shows it.
type gameListView struct {
	// Known is whether the list could be read.
	Known bool `json:"known"`
	// InGame are those on it other than by the panel.
	InGame []listedPlayer `json:"inGame"`
}

// listState is one list of a server as it is, read under its lock.
type listState struct {
	on                 []listedPlayer
	err                error
	made, wanted, kept []string
	// members are those wanted for being members, as against those wanted
	// for being on a saved list the server uses.
	members []string
	// savedUUIDs are the players of the saved lists it uses, by UUID.
	savedUUIDs []string
	fg         floodgate
}

func (s *Server) listState(ctx context.Context, mc *avsysv1.MinecraftServer, list roleList, members []store.Member, saved map[string]*store.PlayerList, fg floodgate) listState {
	on, err := s.readList(ctx, mc, list)
	st := listState{
		on: on, err: err,
		made:    splitNames(mc.Annotations[list.note]),
		wanted:  wantedOn(list, &mc.Spec, members, saved, fg),
		members: wantedFrom(list.from(&mc.Spec), list.picks(&mc.Spec), members),
		kept:    list.kept(&mc.Spec),
		fg:      fg,
	}
	for _, p := range savedPlayers(list.saved(&mc.Spec), saved) {
		st.savedUUIDs = append(st.savedUUIDs, p.UUID)
	}
	return st
}

// savedListWith is the first of the saved lists named that has a player on
// it, by their name in game, or nil.
func savedListWith(ids []string, saved map[string]*store.PlayerList, name string, fg floodgate) *store.PlayerList {
	for _, id := range ids {
		if l := saved[id]; l != nil && slices.ContainsFunc(l.Players, func(p store.ListPlayer) bool { return strings.EqualFold(fg.inGame(p), name) }) {
			return l
		}
	}
	return nil
}

// savedListWithUUID is the first of the saved lists named that has a player
// of a UUID on it, or nil.
func savedListWithUUID(ids []string, saved map[string]*store.PlayerList, uuid string) *store.PlayerList {
	bare := bareUUID(uuid)
	for _, id := range ids {
		if l := saved[id]; l != nil && bare != "" && slices.ContainsFunc(l.Players, func(p store.ListPlayer) bool { return bareUUID(p.UUID) == bare }) {
			return l
		}
	}
	return nil
}

// getGameLists says who is on a server's whitelist and operators other than
// by the panel.
func (s *Server) getGameLists(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	members, err := s.Store.Members(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	saved, err := s.savedLists(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	out := map[string]gameListView{}
	fg := s.floodgateOf(r.Context(), mc)
	for _, list := range roleLists {
		st := s.listState(r.Context(), mc, list, members, saved, fg)
		view := gameListView{Known: st.err == nil, InGame: []listedPlayer{}}
		if st.err == nil {
			view.InGame = addedInGame(st.on, st.made, st.wanted, st.kept, st.savedUUIDs)
		}
		out[list.key] = view
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// removeFromGameList takes a player added in game off a server's whitelist
// or operators: by the game's own command while it runs, and from its file
// while it does not.
func (s *Server) removeFromGameList(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	list, ok := roleListByKey(r.PathValue("list"))
	if !ok {
		return errNotFound
	}
	name := r.PathValue("name")
	if !listedNamePattern.MatchString(name) {
		return errBadRequest("%q is not a player's name.", name)
	}
	ctx := r.Context()
	key := client.ObjectKeyFromObject(mc)
	lock := s.autoOps.lock(key)
	lock.Lock()
	defer lock.Unlock()
	if mc, err = s.live(ctx, mc); err != nil {
		return err
	}
	members, err := s.Store.Members(ctx, a.Org.ID)
	if err != nil {
		return err
	}
	saved, err := s.savedLists(ctx, a.Org.ID)
	if err != nil {
		return err
	}
	fg := s.floodgateOf(ctx, mc)
	st := s.listState(ctx, mc, list, members, saved, fg)
	if st.err != nil {
		s.Log.Warn("could not read a server's list", "server", key.String(), "list", list.what, "error", st.err)
		return &Error{Status: http.StatusBadGateway, Code: "unreachable", Message: "The server's players could not be read right now. Try again in a moment."}
	}
	i := slices.IndexFunc(st.on, func(p listedPlayer) bool { return strings.EqualFold(p.Name, name) })
	switch {
	case i < 0:
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("%s is not on the %s.", name, list.what)}
	case hasName(st.members, name):
		return &Error{Status: http.StatusConflict, Code: "member", Message: fmt.Sprintf("%s is a member's player. Untick them under Members instead.", name)}
	case savedListWith(list.saved(&mc.Spec), saved, name, fg) != nil || keptByUUID(st.savedUUIDs, st.on[i].UUID):
		l := savedListWith(list.saved(&mc.Spec), saved, name, fg)
		if l == nil {
			l = savedListWithUUID(list.saved(&mc.Spec), saved, st.on[i].UUID)
		}
		return &Error{Status: http.StatusConflict, Code: "saved_list", Message: fmt.Sprintf("%s is on the list %s. Take them off that list, or stop using it here.", name, l.Name)}
	case hasName(st.kept, name) || keptByUUID(st.kept, st.on[i].UUID):
		return &Error{Status: http.StatusConflict, Code: "listed", Message: fmt.Sprintf("%s is listed in the settings. Take them out there instead.", name)}
	}
	name = st.on[i].Name
	bedrock := st.on[i].Bedrock
	switch mc.Status.Phase {
	case avsysv1.PhaseRunning:
		if bedrock && list.key == "whitelist" {
			// The game cannot look up a Bedrock player's name: the file it
			// keeps is changed, and read again.
			if err := s.changeBedrockWhitelist(ctx, mc, nil, []listedPlayer{{Name: name}}); err != nil {
				s.Log.Warn("could not take a Bedrock player off a whitelist", "server", key.String(), "player", name, "error", err)
				return &Error{Status: http.StatusBadGateway, Code: "unreachable", Message: "The server did not answer. Try again in a moment."}
			}
			break
		}
		out, err := s.listCommand(ctx, mc, list.remove+name)
		if err != nil {
			return &Error{Status: http.StatusBadGateway, Code: "unreachable", Message: "The server did not answer. Try again in a moment."}
		}
		if noSuchPlayer.MatchString(plain(out)) {
			if bedrock {
				// It knows them only once they have joined.
				return &Error{Status: http.StatusConflict, Code: "no_such_player", Message: fmt.Sprintf("The game can't find %s until they join. Take them off while the server is stopped.", name)}
			}
			return &Error{Status: http.StatusConflict, Code: "no_such_player", Message: fmt.Sprintf("The game could not find %s.", name)}
		}
	case avsysv1.PhaseStopped, avsysv1.PhaseSleeping:
		release, err := s.changing(mc, false)
		if err != nil {
			return err
		}
		defer release()
		sc, err := s.workspaces.get(ctx, mc.Namespace, mc.Name)
		if err != nil {
			return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. Try again in a moment."}
		}
		if err := dropFromListFile(sc, path.Join(dataRoot, list.file), name); err != nil {
			return fileError(err)
		}
	default:
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "Try again once the server has started or stopped."}
	}
	// Not the panel's to put back: a note that says otherwise is out of date.
	if hasName(st.made, name) {
		if err := s.dropFromNote(ctx, key, list, name); err != nil {
			return err
		}
	}
	s.audit(ctx, u, a.Org, "server.list-remove", mc.Name, name+" "+list.key)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// dropFromNote takes a player out of the note of who the panel put on a list.
func (s *Server) dropFromNote(ctx context.Context, key client.ObjectKey, list roleList, name string) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	made := slices.DeleteFunc(splitNames(mc.Annotations[list.note]), func(n string) bool { return strings.EqualFold(n, name) })
	if len(made) == 0 {
		delete(mc.Annotations, list.note)
	} else {
		mc.Annotations[list.note] = strings.Join(made, ",")
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// dropFromListFile takes one player out of a list the game keeps, such as
// ops.json, leaving every other entry as it was.
func dropFromListFile(sc *sftp.Client, file, name string) error {
	f, err := sc.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	f.Close()
	if err != nil {
		return err
	}
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		return errBadRequest("%s could not be read as a list of players.", path.Base(file))
	}
	kept := slices.DeleteFunc(slices.Clone(entries), func(e map[string]any) bool {
		n, _ := e["name"].(string)
		return strings.EqualFold(n, name)
	})
	if len(kept) == len(entries) {
		return nil
	}
	return writeListFile(sc, file, kept)
}

// writeListFile writes a list of players as the game does, in place of
// the one there in one step.
func writeListFile(sc *sftp.Client, file string, entries []map[string]any) error {
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".avsys-tmp"
	w, err := sc.Create(tmp)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sc.PosixRename(tmp, file)
	}
	if err != nil {
		_ = sc.Remove(tmp)
	}
	return err
}

// lock is the lock of one server's lists, so that two looks at the same
// server, or a look and a change, do not interleave their commands.
func (o *autoOps) lock(key client.ObjectKey) *sync.Mutex {
	lock, _ := o.locks.LoadOrStore(key.String(), &sync.Mutex{})
	return lock.(*sync.Mutex)
}
