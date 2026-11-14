package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// The library: worlds uploaded or saved from a snapshot, named, given a
// picture, downloaded, and taken out again. One uploaded is downloaded as it
// was sent; one saved, as a zip of a single-player save, with the resource
// pack its server had.
func TestWorldLibrary(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	call := func(who, method, path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	object := func(raw []byte) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.srv.Config.PackDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.key"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Uploaded, by an operator and not a viewer.
	if code, _ := call("viewer", "POST", "/orgs/alpha/worlds/upload?name=Island&root=Island", "a world"); code != http.StatusForbidden {
		t.Errorf("a viewer uploaded one: %d", code)
	}
	code, raw := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Island&description=Small&root=saves%2FIsland&version=1.21.4&file=island.zip&unpacked=100", "a world")
	if code != http.StatusCreated {
		t.Fatalf("uploaded: %d %s", code, raw)
	}
	island := object(raw)
	id := island["id"].(string)
	if island["ready"] != true || island["root"] != "saves/Island" || island["version"] != "1.21.4" || island["file"] != "island.zip" || island["size"] != float64(len("a world")) || island["source"] != "upload" || island["unpacked"] != float64(100) {
		t.Errorf("uploaded as %v", island)
	}
	if code, _ := call("operator", "POST", "/orgs/alpha/worlds/upload?name=ISLAND&root=Island", "another"); code != http.StatusConflict {
		t.Errorf("a name taken: %d", code)
	}
	// Not among the organization's snapshots.
	if code, list := call("admin", "GET", "/orgs/alpha/snapshots", ""); code != http.StatusOK || len(object(list)["snapshots"].([]any)) != 0 {
		t.Errorf("listed with the snapshots: %d %s", code, list)
	}
	// Seen by every member; downloaded by operators, as it went in.
	if code, list := call("viewer", "GET", "/orgs/alpha/worlds", ""); code != http.StatusOK || !strings.Contains(string(list), `"Island"`) {
		t.Errorf("listed: %d %s", code, list)
	}
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/archive", ""); code != http.StatusForbidden {
		t.Errorf("a viewer downloaded it: %d", code)
	}
	if code, got := call("operator", "GET", "/orgs/alpha/worlds/"+id+"/archive", ""); code != http.StatusOK || string(got) != "a world" {
		t.Errorf("downloaded: %d %q", code, got)
	}
	// Renamed.
	if code, got := call("operator", "PATCH", "/orgs/alpha/worlds/"+id, `{"name":"Atoll"}`); code != http.StatusOK || object(got)["name"] != "Atoll" || object(got)["description"] != "Small" {
		t.Errorf("renamed: %d %s", code, got)
	}
	// A picture of its own.
	if code, _ := call("operator", "GET", "/orgs/alpha/worlds/"+id+"/icon", ""); code != http.StatusNotFound {
		t.Errorf("a picture before one was given: %d", code)
	}
	if code, got := call("operator", "PUT", "/orgs/alpha/worlds/"+id+"/icon", string(asPNG(t, picture(128, 128)))); code != http.StatusNoContent {
		t.Fatalf("given a picture: %d %s", code, got)
	}
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/icon", ""); code != http.StatusOK {
		t.Errorf("its picture: %d", code)
	}

	// Saved from a snapshot: its world's folders, and nothing else, and the
	// resource pack its server had uploaded, required as it was.
	root := t.TempDir()
	pack := packBytes(t, "the map's")
	sha := sha1Of(pack)
	record := `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"expose":{"type":"Router"},` +
		`"resourcePack":{"url":"https://panel.example/packs/` + sha + `.zip","sha1":"` + sha + `","enforce":true,"prompt":"Take it","uploaded":{"name":"map.zip","size":` + fmt.Sprint(len(pack)) + `,"time":"2026-01-01T00:00:00Z"}}}}`
	for path, content := range map[string]string{
		"data/server.properties":                    "motd=hi",
		"data/world/level.dat":                      "the world",
		"data/world_nether/DIM-1/r":                 "the nether",
		"data/world_nether/level.dat":               "Paper's copy",
		"data/server-icon.png":                      string(asPNG(t, picture(64, 64))),
		"data/.avsys/snapshot.json":                 record,
		"data/.avsys/resource-pack-" + sha + ".zip": string(pack),
		"data/plugins/LuckPerms.jar":                "a plugin",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	take := exec.Command("restic", "--repo", filepath.Join(dir, "alpha"), "--password-file", filepath.Join(dir, "alpha.key"),
		"backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=manual", "--tag", "version=1.21.4", "--tag", "loader=paper")
	take.Dir = root
	if out, err := take.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v: %s", err, out)
	}
	list, err := p.srv.Backups.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for _, s := range list {
		if s.Server == "survival" {
			src = s.ID
		}
	}
	if code, _ := call("operator", "POST", "/orgs/alpha/worlds", `{"name":"Both","server":"survival","snapshot":"`+src+`"}`); code != http.StatusBadRequest {
		t.Errorf("from a server and a snapshot: %d", code)
	}
	code, raw = call("operator", "POST", "/orgs/alpha/worlds?wait=true", `{"name":"Survival","snapshot":"`+src+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("saved from a snapshot: %d %s", code, raw)
	}
	saved := object(raw)
	savedID := saved["id"].(string)
	if saved["ready"] != true || saved["root"] != "world" || saved["version"] != "1.21.4" || saved["source"] != "snapshot:survival" || saved["unpacked"] != saved["size"] {
		t.Errorf("saved as %v", saved)
	}
	rp, _ := saved["resourcePack"].(map[string]any)
	if up, _ := rp["uploaded"].(map[string]any); rp["sha1"] != sha || rp["url"] != "" || rp["enforce"] != true || rp["prompt"] != "Take it" || up["name"] != "map.zip" || up["size"] != float64(len(pack)) {
		t.Errorf("saved with the pack %v", rp)
	}
	// A zip, named for the world, with the world at its top as a save in
	// the game has it: its Nether in it, and its pack as its own.
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/worlds/"+savedID+"/archive", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["operator"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "Survival.zip") {
		t.Fatalf("its archive: %d %v", rec.Code, rec.Header())
	}
	files := unzipped(t, rec.Body.Bytes())
	if len(files) != 3 || files["Survival/level.dat"] != "the world" || files["Survival/DIM-1/r"] != "the nether" || files["Survival/resources.zip"] != string(pack) {
		t.Errorf("its archive holds %v", keysOf(files))
	}
	// With the server's picture.
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+savedID+"/icon", ""); code != http.StatusOK {
		t.Errorf("its picture: %d", code)
	}

	// One whose world is not among its files: waited on, it is told and not
	// kept; not waited on, it says why it was not saved until deleted.
	bare := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bare, "data", ".avsys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "data", ".avsys", "snapshot.json"), []byte(strings.Replace(record, `"survival"`, `"empty"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	take = exec.Command("restic", "--repo", filepath.Join(dir, "alpha"), "--password-file", filepath.Join(dir, "alpha.key"),
		"backup", "data", "--host", "empty", "--tag", "avsys", "--tag", "trigger=manual")
	take.Dir = bare
	if out, err := take.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v: %s", err, out)
	}
	list, err = p.srv.Backups.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	var empty string
	for _, s := range list {
		if s.Server == "empty" {
			empty = s.ID
		}
	}
	if code, got := call("operator", "POST", "/orgs/alpha/worlds?wait=true", `{"name":"Nothing","snapshot":"`+empty+`"}`); code != http.StatusBadRequest || !strings.Contains(string(got), "not among the server's files") {
		t.Errorf("waited on with no world: %d %s", code, got)
	}
	if code, got := call("viewer", "GET", "/orgs/alpha/worlds", ""); code != http.StatusOK || strings.Contains(string(got), `"Nothing"`) {
		t.Errorf("one told it failed is kept: %s", got)
	}
	code, raw = call("operator", "POST", "/orgs/alpha/worlds", `{"name":"Nothing","snapshot":"`+empty+`"}`)
	if code != http.StatusAccepted || object(raw)["saving"] != true {
		t.Fatalf("not waited on: %d %s", code, raw)
	}
	failedID := object(raw)["id"].(string)
	var failed map[string]any
	for range 200 {
		_, got := call("viewer", "GET", "/orgs/alpha/worlds/"+failedID, "")
		if failed = object(got); failed["saving"] != true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if failed["ready"] != false || !strings.Contains(fmt.Sprint(failed["error"]), "not among the server's files") {
		t.Errorf("failed as %v", failed)
	}
	if code, _ := call("admin", "DELETE", "/orgs/alpha/worlds/"+failedID, ""); code != http.StatusNoContent {
		t.Errorf("one that failed deleted: %d", code)
	}

	// Given to a template as its own world: a copy, which stays when the
	// library's is deleted.
	_, made := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Island base","spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`)
	tid := object(made)["id"].(string)
	if code, got := call("admin", "PUT", "/orgs/alpha/templates/"+tid+"/world?library="+id, ""); code != http.StatusOK || object(got)["ownWorld"].(map[string]any)["root"] != "saves/Island" || object(got)["ownWorld"].(map[string]any)["name"] != "Atoll" {
		t.Fatalf("given to a template: %d %s", code, got)
	}
	if code, _ := call("admin", "PUT", "/orgs/alpha/templates/"+tid+"/world?library=nope", ""); code != http.StatusNotFound {
		t.Errorf("one not in the library: %d", code)
	}

	// Taken out by an admin, with its archive.
	if code, _ := call("operator", "DELETE", "/orgs/alpha/worlds/"+id, ""); code != http.StatusForbidden {
		t.Errorf("an operator deleted one: %d", code)
	}
	for _, w := range []string{id, savedID} {
		if code, _ := call("admin", "DELETE", "/orgs/alpha/worlds/"+w, ""); code != http.StatusNoContent {
			t.Errorf("deleted: %d", code)
		}
	}
	// Its pack goes with it.
	list, err = p.srv.Backups.List(t.Context(), "alpha")
	if err != nil || len(list) != 3 {
		t.Errorf("left in the repository: %v %v", list, err)
	}
	for _, s := range list {
		if s.Server != "survival" && s.Server != "empty" && s.Server != worldHost(tid) {
			t.Errorf("left in the repository: %+v", s)
		}
	}
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/icon", ""); code != http.StatusNotFound {
		t.Errorf("its picture stayed: %d", code)
	}
}

// packBytes is a resource pack the game would take, said to be described.
func packBytes(t *testing.T, described string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("pack.mcmeta")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(w, `{"pack":{"pack_format":46,"description":%q}}`, described)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha1Of(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// unzipped are the files in a zip, which must be one, by name.
func unzipped(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		out[f.Name] = string(b)
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
