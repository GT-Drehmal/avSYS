package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
)

// call sends a request as the named person and reads the answer.
func (p *panel) call(who, method, path, body string) (int, map[string]any) {
	p.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// A server's notes are the panel's: shown with it, changed with its other
// settings or alone, and never in its spec, where the game would have them.
func TestServerNotes(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()

	code, made := p.call("owner", "POST", "/orgs/alpha/servers", `{"name":"event","notes":"  For the Halloween event.\nDelete in November.  ","spec":{"eula":true,"version":"1.21.4","motd":"Spooky"}}`)
	if code != http.StatusCreated || made["notes"] != "For the Halloween event.\nDelete in November." {
		t.Fatalf("made with notes: %d %v", code, made)
	}
	// Not in the server itself: not its spec, not its annotations.
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "event"}, mc); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(mc)
	if strings.Contains(string(raw), "Halloween") {
		t.Errorf("the notes are in the server's resource: %s", raw)
	}
	// Everyone who sees it sees them.
	if code, got := p.call("viewer", "GET", "/orgs/alpha/servers/event", ""); code != 200 || got["notes"] != "For the Halloween event.\nDelete in November." {
		t.Errorf("seen: %d %v", code, got["notes"])
	}

	// Changed alone, even while the server is busy with something else.
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "event", kindSnapshot, ""); !ok {
		t.Fatal("could not claim the server")
	}
	if code, got := p.call("admin", "PATCH", "/orgs/alpha/servers/event", `{"notes":"Kept for screenshots."}`); code != 200 || got["notes"] != "Kept for screenshots." {
		t.Errorf("changed alone: %d %v", code, got)
	}
	p.srv.ops.end("mc-alpha", "event")
	// And with its settings.
	if code, got := p.call("admin", "PATCH", "/orgs/alpha/servers/event", `{"notes":"Done","spec":{"displayName":"Event"}}`); code != 200 || got["notes"] != "Done" || got["spec"].(map[string]any)["displayName"] != "Event" {
		t.Errorf("changed with its settings: %d %v", code, got)
	}
	// Its settings changed alone leave them.
	if code, got := p.call("admin", "PATCH", "/orgs/alpha/servers/event", `{"spec":{"displayName":"Event!"}}`); code != 200 || got["notes"] != "Done" {
		t.Errorf("settings alone: %d %v", code, got["notes"])
	}
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	var listed []serverView
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed) != 2 || listed[0].Name != "event" || listed[0].Notes != "Done" || listed[1].Notes != "" {
		t.Errorf("listed: %v %+v", err, listed)
	}
	for _, bad := range []string{`{"notes":"` + strings.Repeat("a", 501) + `"}`, `{"notes":"a\u0007b"}`, `{}`} {
		if code, got := p.call("admin", "PATCH", "/orgs/alpha/servers/event", bad); code != http.StatusBadRequest {
			t.Errorf("%.40s: %d %v, want 400", bad, code, got)
		}
	}
	if code, _ := p.call("owner", "POST", "/orgs/alpha/servers", `{"name":"long","notes":"`+strings.Repeat("a", 501)+`","spec":{"eula":true,"version":"1.21.4"}}`); code != http.StatusBadRequest {
		t.Errorf("made with notes too long: %d, want 400", code)
	}
	if got, _ := p.call("admin", "GET", "/orgs/alpha/servers/long", ""); got != http.StatusNotFound {
		t.Errorf("a server was made with notes too long: %d", got)
	}
	// Cleared with nothing.
	if code, got := p.call("admin", "PATCH", "/orgs/alpha/servers/event", `{"notes":""}`); code != 200 || got["notes"] != "" {
		t.Errorf("cleared: %d %v", code, got["notes"])
	}

	// Deleted with nothing to bring it back from, its notes go: a server
	// made under its name later does not have them.
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/servers/event", `{"notes":"Old"}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := p.call("owner", "DELETE", "/orgs/alpha/servers/event", ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	org, _ := p.srv.Store.OrgBySlug(ctx, "alpha")
	if notes, _ := p.srv.Store.ServerNotes(ctx, org.ID, "event"); notes != "" {
		t.Errorf("a deleted server's notes stayed: %q", notes)
	}
	// Notes left under a name some other way are not a new server's.
	if err := p.srv.Store.SetServerNotes(ctx, org.ID, "fresh", "Someone else's"); err != nil {
		t.Fatal(err)
	}
	if code, got := p.call("owner", "POST", "/orgs/alpha/servers", `{"name":"fresh","spec":{"eula":true,"version":"1.21.4"}}`); code != http.StatusCreated || got["notes"] != "" {
		t.Errorf("a new server under a name with notes: %d %v", code, got["notes"])
	}
}

// A copy has its original's notes unless it is given its own.
func TestACopyHasItsOriginalsNotes(t *testing.T) {
	p := newPanel(t)
	p.srv.Backups.Restic = "true"
	held := dialHeld{dialed: make(chan struct{}, 4)}
	p.srv.Gateway = &sshgw.Gateway{Dialer: held}
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/servers/world", `{"notes":"The main world."}`); code != 200 {
		t.Fatal(code)
	}
	for _, c := range []struct{ name, notes, want string }{
		{"copy", "", "The main world."},
		{"other", `,"notes":"A test copy."`, "A test copy."},
		{"bare", `,"notes":""`, ""},
	} {
		code, got := p.call("owner", "POST", "/orgs/alpha/servers", `{"name":"`+c.name+`","fromServer":"world"`+c.notes+`}`)
		if code != http.StatusCreated || got["notes"] != c.want {
			t.Errorf("%s: %d %v, want notes %q", c.name, code, got["notes"], c.want)
		}
		// Let go of the original for the next.
		if code, _ := p.call("owner", "DELETE", "/orgs/alpha/servers/"+c.name, ""); code != http.StatusNoContent {
			t.Fatalf("deleting %s: %d", c.name, code)
		}
		deadline := time.Now().Add(10 * time.Second)
		for p.srv.ops.current("mc-alpha", "world") != nil {
			if time.Now().After(deadline) {
				t.Fatal("the original is still held")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// A snapshot is named, and given a colour, in the panel. The name is
// listed with it, keeps it from the schedule, and goes when it does; so do
// the notes of a deleted server whose last snapshot it was.
func TestSnapshotNames(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	org, _ := p.srv.Store.OrgBySlug(ctx, "alpha")
	short := l.src[:8]
	path := "/orgs/alpha/snapshots/" + short

	if code, got := p.call("admin", "PATCH", path, `{"name":"  Before the update  ","color":"teal"}`); code != 200 || got["name"] != "Before the update" || got["color"] != "teal" || got["id"] != l.src {
		t.Fatalf("named: %d %v", code, got)
	}
	// Left out, the colour stays.
	if code, got := p.call("admin", "PATCH", path, `{"name":"Old spawn"}`); code != 200 || got["name"] != "Old spawn" || got["color"] != "teal" {
		t.Errorf("renamed: %d %v", code, got)
	}
	for _, bad := range []string{`{"name":"` + strings.Repeat("a", 61) + `"}`, `{"name":"two\nlines"}`, `{"color":"chartreuse"}`, `{}`} {
		if code, got := p.call("admin", "PATCH", path, bad); code != http.StatusBadRequest {
			t.Errorf("%.40s: %d %v, want 400", bad, code, got)
		}
	}
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/ffffffff", `{"name":"Nothing"}`); code != http.StatusNotFound {
		t.Errorf("a snapshot that is not there: %d, want 404", code)
	}
	// Listed with it, and said of it.
	code, list := p.call("viewer", "GET", "/orgs/alpha/snapshots", "")
	snaps, _ := list["snapshots"].([]any)
	if code != 200 || len(snaps) != 1 || snaps[0].(map[string]any)["name"] != "Old spawn" || snaps[0].(map[string]any)["color"] != "teal" {
		t.Errorf("listed: %d %v", code, list)
	}
	if code, got := p.call("viewer", "GET", path, ""); code != 200 || got["name"] != "Old spawn" {
		t.Errorf("its details: %d %v", code, got)
	}

	// Its server is gone; its notes are kept while it can be brought back,
	// and are what a server made from the snapshot starts with.
	if err := p.srv.Store.SetServerNotes(ctx, org.ID, "survival", "Our first world."); err != nil {
		t.Fatal(err)
	}
	if code, got := p.call("viewer", "GET", path, ""); code != 200 || got["notes"] != "Our first world." {
		t.Errorf("the notes of its server: %d %v", code, got["notes"])
	}

	// A template's version is not named here.
	l.made("admin", "POST", "/orgs/alpha/templates?wait=true", `{"name":"Survival","snapshot":"`+l.src+`"}`, http.StatusCreated)
	all, err := p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, s := range all {
		if isTemplate(&s) {
			version = s.ID
		}
	}
	if version == "" {
		t.Fatalf("no version among %+v", all)
	}
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/"+version, `{"name":"Mine"}`); code == http.StatusOK {
		t.Error("a template's version was named")
	}

	// Taking the name and the colour away leaves nothing.
	if code, got := p.call("admin", "PATCH", path, `{"name":"","color":""}`); code != 200 || got["name"] != nil || got["color"] != nil {
		t.Errorf("unnamed: %d %v", code, got)
	}
	if names, _ := p.srv.Store.SnapshotNames(ctx, org.ID); len(names) != 0 {
		t.Errorf("unnamed, still kept: %v", names)
	}

	// Deleted, its name goes with it, and so do the notes of its server,
	// which can no longer be brought back.
	if code, _ := p.call("admin", "PATCH", path, `{"name":"Last"}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := p.call("admin", "DELETE", path, ""); code != http.StatusNoContent {
		t.Fatalf("deleting: %d", code)
	}
	if names, _ := p.srv.Store.SnapshotNames(ctx, org.ID); len(names) != 0 {
		t.Errorf("a deleted snapshot's name stayed: %v", names)
	}
	if notes, _ := p.srv.Store.ServerNotes(ctx, org.ID, "survival"); notes != "" {
		t.Errorf("the notes of a server with nothing left to bring it back from stayed: %q", notes)
	}

	// The name of one removed some other way goes the next time the list
	// is read.
	l.change()
	list2, err := p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var other string
	for _, s := range list2 {
		if s.Server == "elsewhere" {
			other = s.ID
		}
	}
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/"+other, `{"name":"Soon gone"}`); code != 200 {
		t.Fatal(code)
	}
	l.restic(t.TempDir(), "forget", other)
	// Named a moment ago, it is not forgotten by a list read in the same second.
	time.Sleep(1100 * time.Millisecond)
	if code, _ := p.call("viewer", "GET", "/orgs/alpha/snapshots", ""); code != 200 {
		t.Fatal(code)
	}
	if names, _ := p.srv.Store.SnapshotNames(ctx, org.ID); len(names) != 0 {
		t.Errorf("the name of a snapshot removed elsewhere stayed: %v", names)
	}

	// A scheduled snapshot of a server that exists is the schedule's to
	// remove, unless it is named.
	l.restic(l.files, "backup", "data", "--host", "world", "--tag", "avsys", "--tag", "trigger=scheduled")
	list3, err := p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var scheduled string
	for _, s := range list3 {
		if s.Server == "world" {
			scheduled = s.ID
		}
	}
	retention := func() map[string]any {
		t.Helper()
		code, got := p.call("viewer", "GET", "/orgs/alpha/snapshots/"+scheduled, "")
		if code != 200 {
			t.Fatalf("details: %d %v", code, got)
		}
		r, _ := got["retention"].(map[string]any)
		return r
	}
	if r := retention(); r["scheduled"] != true || r["named"] != nil {
		t.Errorf("unnamed: %v", r)
	}
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/"+scheduled, `{"color":"rose"}`); code != 200 {
		t.Fatal(code)
	}
	if r := retention(); r["scheduled"] != true {
		t.Errorf("a colour alone kept it from the schedule: %v", r)
	}
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/"+scheduled, `{"name":"Keep me"}`); code != 200 {
		t.Fatal(code)
	}
	if r := retention(); r["scheduled"] != false || r["named"] != true {
		t.Errorf("named: %v", r)
	}
}
