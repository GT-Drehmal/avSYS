package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An organization's saved lists of players: named lists, such as its club's
// members or those signed up for an event, that its servers put on their
// whitelist or operators. A server names the lists it uses, and follows
// each as it is changed. Everyone in the organization sees them; admins,
// who can change any server's whitelist and operators, change them.

// playerListView is a saved list as the pages show it.
type playerListView struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	CreatedBy string             `json:"createdBy,omitempty"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Players   []store.ListPlayer `json:"players"`
	// Servers are those that use it, and for which of their lists.
	Servers []listUse `json:"servers"`
}

// listUse is a server that uses a saved list.
type listUse struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Whitelist   bool   `json:"whitelist,omitempty"`
	Ops         bool   `json:"ops,omitempty"`
}

// listUses is, by list ID, the servers of an organization that use each.
func (s *Server) listUses(ctx context.Context, a *access) (map[string][]listUse, error) {
	var servers avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &servers, client.InNamespace(a.Namespace)); err != nil {
		return nil, err
	}
	out := map[string][]listUse{}
	for _, mc := range servers.Items {
		ids := map[string]*listUse{}
		var order []string
		use := func(id string) *listUse {
			if ids[id] == nil {
				ids[id] = &listUse{Name: mc.Name, DisplayName: mc.Spec.DisplayName}
				order = append(order, id)
			}
			return ids[id]
		}
		for _, id := range mc.Spec.WhitelistLists {
			use(id).Whitelist = true
		}
		for _, id := range mc.Spec.OpLists {
			use(id).Ops = true
		}
		for _, id := range order {
			out[id] = append(out[id], *ids[id])
		}
	}
	for id := range out {
		slices.SortFunc(out[id], func(a, b listUse) int { return strings.Compare(a.Name, b.Name) })
	}
	return out, nil
}

func listViewOf(l *store.PlayerList, uses map[string][]listUse) playerListView {
	v := playerListView{ID: l.ID, Name: l.Name, CreatedBy: l.CreatedBy, UpdatedAt: l.UpdatedAt, Players: l.Players, Servers: uses[l.ID]}
	if v.Players == nil {
		v.Players = []store.ListPlayer{}
	}
	if v.Servers == nil {
		v.Servers = []listUse{}
	}
	return v
}

// writePlayerList answers with one list as it is now.
func (s *Server) writePlayerList(w http.ResponseWriter, r *http.Request, a *access, id string, status int, missing []string) error {
	l, err := s.Store.PlayerList(r.Context(), a.Org.ID, id)
	if err != nil {
		return err
	}
	uses, err := s.listUses(r.Context(), a)
	if err != nil {
		return err
	}
	if missing == nil {
		missing = []string{}
	}
	writeJSON(w, status, struct {
		playerListView
		// Missing are the names asked for that no Minecraft account has.
		Missing []string `json:"missing"`
	}{listViewOf(l, uses), missing})
	return nil
}

func (s *Server) listPlayerLists(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	lists, err := s.Store.PlayerLists(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	uses, err := s.listUses(r.Context(), a)
	if err != nil {
		return err
	}
	out := make([]playerListView, len(lists))
	for i := range lists {
		out[i] = listViewOf(&lists[i], uses)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) getPlayerList(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	return s.writePlayerList(w, r, a, r.PathValue("list"), http.StatusOK, nil)
}

// listName checks a list's name.
func listName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 {
		return "", errBadRequest("Give the list a name of up to 60 characters.")
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return "", errBadRequest("A list's name cannot have control characters.")
		}
	}
	return name, nil
}

func listNameTaken(name string) error {
	return &Error{Status: http.StatusConflict, Code: "name_taken", Message: "There is already a list called " + name + "."}
}

// listStoreError says plainly what a refused change to a list was.
func listStoreError(err error, name string) error {
	switch {
	case errors.Is(err, store.ErrPlayerListNameTaken):
		return listNameTaken(name)
	case errors.Is(err, store.ErrPlayerListFull):
		return errBadRequest("A list can have up to %d players.", store.MaxListPlayers)
	case errors.Is(err, store.ErrTooManyPlayerLists):
		return errBadRequest("An organization can have up to %d lists.", store.MaxPlayerLists)
	}
	return err
}

// createPlayerList makes a list: empty, of the players named, or of who is
// on one of a server's lists now, in game as well as by the panel.
func (s *Server) createPlayerList(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Name  string
		Names []string
		// FromServer and List are a server of the organization and one of
		// its lists, "whitelist" or "ops", whose players the list starts with.
		FromServer string
		List       string
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	name, err := listName(in.Name)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var players []store.ListPlayer
	var missing []string
	if in.FromServer != "" {
		players, missing, err = s.playersOnServer(ctx, a, in.FromServer, in.List)
	} else {
		players, missing, err = s.lookUpPlayers(ctx, in.Names)
	}
	if err != nil {
		return err
	}
	l := &store.PlayerList{OrgID: a.Org.ID, Name: name, CreatedBy: u.Username, Players: players}
	if err := s.Store.CreatePlayerList(ctx, l); err != nil {
		return listStoreError(err, name)
	}
	s.audit(ctx, u, a.Org, "players.create", l.Name, "")
	return s.writePlayerList(w, r, a, l.ID, http.StatusCreated, missing)
}

// playersOnServer is who is on one of a server's lists: in game where that
// can be read, and who the panel puts there besides. Players the game has
// a UUID for are taken as they are; the rest are looked up.
func (s *Server) playersOnServer(ctx context.Context, a *access, server, which string) ([]store.ListPlayer, []string, error) {
	list, ok := roleListByKey(which)
	if !ok {
		return nil, nil, errBadRequest("Say which list: whitelist or ops.")
	}
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: server}, mc); err != nil {
		return nil, nil, err
	}
	members, err := s.Store.Members(ctx, a.Org.ID)
	if err != nil {
		return nil, nil, err
	}
	saved, err := s.savedLists(ctx, a.Org.ID)
	if err != nil {
		return nil, nil, err
	}
	var players []store.ListPlayer
	var names []string
	// One that cannot be read is left out: what the panel puts there is
	// still known.
	on, _ := s.readList(ctx, mc, list)
	for _, p := range on {
		if uuid := bareUUID(p.UUID); uuid != "" {
			players = append(players, store.ListPlayer{UUID: uuid, Name: p.Name})
		} else {
			names = append(names, p.Name)
		}
	}
	for _, name := range withKept(wantedOn(list, &mc.Spec, members, saved), list.kept(&mc.Spec)) {
		if !slices.ContainsFunc(players, func(p store.ListPlayer) bool { return strings.EqualFold(p.Name, name) }) && !hasName(names, name) {
			names = append(names, name)
		}
	}
	found, missing, err := s.lookUpPlayers(ctx, names)
	if err != nil {
		return nil, nil, err
	}
	return append(players, found...), missing, nil
}

// bareUUID is a player's UUID as Mojang gives it, without dashes, or "" for
// what is not one.
func bareUUID(uuid string) string {
	if !playerUUIDPattern.MatchString(uuid) {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(uuid, "-", ""))
}

// lookUpPlayers asks Mojang who each name belongs to, a few at a time.
// Names no account has, and what could not be a name, are missing. Any
// other failure fails the whole: nothing is added on half an answer.
func (s *Server) lookUpPlayers(ctx context.Context, names []string) ([]store.ListPlayer, []string, error) {
	var asked []string
	var missing []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		switch {
		case name == "" || hasName(asked, name) || hasName(missing, name):
		case !minecraftNamePattern.MatchString(name):
			missing = append(missing, name)
		default:
			asked = append(asked, name)
		}
	}
	if len(asked) > store.MaxListPlayers {
		return nil, nil, errBadRequest("Up to %d players can be added at once.", store.MaxListPlayers)
	}
	found := make([]*store.MinecraftAccount, len(asked))
	errs := make([]error, len(asked))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(6, len(asked)) {
		wg.Go(func() {
			for i := range next {
				found[i], errs[i] = s.minecraftProfile(ctx, asked[i])
			}
		})
	}
	for i := range asked {
		next <- i
	}
	close(next)
	wg.Wait()
	var out []store.ListPlayer
	for i, name := range asked {
		var e *Error
		switch {
		case errs[i] == nil:
			if !slices.ContainsFunc(out, func(p store.ListPlayer) bool { return p.UUID == found[i].UUID }) {
				out = append(out, store.ListPlayer{UUID: found[i].UUID, Name: found[i].Name})
			}
		case errors.As(errs[i], &e) && e.Code == "no_such_player":
			missing = append(missing, name)
		default:
			s.Log.Warn("could not look up a player", "name", name, "error", errs[i])
			return nil, nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not check the names with Mojang. Try again in a moment."}
		}
	}
	return out, missing, nil
}

// updatePlayerList renames a list.
func (s *Server) updatePlayerList(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		return err
	}
	name, err := listName(in.Name)
	if err != nil {
		return err
	}
	id := r.PathValue("list")
	if err := s.Store.RenamePlayerList(r.Context(), a.Org.ID, id, name); err != nil {
		return listStoreError(err, name)
	}
	s.audit(r.Context(), u, a.Org, "players.rename", name, "")
	return s.writePlayerList(w, r, a, id, http.StatusOK, nil)
}

// deletePlayerList forgets a list, and takes it out of the servers that
// use it: its players come off their lists, as if taken off it.
func (s *Server) deletePlayerList(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("list")
	l, err := s.Store.PlayerList(ctx, a.Org.ID, id)
	if err != nil {
		return err
	}
	if err := s.Store.DeletePlayerList(ctx, a.Org.ID, id); err != nil {
		return err
	}
	if err := s.dropSavedList(ctx, a, id); err != nil {
		// A server that still names it follows a list that is not there:
		// nobody, as it would be taken out of it on its next change.
		s.Log.Warn("could not take a deleted list out of every server", "org", a.Org.Slug, "list", id, "error", err)
	}
	s.audit(ctx, u, a.Org, "players.delete", l.Name, "")
	s.syncOpsSoon(a.Org)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// dropSavedList takes a list out of every server of the organization that
// uses it. Each is read as it is now and changed only if it has not been
// changed since, so that lists picked for it meanwhile are kept.
func (s *Server) dropSavedList(ctx context.Context, a *access, id string) error {
	var servers avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &servers, client.InNamespace(a.Namespace)); err != nil {
		return err
	}
	var failed error
	for i := range servers.Items {
		if err := s.dropSavedListFrom(ctx, &servers.Items[i], id); err != nil {
			failed = err
		}
	}
	return failed
}

func (s *Server) dropSavedListFrom(ctx context.Context, mc *avsysv1.MinecraftServer, id string) error {
	drop := func(ids []string) []string {
		out := slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == id })
		if len(out) == 0 {
			return nil
		}
		return out
	}
	for attempt := 1; ; attempt++ {
		now, err := s.live(ctx, mc)
		if err != nil {
			return client.IgnoreNotFound(err)
		}
		if !slices.Contains(now.Spec.WhitelistLists, id) && !slices.Contains(now.Spec.OpLists, id) {
			return nil
		}
		orig := now.DeepCopy()
		now.Spec.WhitelistLists, now.Spec.OpLists = drop(now.Spec.WhitelistLists), drop(now.Spec.OpLists)
		err = s.Client.Patch(ctx, now, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
		if !apierrors.IsConflict(err) || attempt == 5 {
			return err
		}
	}
}

// addListPlayers puts the players named on a list, as Mojang has them.
func (s *Server) addListPlayers(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct{ Names []string }
	if err := decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("list")
	l, err := s.Store.PlayerList(ctx, a.Org.ID, id)
	if err != nil {
		return err
	}
	found, missing, err := s.lookUpPlayers(ctx, in.Names)
	if err != nil {
		return err
	}
	if len(found) > 0 {
		if err := s.checkRoomFor(ctx, a, id, found); err != nil {
			return err
		}
		if err := s.Store.AddListPlayers(ctx, a.Org.ID, id, found); err != nil {
			return listStoreError(err, l.Name)
		}
		names := make([]string, len(found))
		for i, p := range found {
			names[i] = p.Name
		}
		s.audit(ctx, u, a.Org, "players.add", l.Name, strings.Join(names, " "))
		s.syncOpsSoon(a.Org)
	}
	return s.writePlayerList(w, r, a, id, http.StatusOK, missing)
}

// checkRoomFor says whether every server that uses a list has room for
// more players from saved lists, with these added to it.
func (s *Server) checkRoomFor(ctx context.Context, a *access, id string, more []store.ListPlayer) error {
	saved, err := s.savedLists(ctx, a.Org.ID)
	if err != nil {
		return err
	}
	var servers avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &servers, client.InNamespace(a.Namespace)); err != nil {
		return err
	}
	for _, mc := range servers.Items {
		for _, ids := range [][]string{mc.Spec.WhitelistLists, mc.Spec.OpLists} {
			if slices.Contains(ids, id) && savedPlayerCount(ids, saved, more) > maxFromSavedLists {
				return errBadRequest("%s would have more than %d players from saved lists.", cmp.Or(mc.Spec.DisplayName, mc.Name), maxFromSavedLists)
			}
		}
	}
	return nil
}

// removeListPlayer takes a player off a list, by UUID.
func (s *Server) removeListPlayer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("list")
	l, err := s.Store.PlayerList(ctx, a.Org.ID, id)
	if err != nil {
		return err
	}
	uuid := r.PathValue("uuid")
	i := slices.IndexFunc(l.Players, func(p store.ListPlayer) bool { return p.UUID == uuid })
	if i < 0 {
		return errNotFound
	}
	if err := s.Store.RemoveListPlayer(ctx, a.Org.ID, id, uuid); err != nil {
		return err
	}
	s.audit(ctx, u, a.Org, "players.remove", l.Name, l.Players[i].Name)
	s.syncOpsSoon(a.Org)
	return s.writePlayerList(w, r, a, id, http.StatusOK, nil)
}
