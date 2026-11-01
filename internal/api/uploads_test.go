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

	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// A world too large for one request is sent in parts, each after the one
// before, put together beside the repository, and kept from there: as large
// as there is room for. What was sent goes once it is kept.
func TestWorldSentInParts(t *testing.T) {
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
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const up = "/orgs/alpha/uploads?upload=island0123456789"

	if code, _ := call("viewer", "PUT", up+"&offset=0", "a "); code != http.StatusForbidden {
		t.Errorf("a viewer sent a part: %d", code)
	}
	// More than there is room for is turned down before anything is kept.
	if code, out := call("operator", "PUT", up+"&offset=0&size=4611686018427387904", "a "); code != http.StatusInsufficientStorage || !strings.Contains(out["message"].(string), "free") {
		t.Errorf("too large for the room there is: %d %v", code, out)
	}
	if code, out := call("operator", "PUT", up+"&offset=0&length=2&size=13", "a "); code != http.StatusOK || out["received"] != float64(2) {
		t.Fatalf("first part: %d %v", code, out)
	}
	// One out of turn says where it got to; one cut short is taken off.
	if code, out := call("operator", "PUT", up+"&offset=9", "x"); code != http.StatusConflict || out["received"] != float64(2) {
		t.Errorf("a part out of turn: %d %v", code, out)
	}
	if code, _ := call("operator", "PUT", up+"&offset=2&length=50", "world, "); code != http.StatusBadRequest {
		t.Errorf("a part cut short: %d", code)
	}
	if code, out := call("operator", "GET", up, ""); code != http.StatusOK || out["received"] != float64(2) {
		t.Errorf("after one cut short: %d %v", code, out)
	}
	if code, out := call("operator", "PUT", up+"&offset=2", "world, zipped"); code != http.StatusOK || out["received"] != float64(15) {
		t.Fatalf("last part: %d %v", code, out)
	}
	// Taken only whole.
	if code, _ := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Island&root=Island&upload=island0123456789&size=99", ""); code != http.StatusBadRequest {
		t.Errorf("taken though not whole: %d", code)
	}
	code, made := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Island&root=Island&upload=island0123456789&size=15", "")
	if code != http.StatusCreated || made["size"] != float64(15) {
		t.Fatalf("kept: %d %v", code, made)
	}
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/worlds/"+made["id"].(string)+"/archive", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["operator"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	if rec.Body.String() != "a world, zipped" {
		t.Errorf("kept as %q", rec.Body.String())
	}
	if left, _ := os.ReadDir(filepath.Join(dir, ".uploads", "alpha")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	// Nor is the folder taken for an organization's repository.
	if orgs := p.srv.Backups.Organizations(); len(orgs) != 1 || orgs[0] != "alpha" {
		t.Errorf("organizations: %v", orgs)
	}

	// A template's own world the same way.
	_, tpl := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Big","spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`)
	const tup = "/orgs/alpha/uploads?upload=template0123456789"
	if code, _ := call("admin", "PUT", tup+"&offset=0&size=5", "large"); code != http.StatusOK {
		t.Fatalf("its part: %d", code)
	}
	if code, got := call("admin", "PUT", "/orgs/alpha/templates/"+tpl["id"].(string)+"/world?root=Big&upload=template0123456789&size=5", ""); code != http.StatusOK || got["ownWorld"].(map[string]any)["size"] != float64(5) {
		t.Errorf("a template's world: %d %v", code, got)
	}
	// Given up on, it can be taken away.
	if code, _ := call("operator", "PUT", up+"&offset=0", "half"); code != http.StatusOK {
		t.Fatal(code)
	}
	if code, _ := call("operator", "DELETE", up, ""); code != http.StatusNoContent {
		t.Errorf("taken away: %d", code)
	}
	if code, out := call("operator", "GET", up, ""); out["received"] != float64(0) {
		t.Errorf("after it was taken away: %d %v", code, out)
	}
}
