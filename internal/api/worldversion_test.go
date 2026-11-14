package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// A server is given a world last opened in a newer game version than it
// runs, or is set to a version older than its world, only when the request
// says that is meant: the same everywhere.
func TestNewerWorldIsRefusedUnlessMeant(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	const world = "/orgs/alpha/servers/world"

	// Its version changed to an older one than its world was opened in.
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"version":"1.21"}}`); got != http.StatusConflict || code != "newer_world" {
		t.Errorf("made older than its world: got %d %q, want 409 newer_world", got, code)
	}
	if v := p.versionOf("world"); v != "1.21.4" {
		t.Errorf("refused, it is on %s", v)
	}
	if got, code := p.do("owner", "PATCH", world+"?newerWorld=true", `{"spec":{"version":"1.21"}}`); got != http.StatusOK || p.versionOf("world") != "1.21" {
		t.Errorf("made older, as meant: got %d %q, on %s", got, code, p.versionOf("world"))
	}
	// A newer one is never asked about.
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"version":"26.2"}}`); got != http.StatusOK {
		t.Errorf("made newer: got %d %q", got, code)
	}
	// The world is as new as the version the game last said it ran.
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Status.Version = "Vanilla 26.3"
	if err := p.srv.Client.Status().Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"version":"26.1.1"}}`); got != http.StatusConflict || code != "newer_world" {
		t.Errorf("made older than the version it ran: got %d %q, want 409 newer_world", got, code)
	}
	if got, code := p.do("owner", "PATCH", world+"?newerWorld=true", `{"spec":{"version":"1.21.4"}}`); got != http.StatusOK {
		t.Errorf("put back on 1.21.4: got %d %q", got, code)
	}
	// Anything else about it is changed as before.
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"displayName":"World"}}`); got != http.StatusOK {
		t.Errorf("renamed: got %d %q", got, code)
	}

	// Copied with its world, to an older version than that world was
	// opened in. A node that is not there stops one let through.
	p.srv.Backups.Restic = "true"
	post := func(query, body string) (int, string) {
		return p.do("owner", "POST", "/orgs/alpha/servers"+query, body)
	}
	if got, code := post("", `{"name":"older","fromServer":"world","node":"nowhere","spec":{"version":"1.21"}}`); got != http.StatusConflict || code != "newer_world" {
		t.Errorf("copied to an older version: got %d %q, want 409 newer_world", got, code)
	}
	if got, code := post("?newerWorld=true", `{"name":"older","fromServer":"world","node":"nowhere","spec":{"version":"1.21"}}`); code == "newer_world" {
		t.Errorf("copied to an older version, as meant: got %d %q", got, code)
	}
	// With a new world there is nothing to ask, nor on the version its
	// world was opened in.
	if got, code := post("", `{"name":"older","fromServer":"world","world":"new","node":"nowhere","spec":{"version":"1.21"}}`); code == "newer_world" {
		t.Errorf("copied with a new world: got %d %q", got, code)
	}
	if got, code := post("", `{"name":"same","fromServer":"world","node":"nowhere","spec":{"version":"26.3"}}`); code == "newer_world" {
		t.Errorf("copied on 26.3: got %d %q", got, code)
	}
	// Set to 1.21.4 while its world was last opened in 26.3, a copy of it as
	// it is would not start either.
	if got, code := post("", `{"name":"same","fromServer":"world","node":"nowhere"}`); got != http.StatusConflict || code != "newer_world" {
		t.Errorf("copied as it is: got %d %q, want 409 newer_world", got, code)
	}

	// A world sent to it, said to be from a newer version: to start it with,
	// or in place of its own. Held by another import, one let through stops
	// there.
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindImport, ""); !ok {
		t.Fatal("could not claim the server")
	}
	for _, q := range []string{"world=true", "world=true&replace=true"} {
		if got, code := p.do("owner", "PUT", world+"/import?"+q+"&version=26.2", "x"); got != http.StatusConflict || code != "newer_world" {
			t.Errorf("%s from 26.2: got %d %q, want 409 newer_world", q, got, code)
		}
		if got, code := p.do("owner", "PUT", world+"/import?"+q+"&version=26.2&newerWorld=true", "x"); code != "importing" {
			t.Errorf("%s from 26.2, as meant: got %d %q, want it let through", q, got, code)
		}
		for _, older := range []string{"1.21.4", "1.21", "", "b1.7.3"} {
			if got, code := p.do("owner", "PUT", world+"/import?"+q+"&version="+older, "x"); code != "importing" {
				t.Errorf("%s from %q: got %d %q, want it let through", q, older, got, code)
			}
		}
	}
	// A whole server's archive, whose world is newer than it was set to.
	if got, code := p.do("owner", "PUT", world+"/import?version=26.2", "x"); got != http.StatusConflict || code != "newer_world" {
		t.Errorf("a server's archive from 26.2: got %d %q, want 409 newer_world", got, code)
	}
	p.srv.ops.end("mc-alpha", "world")
}

// Without the list of versions nothing can be told, and nothing is refused.
func TestNewerWorldUntold(t *testing.T) {
	p := newPanel(t)
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"version":"1.21"}}`); got != http.StatusOK {
		t.Errorf("with no list of versions: got %d %q", got, code)
	}
}

// A world from the library, a template's own world, and the world a
// template's or a snapshot's files have are held to the same rule.
func TestNewerWorldKeptByThePanel(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	call := func(who, method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	codeOf := func(out map[string]any) string { s, _ := out["code"].(string); return s }
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := os.WriteFile(filepath.Join(dir, "alpha.key"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const world = "/orgs/alpha/servers/world"

	// In the library, last opened in 26.2; "world" runs 1.21.4.
	code, lib := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Newer&root=Newer&version=26.2", "a world")
	if code != http.StatusCreated {
		t.Fatalf("uploaded: %d %v", code, lib)
	}
	libID := lib["id"].(string)
	// A template whose servers are given that world.
	_, tmpl := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Newer base","spec":{"loader":"vanilla","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`)
	ownID := tmpl["id"].(string)
	if code, out := call("admin", "PUT", "/orgs/alpha/templates/"+ownID+"/world?library="+libID, ""); code != http.StatusOK {
		t.Fatalf("given the world: %d %v", code, out)
	}

	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindImport, ""); !ok {
		t.Fatal("could not claim the server")
	}
	for _, q := range []string{"world=true&library=" + libID, "world=true&replace=true&library=" + libID, "world=true&template=" + ownID, "world=true&replace=true&template=" + ownID} {
		if code, out := call("owner", "PUT", world+"/import?"+q, ""); code != http.StatusConflict || codeOf(out) != "newer_world" || out["message"] != "The world was last opened in 26.2. 1.21.4 cannot open the world, so the server will not start." {
			t.Errorf("%s: got %d %v, want 409 newer_world", q, code, out)
		}
		if code, out := call("owner", "PUT", world+"/import?"+q+"&newerWorld=true", ""); codeOf(out) != "importing" {
			t.Errorf("%s, as meant: got %d %v, want it let through", q, code, out)
		}
	}
	p.srv.ops.end("mc-alpha", "world")

	// A snapshot of a server on 26.2, and a template made from it.
	root := t.TempDir()
	record := `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"vanilla","version":"26.2","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"expose":{"type":"Router"}}}`
	for path, content := range map[string]string{"data/world/level.dat": "a world", "data/.avsys/snapshot.json": record} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	take := exec.Command("restic", "--repo", filepath.Join(dir, "alpha"), "--password-file", filepath.Join(dir, "alpha.key"),
		"backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=manual", "--tag", "version=26.2", "--tag", "loader=vanilla")
	take.Dir = root
	if out, err := take.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v: %s", err, out)
	}
	list, err := p.srv.Backups.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var snap string
	for _, s := range list {
		if s.Server == "survival" {
			snap = s.ID
		}
	}
	code, made := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Survival","snapshot":"`+snap+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("template: %d %v", code, made)
	}
	filesID := made["id"].(string)

	// Made on an older version with the world they have. A node that is
	// not there stops one let through.
	for _, from := range []string{`"fromSnapshot":"` + snap + `"`, `"fromTemplate":"` + filesID + `"`} {
		body := `{"name":"older",` + from + `,"node":"nowhere","spec":{"version":"1.21.4"}}`
		if code, out := call("owner", "POST", "/orgs/alpha/servers", body); code != http.StatusConflict || codeOf(out) != "newer_world" {
			t.Errorf("%s on 1.21.4: got %d %v, want 409 newer_world", from, code, out)
		}
		if code, out := call("owner", "POST", "/orgs/alpha/servers?newerWorld=true", body); codeOf(out) == "newer_world" {
			t.Errorf("%s on 1.21.4, as meant: got %d %v", from, code, out)
		}
		// Not with a new world, nor on the version it was on.
		if code, out := call("owner", "POST", "/orgs/alpha/servers", `{"name":"older",`+from+`,"world":"new","node":"nowhere","spec":{"version":"1.21.4"}}`); codeOf(out) == "newer_world" {
			t.Errorf("%s with a new world: got %d %v", from, code, out)
		}
		if code, out := call("owner", "POST", "/orgs/alpha/servers", `{"name":"older",`+from+`,"node":"nowhere"}`); codeOf(out) == "newer_world" {
			t.Errorf("%s as it was: got %d %v", from, code, out)
		}
	}
}
