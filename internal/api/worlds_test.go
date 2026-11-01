package api

import (
	"archive/tar"
	"bytes"
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
// picture, downloaded as they were kept, and taken out again.
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

	// Saved from a snapshot: its world's folders, and nothing else.
	root := t.TempDir()
	record := `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"expose":{"type":"Router"}}}`
	for path, content := range map[string]string{
		"data/server.properties":     "motd=hi",
		"data/world/level.dat":       "the world",
		"data/world_nether/DIM-1/r":  "the nether",
		"data/server-icon.png":       string(asPNG(t, picture(64, 64))),
		"data/.avsys/snapshot.json":  record,
		"data/plugins/LuckPerms.jar": "a plugin",
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
	code, got := call("operator", "GET", "/orgs/alpha/worlds/"+savedID+"/archive", "")
	if code != http.StatusOK {
		t.Fatalf("its archive: %d", code)
	}
	files := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(got))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		if h.Typeflag == tar.TypeReg {
			files[h.Name] = string(data)
		}
	}
	if len(files) != 2 || files["world/level.dat"] != "the world" || files["world_nether/DIM-1/r"] != "the nether" {
		t.Errorf("its archive holds %v", files)
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
