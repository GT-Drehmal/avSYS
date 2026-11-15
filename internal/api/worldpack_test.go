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
	"strings"
	"testing"

	"github.com/pkg/sftp"

	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// A world in the library names the resource pack it is played with: one
// uploaded to it, kept beside it, or one from an address. A server given the
// world is given the pack, and so is a template given it, and the servers
// made from that.
func TestWorldPack(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
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
	packOf := func(v map[string]any) map[string]any {
		rp, _ := v["resourcePack"].(map[string]any)
		return rp
	}
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.srv.Config.PackDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.key"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := func(host string) int {
		t.Helper()
		list, err := p.srv.Backups.List(t.Context(), "alpha")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range list {
			if s.Server == host {
				n++
			}
		}
		return n
	}

	code, made := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Island&root=Island&version=1.21.4&file=island.zip", "a world")
	if code != http.StatusCreated || made["resourcePack"] != nil {
		t.Fatalf("uploaded: %d %v", code, made)
	}
	id := made["id"].(string)

	// Uploaded to it by an operator, checked as a server's is.
	pack, other := packBytes(t, "one"), packBytes(t, "two")
	if code, _ := call("viewer", "PUT", "/orgs/alpha/worlds/"+id+"/resource-pack?name=map.zip", string(pack)); code != http.StatusForbidden {
		t.Errorf("a viewer gave it a pack: %d", code)
	}
	if code, got := call("operator", "PUT", "/orgs/alpha/worlds/"+id+"/resource-pack?name=map.zip", "not a zip"); code != http.StatusBadRequest || !strings.Contains(got["message"].(string), "not a zip") {
		t.Errorf("not a pack: %d %v", code, got)
	}
	code, got := call("operator", "PUT", "/orgs/alpha/worlds/"+id+"/resource-pack?name=map.zip", string(pack))
	if code != http.StatusOK {
		t.Fatalf("given a pack: %d %v", code, got)
	}
	rp := packOf(got)
	if up, _ := rp["uploaded"].(map[string]any); rp["sha1"] != sha1Of(pack) || up["name"] != "map.zip" || up["size"] != float64(len(pack)) || rp["formats"] == nil {
		t.Errorf("its pack: %v", rp)
	}
	if code, got := call("viewer", "GET", "/orgs/alpha/worlds/"+id, ""); code != http.StatusOK || packOf(got)["sha1"] != sha1Of(pack) {
		t.Errorf("seen: %d %v", code, got)
	}
	// Required, and what players are asked, without another upload.
	if code, got := call("operator", "PATCH", "/orgs/alpha/worlds/"+id, `{"resourcePack":{"enforce":true,"prompt":"For the map"}}`); code != http.StatusOK || packOf(got)["enforce"] != true || packOf(got)["prompt"] != "For the map" || packOf(got)["sha1"] != sha1Of(pack) {
		t.Errorf("made required: %d %v", code, got)
	}
	// Another in its place keeps those, and only it is kept.
	if code, got := call("operator", "PUT", "/orgs/alpha/worlds/"+id+"/resource-pack?name=other.zip", string(other)); code != http.StatusOK || packOf(got)["sha1"] != sha1Of(other) || packOf(got)["enforce"] != true {
		t.Errorf("another: %d %v", code, got)
	}
	if n := kept(packHost(libraryHost(id))); n != 1 {
		t.Errorf("%d packs kept", n)
	}
	call("operator", "PUT", "/orgs/alpha/worlds/"+id+"/resource-pack?name=map.zip", string(pack))

	// Given to a server as the world is, with what is in it.
	sc := filesFrom(t, p, sftp.InMemHandler())
	code, server := call("admin", "PUT", "/orgs/alpha/servers/world/resource-pack?world="+id, "")
	if code != http.StatusOK {
		t.Fatalf("given to a server: %d %v", code, server)
	}
	spec := server["spec"].(map[string]any)
	srp := spec["resourcePack"].(map[string]any)
	if up, _ := srp["uploaded"].(map[string]any); srp["sha1"] != sha1Of(pack) || up["name"] != "map.zip" || !strings.HasSuffix(srp["url"].(string), "/packs/"+sha1Of(pack)+".zip") {
		t.Errorf("the server's pack: %v", srp)
	}
	if got := read(t, sc, packFile(sha1Of(pack))); got != string(pack) {
		t.Errorf("in the server's files: %d bytes", len(got))
	}
	if code, _ := call("admin", "PUT", "/orgs/alpha/servers/world/resource-pack?world=nope", ""); code != http.StatusNotFound {
		t.Errorf("a world not in the library: %d", code)
	}
	if code, _ := call("admin", "PUT", "/orgs/alpha/servers/world/resource-pack?world="+id+"&template=x", ""); code != http.StatusBadRequest {
		t.Errorf("from a world and a template: %d", code)
	}

	// Given to a template with its world, kept with it apart from the
	// library's, and from there to a server.
	_, tmpl := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Island base","spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`)
	tid := tmpl["id"].(string)
	if code, got := call("admin", "PUT", "/orgs/alpha/templates/"+tid+"/world?library="+id, ""); code != http.StatusOK || packOf(got["ownWorld"].(map[string]any)) != nil {
		t.Errorf("given without its pack: %d %v", code, got)
	}
	code, got = call("admin", "PUT", "/orgs/alpha/templates/"+tid+"/world?library="+id+"&pack=true", "")
	if trp := packOf(got["ownWorld"].(map[string]any)); code != http.StatusOK || trp["sha1"] != sha1Of(pack) || trp["enforce"] != true {
		t.Fatalf("given with its pack: %d %v", code, got)
	}
	if n := kept(packHost(worldHost(tid))); n != 1 {
		t.Errorf("%d packs kept with the template", n)
	}
	call("admin", "DELETE", "/orgs/alpha/servers/world/resource-pack", "")
	if code, server := call("admin", "PUT", "/orgs/alpha/servers/world/resource-pack?template="+tid, ""); code != http.StatusOK || server["spec"].(map[string]any)["resourcePack"].(map[string]any)["sha1"] != sha1Of(pack) {
		t.Errorf("given from the template: %d %v", code, server)
	}
	if code, got := call("admin", "PATCH", "/orgs/alpha/templates/"+tid+"/world", `{"resourcePack":{"enforce":false}}`); code != http.StatusOK || packOf(got["ownWorld"].(map[string]any))["enforce"] == true {
		t.Errorf("made not required: %d %v", code, got)
	}
	if code, _ := call("admin", "PATCH", "/orgs/alpha/templates/"+tid+"/world", `{"resourcePack":{"url":"https://example.com/p.zip"}}`); code != http.StatusBadRequest {
		t.Errorf("one from an address kept with its world: %d", code)
	}
	if code, got := call("admin", "PATCH", "/orgs/alpha/templates/"+tid+"/world", `{"resourcePack":null}`); code != http.StatusOK || packOf(got["ownWorld"].(map[string]any)) != nil {
		t.Errorf("taken off the template: %d %v", code, got)
	}
	if n := kept(packHost(worldHost(tid))); n != 0 {
		t.Errorf("%d packs left with the template", n)
	}
	call("admin", "PUT", "/orgs/alpha/templates/"+tid+"/world?library="+id+"&pack=true", "")
	if code, _ := call("admin", "DELETE", "/orgs/alpha/templates/"+tid, ""); code != http.StatusNoContent {
		t.Errorf("template deleted: %d", code)
	}
	if n := kept(packHost(worldHost(tid))); n != 0 {
		t.Errorf("%d packs left of a deleted template", n)
	}

	// One from an address in its place: the one uploaded goes, and a server
	// cannot be given that one from where it was kept.
	code, got = call("operator", "PATCH", "/orgs/alpha/worlds/"+id, `{"resourcePack":{"url":"https://example.com/map.zip","sha1":"`+strings.Repeat("ab", 20)+`"}}`)
	if rp := packOf(got); code != http.StatusOK || rp["url"] != "https://example.com/map.zip" || rp["uploaded"] != nil || rp["enforce"] != true {
		t.Errorf("one from an address: %d %v", code, got)
	}
	if n := kept(packHost(libraryHost(id))); n != 0 {
		t.Errorf("%d uploaded packs left", n)
	}
	if code, _ := call("admin", "PUT", "/orgs/alpha/servers/world/resource-pack?world="+id, ""); code != http.StatusBadRequest {
		t.Errorf("given one from an address as uploaded: %d", code)
	}
	for _, bad := range []string{`{"resourcePack":{"url":"ftp://example.com/p.zip"}}`, `{"resourcePack":{"url":"https://example.com/p.zip","sha1":"nope"}}`, `{"resourcePack":[]}`} {
		if code, _ := call("operator", "PATCH", "/orgs/alpha/worlds/"+id, bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// And none.
	if code, got := call("operator", "PATCH", "/orgs/alpha/worlds/"+id, `{"resourcePack":null}`); code != http.StatusOK || got["resourcePack"] != nil {
		t.Errorf("taken off: %d %v", code, got)
	}
	if code, _ := call("operator", "PATCH", "/orgs/alpha/worlds/"+id, `{"resourcePack":{"enforce":true}}`); code != http.StatusBadRequest {
		t.Errorf("required with none: %d", code)
	}
	// A rename leaves it as it is.
	call("operator", "PUT", "/orgs/alpha/worlds/"+id+"/resource-pack?name=map.zip", string(pack))
	if code, got := call("operator", "PATCH", "/orgs/alpha/worlds/"+id, `{"name":"Atoll"}`); code != http.StatusOK || packOf(got)["sha1"] != sha1Of(pack) {
		t.Errorf("renamed: %d %v", code, got)
	}

	// Deleted, it goes with the world.
	if code, _ := call("admin", "DELETE", "/orgs/alpha/worlds/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("deleted: %d", code)
	}
	if n := kept(packHost(libraryHost(id))); n != 0 {
		t.Errorf("%d packs left of a deleted world", n)
	}
}

// A saved world's zip has the world in one folder, as a save in the game
// has it: what a server kept beside it of the Nether and the End goes in
// it, a file it has twice goes once, and nothing else goes at all.
func TestZipEntry(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"world", "Map", true},
		{"world/", "Map", true},
		{"world/level.dat", "Map/level.dat", true},
		{"./world/region/r.0.0.mca", "Map/region/r.0.0.mca", true},
		{"world_nether/DIM-1/region/r.0.0.mca", "Map/DIM-1/region/r.0.0.mca", true},
		{"world_nether/DIM-1", "Map/DIM-1", true},
		{"world_the_end/DIM1/region/r.0.0.mca", "Map/DIM1/region/r.0.0.mca", true},
		{"world_nether/level.dat", "", false},
		{"world_the_end/DIM-1/r", "", false},
		{"worldly/level.dat", "", false},
		{"plugins/a.jar", "", false},
		{"world/../etc/passwd", "", false},
		{"/world/level.dat", "", false},
	} {
		got, ok := zipEntry(c.in, "world", "Map")
		if got != c.want || ok != c.ok {
			t.Errorf("%q: %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
	for in, want := range map[string]string{"Island": "Island", "A/B: c?": "A_B_ c_", "  .. ": "world", "": "world", "Ünïcode ✓": "Ünïcode ✓"} {
		if got := saveName(in); got != want {
			t.Errorf("saveName(%q) = %q, want %q", in, got, want)
		}
	}
}
