package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A panel from before template snapshots were noted left its templates'
// and worlds' snapshots in the repository alone: they are read from it the
// first time they are asked for, and only then. Each start reads them again,
// putting right what was removed or made while the panel was not running.
func TestTemplateSnapshotsOfAnEarlierPanel(t *testing.T) {
	l := newLibraryPanel(t, nil)
	ctx := t.Context()
	org, err := l.p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	tpl := &store.Template{OrgID: org.ID, Name: "Old", CreatedBy: "admin"}
	if err := l.p.srv.Store.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		l.restic(l.files, "backup", "data", "--host", templateHost(tpl.ID), "--tag", "avsys", "--tag", "trigger=template", "--tag", "by=admin", "--tag", "version=1.21.4", "--tag", "loader=paper")
	}
	world := &store.World{OrgID: org.ID, Name: "Old world", Root: "Island", Source: "upload", File: "island.zip"}
	if err := l.p.srv.Store.CreateWorld(ctx, world); err != nil {
		t.Fatal(err)
	}
	archive := t.TempDir()
	if err := os.WriteFile(filepath.Join(archive, ownWorldFile), []byte("a world"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.restic(archive, "backup", ownWorldFile, "--host", libraryHost(world.ID), "--tag", "avsys", "--tag", "trigger=template", "--tag", "by=admin")
	if err := l.p.srv.Store.SetWorldArchive(ctx, org.ID, world.ID, world.Root, "", 7, 0); err != nil {
		t.Fatal(err)
	}

	l.same("as an earlier panel left them")
	if v := l.made("viewer", "GET", "/orgs/alpha/templates/"+tpl.ID, "", http.StatusOK); len(v["versions"].([]any)) != 2 {
		t.Errorf("an earlier panel's template: %v", v)
	}
	// Read once: not again.
	l.change()
	l.ran()
	l.made("viewer", "GET", "/orgs/alpha/worlds/"+world.ID, "", http.StatusOK)
	if code, _ := l.call("viewer", "GET", "/orgs/alpha/templates", ""); code != http.StatusOK || l.ran() > 0 {
		t.Errorf("read again: %d", code)
	}

	var logged bytes.Buffer
	l.p.srv.Log = slog.New(slog.NewTextHandler(&logged, nil))
	start := func(want string) {
		t.Helper()
		logged.Reset()
		l.p.srv.ReadTemplateSnapshots(ctx)
		if !strings.Contains(logged.String(), want) {
			t.Errorf("at start, logged %q, want %q", logged.String(), want)
		}
	}
	start("organizations=1 added=0 removed=0")

	// Removed while the panel was not running, and one noted that never
	// was: put right at the next start.
	list, err := l.p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	l.restic(l.dir, "forget", versionsOf(list, tpl.ID)[0].ID)
	if err := l.p.srv.Store.NoteTemplateSnapshot(ctx, org.ID, store.TemplateSnapshot{ID: strings.Repeat("0", 64), Host: templateHost(tpl.ID), Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	start("removed=2")
	l.same("after a start")
	if noted, err := l.p.srv.Store.TemplateSnapshots(ctx, org.ID); err != nil || len(noted) != 2 {
		t.Errorf("noted after a start: %v %v", noted, err)
	}

	// Noted wrongly, they are all noted again.
	if _, _, err := l.p.srv.Store.SetTemplateSnapshots(ctx, org.ID, nil); err != nil {
		t.Fatal(err)
	}
	start("added=2 removed=0")
	l.same("noted again")
	start("added=0 removed=0")
}

// Snapshots brought back from offsite are under IDs of their own: the
// templates and worlds they are of have them again.
func TestTemplateSnapshotsBroughtBack(t *testing.T) {
	l := newLibraryPanel(t, &backup.Offsite{Repository: filepath.Join(t.TempDir(), "far"), Password: "kept-by-the-operator"})
	l.fill()
	l.same("as made")
	l.made("admin", "POST", "/orgs/alpha/offsite/run?wait=true", "", http.StatusOK)
	// The panel's storage is lost.
	for _, path := range []string{filepath.Join(l.dir, "alpha"), filepath.Join(l.dir, "alpha.key"), filepath.Join(l.dir, "alpha.offsite")} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	if code, raw := l.call("viewer", "GET", "/orgs/alpha/worlds", ""); code != http.StatusOK || strings.Contains(string(raw), `"ready":true`) {
		t.Errorf("worlds with the storage lost: %d %s", code, raw)
	}
	l.same("with the storage lost")
	l.made("root", "POST", "/orgs/alpha/offsite/fetch", "", http.StatusOK)
	l.same("brought back")
	if v := l.made("viewer", "GET", "/orgs/alpha/templates/"+l.template, "", http.StatusOK); len(v["versions"].([]any)) != 2 || v["world"] != "own" {
		t.Errorf("a template brought back: %v", v)
	}
}
