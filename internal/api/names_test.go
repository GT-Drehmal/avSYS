package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/mojang"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// hangingAt is offline, but for addresses that never answer.
type hangingAt struct {
	offline
	hang map[string]bool
}

func (h hangingAt) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.hang[r.URL.String()] {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return h.offline.RoundTrip(r)
}

// Who a name is can change: with Mojang not answering, a lookup kept past
// its time is not used, as the version catalog's would be. Linking fails
// rather than link whoever had the name before.
func TestNameLookupsAreNotStale(t *testing.T) {
	p := newPanel(t)
	m := newMojangAccounts("Steve")
	m.down = true
	p.srv.catalog.http.Transport = m
	before := strings.Repeat("e", 32)
	p.srv.catalog.keep(profileURL("Steve"), []byte(`{"id":"`+before+`","name":"Steve"}`), -time.Minute)
	p.srv.catalog.keep(sessionProfileURL(before), []byte(`{"id":"`+before+`","name":"Steve"}`), -time.Minute)
	if code, text := call(p, "viewer", "PUT", "/me/minecraft", `{"name":"Steve"}`); code != http.StatusBadGateway {
		t.Errorf("linked with Mojang down: %d %s", code, text)
	}
	if _, err := p.srv.catalog.getOr(context.Background(), sessionProfileURL(before), time.Hour, noContent); err == nil {
		t.Errorf("an old profile was used")
	}
	// The version catalog's old copy still is.
	p.srv.catalog.keep(mojangManifest, []byte(`{"old":true}`), -time.Hour)
	if body, err := p.srv.catalog.get(context.Background(), mojangManifest, time.Hour); err != nil || string(body) != `{"old":true}` {
		t.Errorf("the version list: %s %v", body, err)
	}
	m.down = false
	if code, text := call(p, "viewer", "PUT", "/me/minecraft", `{"name":"Steve"}`); code != http.StatusOK || !strings.Contains(text, m.known["steve"]) {
		t.Errorf("linked once Mojang answers: %d %s", code, text)
	}
}

// Members' accounts are looked up for new names in turn: one that never
// answers, and takes all of a look's time, does not keep the others from
// being looked up with the next.
func TestRenamesTakeTurns(t *testing.T) {
	askMojang(t, 50*time.Millisecond, time.Millisecond, mojang.Dial)
	defer func(d time.Duration) { refreshFor = d }(refreshFor)
	refreshFor = 150 * time.Millisecond
	p := newPanel(t)
	stuck, renamed := strings.Repeat("1", 32), strings.Repeat("2", 32)
	p.srv.catalog.http.Transport = hangingAt{
		offline: offline{
			"https://api.mojang.com/users/profiles/minecraft/stuck": `{"id":"` + stuck + `","name":"Stuck"}`,
			"https://api.mojang.com/users/profiles/minecraft/steve": `{"id":"` + renamed + `","name":"Steve"}`,
			sessionProfileURL(renamed):                              `{"id":"` + renamed + `","name":"Stefan"}`,
		},
		hang: map[string]bool{sessionProfileURL(stuck): true},
	}
	for who, name := range map[string]string{"operator": "stuck", "admin": "steve"} {
		if code, text := call(p, who, "PUT", "/me/minecraft", `{"name":"`+name+`"}`); code != http.StatusOK {
			t.Fatalf("linking %s: %d %s", name, code, text)
		}
	}
	ctx := t.Context()
	alpha, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	// What a link sets going may have looked already: from the start.
	p.srv.memberNameChecks.Clear()
	p.srv.catalog.entries = map[string]cacheEntry{}
	for range 2 {
		members, err := p.srv.Store.Members(ctx, alpha.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The one that never answers comes first.
		slices.SortStableFunc(members, func(a, b store.Member) int {
			return strings.Compare(b.Username, a.Username)
		})
		p.srv.refreshNames(ctx, members)
	}
	if got := p.members("alpha")["admin"]; got != "Stefan" {
		t.Errorf("after two looks: %q, want Stefan", got)
	}
}
