package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// libraryPanel is a panel with a repository for alpha, and a way to call it.
func libraryPanel(t *testing.T) (*panel, string, func(who, method, path, body string) (int, []byte)) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.srv.Config.PackDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.key"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(who, method, path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	return p, dir, call
}

func objectOf(raw []byte) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// randomBytes is n bytes no compression makes smaller, as a world's region
// files mostly are.
func randomBytes(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeFiles writes files under root, by their paths in it.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// resticIn runs restic on alpha's repository, from dir, and gives what it
// printed.
func resticIn(t *testing.T, repo, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("restic", append([]string{"--repo", filepath.Join(repo, "alpha"), "--password-file", filepath.Join(repo, "alpha.key"), "--no-cache"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("restic %v: %v: %s", args, err, stderr)
	}
	return out
}

// libraryOf is the snapshots of the library kept under a world's host, and
// the one it is checked under before it takes its place.
func libraryOf(t *testing.T, p *panel, id string) (kept, unpacking []backup.Snapshot) {
	t.Helper()
	list, err := p.srv.Backups.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		switch s.Server {
		case libraryHost(id):
			kept = append(kept, s)
		case unpackingHost(id):
			unpacking = append(unpacking, s)
		}
	}
	return kept, unpacking
}

// A world uploaded to the library is kept as its own files; put in a server
// in place of its world, as Replace world and Put back do, it is the same
// files; and the server's next snapshot adds next to nothing of the world
// to the repository: all of it is the library's already.
func TestALibraryWorldSharesWithItsServers(t *testing.T) {
	p, repo, call := libraryPanel(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	land, fire := randomBytes(t, 3<<20), randomBytes(t, 1<<20)
	world := worldZip(t, map[string]string{
		"Big/level.dat":                   "a big world",
		"Big/region/r.0.0.mca":            land,
		"Big_nether/DIM-1/region/r.0.mca": fire,
	})
	code, raw := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Big&root=Big&version=1.21.4&file=big.zip", string(world))
	if code != http.StatusCreated {
		t.Fatalf("uploaded: %d %s", code, raw)
	}
	id := objectOf(raw)["id"].(string)
	kept, _ := libraryOf(t, p, id)
	if len(kept) != 1 || kept[0].Form != backup.FormFolders {
		t.Fatalf("kept as %+v", kept)
	}
	size := int64(len("a big world") + len(land) + len(fire))
	if kept[0].Size != size || kept[0].Added < size/2 {
		t.Errorf("kept %d bytes, adding %d, of a world of %d", kept[0].Size, kept[0].Added, size)
	}

	// A Paper server with a world of its own, which is replaced: its files
	// as its workspace has them once the library's world is put back where
	// an archive would be unpacked, as RestoreFolders puts it.
	server := t.TempDir()
	data := filepath.Join(server, "data")
	writeFiles(t, data, map[string]string{
		"server.properties":      "motd=hi\n",
		"plugins/a.jar":          "a plugin",
		"world/level.dat":        "the old world",
		"world/region/r.0.0.mca": "old land",
		"world_nether/DIM-1/r.0": "old fire",
		"world_the_end/DIM1/r.0": "old end",
	})
	staging := filepath.Join(data, ".avsys", "import")
	resticIn(t, repo, server, "restore", kept[0].ID+":/data", "--target", staging)
	script := regexp.MustCompile(regexp.QuoteMeta(dataRoot)+`\b`).ReplaceAllString(worldScript, data)
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader(keptWorld + "\nsplit\n\nreplace\nworld\n\nrestored\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	got := filesUnder(t, data)
	want := map[string]string{
		"server.properties":                 "motd=hi\n",
		"plugins/a.jar":                     "a plugin",
		"world/level.dat":                   "a big world",
		"world/region/r.0.0.mca":            land,
		"world_nether/DIM-1/region/r.0.mca": fire,
	}
	if len(got) != len(want) {
		t.Errorf("the server has %v", keysOf(got))
	}
	for name, content := range want {
		if got[name] != content {
			t.Errorf("%s: %d bytes, want %d", name, len(got[name]), len(content))
		}
	}

	// Its snapshot, as the server's workspace takes it.
	resticIn(t, repo, server, "backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=pre-replace")
	list, err := p.srv.Backups.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Server == "survival" {
			t.Logf("a server's snapshot of %d bytes, the library's world %d of them, added %d", s.Size, size, s.Added)
			if s.Added > size/20 {
				t.Errorf("the server's snapshot added %d bytes of a world of %d it shares with the library", s.Added, size)
			}
		}
	}

	// A vanilla server new from it: the Nether in the world, as the game
	// itself keeps it.
	fresh := t.TempDir()
	data = filepath.Join(fresh, "data")
	staging = filepath.Join(data, ".avsys", "import")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	resticIn(t, repo, fresh, "restore", kept[0].ID+":/data", "--target", staging)
	script = regexp.MustCompile(regexp.QuoteMeta(dataRoot)+`\b`).ReplaceAllString(worldScript, data)
	cmd = exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader(keptWorld + "\n\n\n\n\n\nrestored\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	got = filesUnder(t, data)
	if len(got) != 3 || got["world/level.dat"] != "a big world" || got["world/DIM-1/region/r.0.mca"] != fire {
		t.Errorf("the new server has %v", keysOf(got))
	}

	// Saved back to the library from that server's snapshot: copied, with
	// nothing added at all.
	var snap string
	for _, s := range list {
		if s.Server == "survival" {
			snap = s.ID
		}
	}
	record := `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"expose":{"type":"Router"}}}`
	writeFiles(t, server, map[string]string{"data/.avsys/snapshot.json": record})
	resticIn(t, repo, server, "backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=manual")
	list, _ = p.srv.Backups.List(t.Context(), "alpha")
	for _, s := range list {
		if s.Server == "survival" && s.Trigger == "manual" {
			snap = s.ID
		}
	}
	code, raw = call("operator", "POST", "/orgs/alpha/worlds?wait=true", `{"name":"Big again","snapshot":"`+snap+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("saved: %d %s", code, raw)
	}
	again := objectOf(raw)
	if again["kept"] != "folders" || again["root"] != "world" || again["size"] != float64(size) {
		t.Errorf("saved as %v", again)
	}
}

// oldWorld puts a world in the library as the library kept them before:
// its archive, as a file of a snapshot of its own.
func oldWorld(t *testing.T, p *panel, w *store.World, archive []byte) *backup.Snapshot {
	t.Helper()
	ctx := t.Context()
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	w.OrgID = org.ID
	if err := p.srv.Store.CreateWorld(ctx, w); err != nil {
		t.Fatal(err)
	}
	kept, err := p.srv.Backups.KeepFile(ctx, "alpha", libraryHost(w.ID), ownWorldFile, w.CreatedBy, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.srv.Store.SetWorldArchive(ctx, org.ID, w.ID, w.Root, w.Version, kept.Size, w.Unpacked); err != nil {
		t.Fatal(err)
	}
	return kept
}

// tarOf is a tar of files, by their paths in it.
func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The worlds the library kept as archives are kept as their own files as
// the panel starts, under the same IDs, and their archives removed: each
// checked first, and one that cannot be is kept as it was, used as it was,
// and says why. What one cut short left is cleared up, wherever it was cut.
func TestLibraryUnpackedAsThePanelStarts(t *testing.T) {
	p, _, call := libraryPanel(t)
	ctx := context.Background()
	zipped := islandZip(t)

	// Uploaded, kept as it was sent; with a picture.
	upload := &store.World{Name: "Island", CreatedBy: "alex", Root: "saves/Island", Version: "1.21.4", File: "island.zip", Source: "upload", Unpacked: 100}
	uploadArchive := oldWorld(t, p, upload, zipped)
	if code, _ := call("operator", "PUT", "/orgs/alpha/worlds/"+upload.ID+"/icon", string(asPNG(t, picture(64, 64)))); code != http.StatusNoContent {
		t.Fatalf("its picture: %d", code)
	}
	// Saved from a server: a tar of its folders, the Nether beside it.
	saved := &store.World{Name: "Survival", CreatedBy: "alex", Root: "world", Version: "1.21.4", Source: "server:survival"}
	oldWorld(t, p, saved, tarOf(t, map[string]string{"world/level.dat": "the world", "world/region/r.0.0.mca": "land", "world_nether/DIM-1/r.0.0.mca": "fire"}))
	// One whose archive is not one, which stays as it is.
	broken := &store.World{Name: "Broken", CreatedBy: "alex", Root: "Broken", File: "broken.zip", Source: "upload"}
	oldWorld(t, p, broken, []byte("not an archive"))
	// One cut short before its unpacked files took the archive's place:
	// those are left under a host of their own, and so is the folder it was
	// unpacked in.
	halfway := &store.World{Name: "Halfway", CreatedBy: "alex", Root: "saves/Island", File: "halfway.zip", Source: "upload"}
	oldWorld(t, p, halfway, zipped)
	left := t.TempDir()
	writeFiles(t, left, map[string]string{"data/world/level.dat": "half of it"})
	if _, err := p.srv.Backups.KeepFolders(ctx, "alpha", left, []string{"data/world"}, unpackingHost(halfway.ID), "alex"); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(p.srv.Backups.WorkDir("alpha"), "0123456789abcdef")
	writeFiles(t, stale, map[string]string{"data/world/level.dat": "half of it"})
	long := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, long, long); err != nil {
		t.Fatal(err)
	}
	// One cut short once they had, before its archive went.
	copied := &store.World{Name: "Copied", CreatedBy: "alex", Root: "saves/Island", File: "copied.zip", Source: "upload"}
	copiedArchive := oldWorld(t, p, copied, zipped)
	unpackedDir := t.TempDir()
	writeFiles(t, unpackedDir, map[string]string{"data/world/level.dat": "the island", "data/world/region/r.0.0.mca": "sand"})
	if _, err := p.srv.Backups.KeepFolders(ctx, "alpha", unpackedDir, []string{"data/world"}, libraryHost(copied.ID), "alex"); err != nil {
		t.Fatal(err)
	}
	p.srv.ReadTemplateSnapshots(ctx)

	// Before: kept as archives, and used as they are.
	_, raw := call("viewer", "GET", "/orgs/alpha/worlds/"+upload.ID, "")
	if got := objectOf(raw); got["kept"] != "archive" || got["ready"] != true || got["root"] != "saves/Island" {
		t.Errorf("before: %v", got)
	}
	if code, got := call("operator", "GET", "/orgs/alpha/worlds/"+upload.ID+"/archive", ""); code != http.StatusOK || !bytes.Equal(got, zipped) {
		t.Errorf("before, downloaded: %d, %d bytes", code, len(got))
	}

	p.srv.UnpackLibrary(ctx)

	unpacked := float64(len("the island") + len("sand"))
	for _, w := range []struct {
		world *store.World
		size  float64
	}{{upload, unpacked}, {saved, float64(len("the world") + len("land") + len("fire"))}, {halfway, unpacked}, {copied, unpacked}} {
		_, raw := call("viewer", "GET", "/orgs/alpha/worlds/"+w.world.ID, "")
		got := objectOf(raw)
		if got["id"] != w.world.ID || got["kept"] != "folders" || got["ready"] != true || got["root"] != "world" || got["size"] != w.size || got["unpacked"] != w.size || got["unpackError"] != nil || got["name"] != w.world.Name {
			t.Errorf("%s after: %v", w.world.Name, got)
		}
		kept, unpacking := libraryOf(t, p, w.world.ID)
		if len(kept) != 1 || kept[0].Form != backup.FormFolders || kept[0].By != "alex" || len(unpacking) != 0 {
			t.Errorf("%s kept as %+v, and %+v", w.world.Name, kept, unpacking)
		}
	}
	// Its archive gone; the one whose unpacked files were in place already
	// keeps those.
	if _, err := p.srv.Backups.Get(ctx, "alpha", uploadArchive.ID); err == nil {
		t.Error("an archive unpacked is still there")
	}
	if _, err := p.srv.Backups.Get(ctx, "alpha", copiedArchive.ID); err == nil {
		t.Error("the archive of one cut short once it was unpacked is still there")
	}
	if level, _ := p.srv.Backups.File(ctx, "alpha", mustKept(t, p, copied.ID).ID, "/data/world/level.dat", 1<<10); string(level) != "the island" {
		t.Errorf("one cut short once it was unpacked has %q", level)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a folder left unpacked stayed: %v", err)
	}
	if left, _ := os.ReadDir(p.srv.Backups.WorkDir("alpha")); len(left) != 0 {
		t.Errorf("left unpacked: %v", left)
	}
	// Downloaded as a save, its Nether in it, with its picture still.
	if code, got := call("operator", "GET", "/orgs/alpha/worlds/"+upload.ID+"/archive", ""); code != http.StatusOK || unzipped(t, got)["Island/level.dat"] != "the island" {
		t.Errorf("downloaded: %d %v", code, keysOf(unzipped(t, got)))
	}
	if code, got := call("operator", "GET", "/orgs/alpha/worlds/"+saved.ID+"/archive", ""); code != http.StatusOK || unzipped(t, got)["Survival/DIM-1/r.0.0.mca"] != "fire" {
		t.Errorf("downloaded: %d %v", code, keysOf(unzipped(t, got)))
	}
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+upload.ID+"/icon", ""); code != http.StatusOK {
		t.Errorf("its picture: %d", code)
	}
	// Given to a template, as it is kept now.
	_, made := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Island base","spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`)
	tid := objectOf(made)["id"].(string)
	if code, got := call("admin", "PUT", "/orgs/alpha/templates/"+tid+"/world?library="+upload.ID, ""); code != http.StatusOK || objectOf(got)["ownWorld"].(map[string]any)["root"] != "world" {
		t.Errorf("given to a template: %d %s", code, got)
	}

	// The one that could not be: as it was, and saying why.
	_, raw = call("viewer", "GET", "/orgs/alpha/worlds/"+broken.ID, "")
	if got := objectOf(raw); got["kept"] != "archive" || got["ready"] != true || !strings.Contains(got["unpackError"].(string), "Use a .zip or .tar.gz file") || got["root"] != "Broken" {
		t.Errorf("one that could not be unpacked: %v", got)
	}
	if code, got := call("operator", "GET", "/orgs/alpha/worlds/"+broken.ID+"/archive", ""); code != http.StatusOK || string(got) != "not an archive" {
		t.Errorf("one that could not be unpacked, downloaded: %d %q", code, got)
	}
	if kept, unpacking := libraryOf(t, p, broken.ID); len(kept) != 1 || kept[0].Form != "" || len(unpacking) != 0 {
		t.Errorf("one that could not be unpacked is kept as %+v, %+v", kept, unpacking)
	}

	// Again, as the panel starts again: nothing changes.
	before, _ := p.srv.Backups.List(ctx, "alpha")
	p.srv.UnpackLibrary(ctx)
	after, _ := p.srv.Backups.List(ctx, "alpha")
	if len(before) != len(after) {
		t.Errorf("unpacked again: %d snapshots, then %d", len(before), len(after))
	}
	for i := range before {
		if i < len(after) && before[i].ID != after[i].ID {
			t.Errorf("unpacked again: %s, then %s", before[i].ID, after[i].ID)
		}
	}
	// Deleted, it goes with whatever of it is kept.
	if code, _ := call("admin", "DELETE", "/orgs/alpha/worlds/"+broken.ID, ""); code != http.StatusNoContent {
		t.Errorf("deleted: %d", code)
	}
	if kept, _ := libraryOf(t, p, broken.ID); len(kept) != 0 {
		t.Errorf("left of one deleted: %+v", kept)
	}
}

func mustKept(t *testing.T, p *panel, id string) backup.Snapshot {
	t.Helper()
	kept, _ := libraryOf(t, p, id)
	if len(kept) == 0 {
		t.Fatalf("nothing kept of %s", id)
	}
	return kept[0]
}
