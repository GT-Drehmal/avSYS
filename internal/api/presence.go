package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// presenceFor is how long a tab is counted as on a page with no word from
// it. Each tells the panel where it is every 25 seconds.
const presenceFor = time.Minute

// presence is which servers, and which of their files, each open tab of the
// panel is on: kept in memory only. A tab is gone when its event stream
// closes, or when it has not said where it is for presenceFor.
type presence struct {
	mu   sync.Mutex
	tabs map[string]*tabPlace // by user ID and the tab's own ID
	// told is held from looking at who is where to telling it, so that
	// what is told last is what is so now.
	told sync.Mutex
}

type tabPlace struct {
	user    presentUser
	streams int
	org, ns string
	server  string
	file    string
	seen    time.Time
}

type presentUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

// Present is someone on a server's pages, and the file they have open there.
type Present struct {
	User   presentUser `json:"user"`
	Server string      `json:"server"`
	File   string      `json:"file,omitempty"`
}

// presenceView is who is on an organization's servers now.
type presenceView struct {
	Org    string    `json:"org"`
	People []Present `json:"people"`
}

// withoutFiles is the view for one who cannot see files: who is where,
// without what files.
func (v presenceView) withoutFiles() presenceView {
	people := make([]Present, len(v.People))
	for i, p := range v.People {
		p.File = ""
		people[i] = p
	}
	return presenceView{Org: v.Org, People: dedupe(people)}
}

var tabID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func tabKey(u *store.User, tab string) string { return u.ID + "/" + tab }

func userOf(u *store.User) presentUser {
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	return presentUser{ID: u.ID, Name: name, AvatarURL: u.AvatarURL}
}

// view is who is on an organization's servers, by its namespace. The lock
// is held.
func (p *presence) view(org, ns string) presenceView {
	var people []Present
	for _, t := range p.tabs {
		if t.ns == ns && t.server != "" {
			people = append(people, Present{User: t.user, Server: t.server, File: t.file})
		}
	}
	return presenceView{Org: org, People: dedupe(people)}
}

// dedupe drops those listed twice, as one with two tabs on the same page,
// and puts them in order.
func dedupe(people []Present) []Present {
	sort.Slice(people, func(i, j int) bool {
		a, b := people[i], people[j]
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		if a.User.Name != b.User.Name {
			return a.User.Name < b.User.Name
		}
		if a.User.ID != b.User.ID {
			return a.User.ID < b.User.ID
		}
		return a.File < b.File
	})
	out := []Present{}
	for i, p := range people {
		if i == 0 || p != people[i-1] {
			out = append(out, p)
		}
	}
	return out
}

// publish tells an organization's members who is on its servers now.
func (s *Server) publishPresence(org, ns string) {
	s.presence.told.Lock()
	defer s.presence.told.Unlock()
	s.presence.mu.Lock()
	v := s.presence.view(org, ns)
	s.presence.mu.Unlock()
	s.Events.Publish(Event{Type: "presence", Namespace: ns, Data: v})
}

// streamOpened counts an event stream of a tab, and says how to count it
// closed: a tab with none open is gone.
func (s *Server) streamOpened(u *store.User, tab string) (closed func()) {
	if !tabID.MatchString(tab) {
		return func() {}
	}
	key := tabKey(u, tab)
	s.presence.mu.Lock()
	if s.presence.tabs == nil {
		s.presence.tabs = map[string]*tabPlace{}
	}
	t := s.presence.tabs[key]
	if t == nil {
		t = &tabPlace{user: userOf(u), seen: time.Now()}
		s.presence.tabs[key] = t
	}
	t.streams++
	s.presence.mu.Unlock()
	return func() {
		s.presence.mu.Lock()
		t.streams--
		gone := t.streams <= 0 && s.presence.tabs[key] == t
		if gone {
			delete(s.presence.tabs, key)
		}
		s.presence.mu.Unlock()
		if gone && t.server != "" {
			s.publishPresence(t.org, t.ns)
		}
	}
}

// presenceOf is who is on the servers of these organizations now, by
// namespace and slug.
func (s *Server) presenceOf(orgs map[string]string) []presenceView {
	s.presence.mu.Lock()
	defer s.presence.mu.Unlock()
	out := make([]presenceView, 0, len(orgs))
	for ns, org := range orgs {
		out = append(out, s.presence.view(org, ns))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return out
}

// getPresence is who is on an organization's servers now, for a page that
// may have missed them come and go.
func (s *Server) getPresence(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	v := s.presenceOf(map[string]string{a.Namespace: a.Org.Slug})[0]
	if !a.Role.AtLeast(store.RoleOperator) {
		v = v.withoutFiles()
	}
	writeJSON(w, http.StatusOK, v.People)
	return nil
}

// putPresence notes which of an organization's servers, and which file of
// it, a tab is on; with none, that it left them.
func (s *Server) putPresence(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct {
		Tab    string `json:"tab"`
		Server string `json:"server"`
		File   string `json:"file"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if !tabID.MatchString(in.Tab) {
		return errBadRequest("Name the tab.")
	}
	if in.Server != "" && !serverNamePattern.MatchString(in.Server) {
		return errBadRequest("That is not a server's name.")
	}
	if len(in.File) > 1024 || (in.Server == "" && in.File != "") {
		return errBadRequest("That is not a file of a server.")
	}
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	// Only those who can open files say which.
	if !a.Role.AtLeast(store.RoleOperator) {
		in.File = ""
	}
	key := tabKey(u, in.Tab)
	s.presence.mu.Lock()
	if s.presence.tabs == nil {
		s.presence.tabs = map[string]*tabPlace{}
	}
	t := s.presence.tabs[key]
	if t == nil {
		t = &tabPlace{user: userOf(u)}
		s.presence.tabs[key] = t
	}
	was := *t
	t.user, t.seen = userOf(u), time.Now()
	t.org, t.ns, t.server, t.file = a.Org.Slug, a.Namespace, in.Server, in.File
	if in.Server == "" {
		t.org, t.ns = "", ""
	}
	s.presence.mu.Unlock()
	if was.ns != "" && was.ns != a.Namespace && was.server != "" {
		s.publishPresence(was.org, was.ns)
	}
	if was.ns != t.ns || was.server != t.server || was.file != t.file || was.user != t.user {
		s.publishPresence(a.Org.Slug, a.Namespace)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// RunPresence forgets tabs that have not said where they are for a while,
// as one whose computer slept, until ctx ends.
func (s *Server) RunPresence(ctx context.Context) {
	tick := time.NewTicker(presenceFor / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.forgetQuiet(now)
		}
	}
}

func (s *Server) forgetQuiet(now time.Time) {
	type org struct{ slug, ns string }
	left := map[org]bool{}
	s.presence.mu.Lock()
	for key, t := range s.presence.tabs {
		if now.Sub(t.seen) < presenceFor {
			continue
		}
		if t.server != "" {
			left[org{t.org, t.ns}] = true
		}
		if t.streams > 0 {
			// Its stream is open but it has not said where it is: it is on
			// none of them.
			t.org, t.ns, t.server, t.file = "", "", "", ""
			continue
		}
		delete(s.presence.tabs, key)
	}
	s.presence.mu.Unlock()
	for o := range left {
		s.publishPresence(o.slug, o.ns)
	}
}
