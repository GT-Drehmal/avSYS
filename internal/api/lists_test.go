package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// libraryRepo is an organization's repository with what templates and the
// library are made of in it, for a panel whose restic counts how often it is
// run.
type libraryRepo struct {
	t   *testing.T
	p   *panel
	dir string
	// runs is where each run of restic by the panel is noted.
	runs string
	// src is a snapshot of a server, with its record, of the files in
	// files/data.
	src, files string
	// template has two versions and a world of its own; world is in the
	// library.
	template, world string
}

func (l *libraryRepo) restic(dir string, args ...string) {
	l.t.Helper()
	cmd := exec.Command("restic", append([]string{"--repo", filepath.Join(l.dir, "alpha"), "--password-file", filepath.Join(l.dir, "alpha.key")}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		l.t.Fatalf("restic %v: %v: %s", args, err, out)
	}
}

// ran is how often the panel has run restic since it was last asked.
func (l *libraryRepo) ran() int {
	l.t.Helper()
	data, err := os.ReadFile(l.runs)
	if err != nil && !os.IsNotExist(err) {
		l.t.Fatal(err)
	}
	_ = os.Remove(l.runs)
	return strings.Count(string(data), "\n")
}

// change adds a snapshot to the repository, as anything taking one does:
// what restic last said of it is out of date.
func (l *libraryRepo) change() {
	l.t.Helper()
	dir := l.t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte(time.Now().String()), 0o644); err != nil {
		l.t.Fatal(err)
	}
	l.restic(dir, "backup", "f", "--host", "elsewhere", "--tag", "avsys", "--tag", "trigger=manual")
}

func (l *libraryRepo) call(who, method, path, body string) (int, []byte) {
	l.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+l.p.tokens[who])
	rec := httptest.NewRecorder()
	l.p.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (l *libraryRepo) made(who, method, path, body string, want int) map[string]any {
	l.t.Helper()
	code, raw := l.call(who, method, path, body)
	if code != want {
		l.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// newLibraryRepo is a panel whose organization has a template with two
// versions and a world of its own, one of settings alone, and two worlds in
// its library.
func newLibraryRepo(t *testing.T) *libraryRepo {
	l := newLibraryPanel(t, nil)
	l.fill()
	return l
}

// newLibraryPanel is a panel whose organization's repository has a snapshot
// of a server in it, and nothing more, copied offsite as given.
func newLibraryPanel(t *testing.T, offsite *backup.Offsite) *libraryRepo {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	l := &libraryRepo{t: t, p: newPanel(t), dir: t.TempDir()}
	l.runs = filepath.Join(t.TempDir(), "runs")
	// restic as the panel runs it, noting each run.
	stand := filepath.Join(t.TempDir(), "restic")
	real, _ := exec.LookPath("restic")
	if err := os.WriteFile(stand, []byte("#!/bin/sh\necho \"$*\" >> "+l.runs+"\nexec "+real+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l.p.srv.Backups = &backup.Manager{Dir: l.dir, Restic: stand, Offsite: offsite, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := os.WriteFile(filepath.Join(l.dir, "alpha.key"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	l.files = root
	record := `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"paper","version":"1.21.4",` +
		`"resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"expose":{"type":"Router","hostname":"survival.alpha.test"}}}`
	for path, content := range map[string]string{"data/world/level.dat": "a world", "data/.avsys/snapshot.json": record} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l.restic(root, "init")
	l.restic(root, "backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=manual", "--tag", "version=1.21.4", "--tag", "loader=paper", "--tag", "by=owner")
	// Many more, as a real organization has, where asked for.
	more, _ := strconv.Atoi(os.Getenv("AVSYS_TEST_SNAPSHOTS"))
	for range more {
		l.change()
	}
	list, err := l.p.srv.Backups.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Server == "survival" {
			l.src = s.ID
		}
	}
	return l
}

func (l *libraryRepo) fill() {
	tpl := l.made("admin", "POST", "/orgs/alpha/templates", `{"name":"Survival","snapshot":"`+l.src+`"}`, http.StatusCreated)
	l.template = tpl["id"].(string)
	l.made("admin", "POST", "/orgs/alpha/templates/"+l.template+"/versions", `{"snapshot":"`+l.src+`"}`, http.StatusCreated)
	l.made("admin", "PUT", "/orgs/alpha/templates/"+l.template+"/world?root=Island&name=Island", "a world", http.StatusOK)
	l.made("admin", "POST", "/orgs/alpha/templates", `{"name":"Alone","spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`, http.StatusCreated)
	world := l.made("operator", "POST", "/orgs/alpha/worlds/upload?name=Island&root=saves%2FIsland&version=1.21.4&file=island.zip", string(islandZip(l.t)), http.StatusCreated)
	l.world = world["id"].(string)
	l.made("operator", "POST", "/orgs/alpha/worlds?wait=true", `{"name":"Survival","snapshot":"`+l.src+`"}`, http.StatusCreated)
}

// same checks that the templates and worlds are listed as they are when
// worked out from the repository's list of snapshots.
func (l *libraryRepo) same(when string) {
	l.t.Helper()
	ctx := l.t.Context()
	org, err := l.p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		l.t.Fatal(err)
	}
	list, err := l.p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		l.t.Fatal(err)
	}
	templates, err := l.p.srv.Store.Templates(ctx, org.ID)
	if err != nil {
		l.t.Fatal(err)
	}
	var wantTemplates []templateView
	for i := range templates {
		wantTemplates = append(wantTemplates, templateFor(store.RoleAdmin, templateOf(&templates[i], list)))
	}
	worlds, err := l.p.srv.Store.Worlds(ctx, org.ID)
	if err != nil {
		l.t.Fatal(err)
	}
	var wantWorlds []worldView
	for i := range worlds {
		wantWorlds = append(wantWorlds, worldOf(&worlds[i], librarySnapshots(list), false))
	}
	for path, want := range map[string]any{"/orgs/alpha/templates": wantTemplates, "/orgs/alpha/worlds": wantWorlds} {
		code, raw := l.call("admin", "GET", path, "")
		if code != http.StatusOK {
			l.t.Fatalf("%s: %s: %d %s", when, path, code, raw)
		}
		wanted, _ := json.Marshal(want)
		if wanted == nil || string(wanted) == "null" {
			wanted = []byte("[]")
		}
		var got, expected any
		_ = json.Unmarshal(raw, &got)
		_ = json.Unmarshal(wanted, &expected)
		if !reflect.DeepEqual(got, expected) {
			l.t.Errorf("%s: %s lists\n%s\nnot\n%s", when, path, raw, wanted)
		}
	}
}

// Templates and worlds are listed without asking restic for the snapshots,
// which takes a good part of a second each time the repository has changed,
// and are listed just as when they were worked out from them.
func TestTemplatesAndWorldsAreListedWithoutRestic(t *testing.T) {
	l := newLibraryRepo(t)
	l.same("as made")
	for _, path := range []string{"/orgs/alpha/templates", "/orgs/alpha/worlds", "/orgs/alpha/worlds/" + l.world} {
		l.change()
		l.ran()
		began := time.Now()
		code, raw := l.call("viewer", "GET", path, "")
		took := time.Since(began)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
		if n := l.ran(); n > 0 {
			t.Errorf("GET %s ran restic %d times", path, n)
		}
		// How long it took with the repository just changed, for the record.
		t.Logf("GET %s, the repository just changed: %v", path, took.Round(100*time.Microsecond))
	}
	// A template asked for alone, once what its version is was read.
	l.call("viewer", "GET", "/orgs/alpha/templates/"+l.template, "")
	l.change()
	l.ran()
	if code, raw := l.call("viewer", "GET", "/orgs/alpha/templates/"+l.template, ""); code != http.StatusOK || !strings.Contains(string(raw), `"versions"`) {
		t.Fatalf("one template: %d %s", code, raw)
	}
	// Its version's record, once read, is not read again.
	if n := l.ran(); n > 0 {
		t.Errorf("GET one template ran restic %d times", n)
	}
}

// What templates and worlds are listed with stays as the repository has it
// as their snapshots are removed, and as a template's are by retention,
// which never takes them.
func TestTemplateAndWorldListsAfterRemoving(t *testing.T) {
	l := newLibraryRepo(t)
	ctx := t.Context()
	// Retention is of scheduled snapshots alone.
	if n, err := l.p.srv.Backups.Forget(ctx, "alpha", templateHost(l.template), backup.Keep{Daily: 1}); err != nil || n != 0 {
		t.Errorf("retention took %d of a template's: %v", n, err)
	}
	l.same("after retention")

	tpl := l.made("admin", "GET", "/orgs/alpha/templates/"+l.template, "", http.StatusOK)
	versions := tpl["versions"].([]any)
	oldest := versions[len(versions)-1].(map[string]any)["id"].(string)
	l.made("admin", "DELETE", "/orgs/alpha/templates/"+l.template+"/versions/"+oldest, "", http.StatusOK)
	l.same("a version deleted")
	l.made("admin", "DELETE", "/orgs/alpha/templates/"+l.template+"/world", "", http.StatusOK)
	l.same("a template's world taken away")
	l.made("admin", "PUT", "/orgs/alpha/templates/"+l.template+"/world?root=Island", "another world", http.StatusOK)
	l.made("admin", "PUT", "/orgs/alpha/templates/"+l.template+"/world?root=Island", "a third world", http.StatusOK)
	l.same("a template's world given another")
	if code, _ := l.call("admin", "DELETE", "/orgs/alpha/worlds/"+l.world, ""); code != http.StatusNoContent {
		t.Fatalf("world deleted: %d", code)
	}
	l.same("a world deleted")
	if code, _ := l.call("admin", "DELETE", "/orgs/alpha/templates/"+l.template, ""); code != http.StatusNoContent {
		t.Fatalf("template deleted: %d", code)
	}
	l.same("a template deleted")

	// One removed from the repository other than by the panel is not
	// listed, and the panel needs no restic to tell.
	list, err := l.p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var gone string
	for _, s := range list {
		if strings.HasPrefix(s.Server, "world-") {
			gone = s.ID
		}
	}
	if gone == "" {
		t.Fatal("no world's snapshot is left")
	}
	l.restic(l.dir, "forget", gone)
	l.ran()
	l.same("one removed by hand")
	if code, raw := l.call("viewer", "GET", "/orgs/alpha/worlds", ""); code != http.StatusOK || strings.Contains(string(raw), `"ready":true`) {
		t.Errorf("a world whose snapshot was removed by hand: %d %s", code, raw)
	}
}
