package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A Minecraft name is 3 to 16 letters, digits and underscores; a few old
// accounts have two.
var minecraftNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{2,16}$`)

// minecraftProfile asks Mojang which account a name belongs to now.
func (s *Server) minecraftProfile(ctx context.Context, name string) (*store.MinecraftAccount, error) {
	if !minecraftNamePattern.MatchString(name) {
		return nil, errBadRequest("%q is not a Minecraft username.", name)
	}
	body, err := s.catalog.get(ctx, "https://api.mojang.com/users/profiles/minecraft/"+url.PathEscape(name), 5*time.Minute)
	if errors.Is(err, errNotFound) {
		return nil, &Error{Status: http.StatusNotFound, Code: "no_such_player", Message: "There is no Minecraft account named " + name + "."}
	}
	if err != nil {
		return nil, err
	}
	var profile struct{ ID, Name string }
	if err := json.Unmarshal(body, &profile); err != nil || profile.ID == "" || !minecraftNamePattern.MatchString(profile.Name) {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Mojang's answer about that name could not be read."}
	}
	return &store.MinecraftAccount{UUID: profile.ID, Name: profile.Name}, nil
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
	if err := s.Store.SetMinecraftAccount(r.Context(), u.ID, account); err != nil {
		return err
	}
	s.syncOpsOf(u)
	writeJSON(w, http.StatusOK, account)
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

// wantedOps is who should be an operator on a server for their role: the
// linked players of every member at or above the server's role, by name.
func wantedOps(spec *avsysv1.MinecraftServerSpec, members []store.Member) []string {
	min := opRole(spec)
	var out []string
	if min == "" {
		return out
	}
	seen := map[string]bool{}
	for _, m := range members {
		key := strings.ToLower(m.MinecraftName)
		if m.MinecraftName != "" && m.Role.AtLeast(min) && !seen[key] {
			seen[key] = true
			out = append(out, m.MinecraftName)
		}
	}
	sort.Strings(out)
	return out
}

// splitNames reads a note of player names. Anything in it that could not
// be a player's name is dropped: these end up in commands typed at a
// server's console, and the note travels with snapshots, which a server's
// own workspace can write.
func splitNames(s string) []string {
	var out []string
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); minecraftNamePattern.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}

// opChanges is what to do to get from the operators the panel has made on a
// server to the ones it should have. kept are operators set by hand in the
// server's settings, which are never taken off here.
func opChanges(made, wanted, kept []string) (add, remove []string) {
	has := func(list []string, name string) bool {
		for _, n := range list {
			if strings.EqualFold(n, name) {
				return true
			}
		}
		return false
	}
	for _, name := range wanted {
		if !has(made, name) {
			add = append(add, name)
		}
	}
	for _, name := range made {
		if !has(wanted, name) && !has(kept, name) {
			remove = append(remove, name)
		}
	}
	return add, remove
}

// syncOps makes one running server's operators what its organization's
// roles say. It does nothing, and costs nothing, when they already are.
func (s *Server) syncOps(ctx context.Context, mc *avsysv1.MinecraftServer, members []store.Member) error {
	if mc.Status.Phase != avsysv1.PhaseRunning {
		// One that is not running is put right when it next is: the note
		// of who was made an operator stays as it is until then.
		return nil
	}
	key := client.ObjectKeyFromObject(mc)
	lock, _ := s.autoOps.locks.LoadOrStore(key.String(), &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

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
	made := splitNames(fresh.Annotations[avsysv1.AnnotationAutoOps])
	wanted := wantedOps(&fresh.Spec, members)
	add, remove := opChanges(made, wanted, fresh.Spec.Ops)
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	for _, name := range add {
		if _, err := s.rcon(ctx, fresh, "op "+name); err != nil {
			return err
		}
	}
	for _, name := range remove {
		if _, err := s.rcon(ctx, fresh, "deop "+name); err != nil {
			return err
		}
	}
	orig := fresh.DeepCopy()
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	if len(wanted) == 0 {
		delete(fresh.Annotations, avsysv1.AnnotationAutoOps)
	} else {
		fresh.Annotations[avsysv1.AnnotationAutoOps] = strings.Join(wanted, ",")
	}
	if err := s.Client.Patch(ctx, fresh, client.MergeFrom(orig)); err != nil {
		return err
	}
	s.Log.Info("operators brought in step with roles", "server", key.String(), "added", add, "removed", remove)
	return nil
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
	for i := range members {
		m := &members[i]
		if m.MinecraftUUID == "" {
			continue
		}
		body, err := s.catalog.get(ctx, "https://sessionserver.mojang.com/session/minecraft/profile/"+url.PathEscape(m.MinecraftUUID), 6*time.Hour)
		if err != nil {
			continue
		}
		var profile struct{ Name string }
		if json.Unmarshal(body, &profile) != nil || !minecraftNamePattern.MatchString(profile.Name) || profile.Name == m.MinecraftName {
			continue
		}
		if err := s.Store.RenameMinecraftAccount(ctx, m.UserID, profile.Name); err != nil {
			continue
		}
		s.Log.Info("a linked Minecraft account was renamed", "user", m.Username, "from", m.MinecraftName, "to", profile.Name)
		m.MinecraftName = profile.Name
	}
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
// with their organization's roles, until ctx ends. Changes made in the panel
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
	running := map[string]bool{}
	for i := range all.Items {
		if all.Items[i].Status.Phase == avsysv1.PhaseRunning {
			running[all.Items[i].Namespace] = true
		}
	}
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
	}
}

// noteAutoOps sets a server's note of which operators the panel made, to
// what a snapshot recorded of it.
func (s *Server) noteAutoOps(ctx context.Context, key client.ObjectKey, recorded string) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	made := strings.Join(splitNames(recorded), ",")
	if mc.Annotations[avsysv1.AnnotationAutoOps] == made {
		return nil
	}
	orig := mc.DeepCopy()
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	if made == "" {
		delete(mc.Annotations, avsysv1.AnnotationAutoOps)
	} else {
		mc.Annotations[avsysv1.AnnotationAutoOps] = made
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}
