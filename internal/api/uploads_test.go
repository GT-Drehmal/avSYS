package api

import (
	"encoding/json"
	"errors"
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

	"github.com/pkg/sftp"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
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
	// Taken only whole, and what was refused is not left in the way of the
	// next try.
	if code, _ := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Island&root=Island&upload=island0123456789&size=99", ""); code != http.StatusBadRequest {
		t.Errorf("taken though not whole: %d", code)
	}
	if left, _ := os.ReadDir(filepath.Join(dir, ".uploads", "alpha")); len(left) != 0 {
		t.Errorf("left behind by a refusal: %v", left)
	}
	if code, out := call("operator", "PUT", up+"&offset=0", "a world, zipped"); code != http.StatusOK || out["received"] != float64(15) {
		t.Fatalf("sent again: %d %v", code, out)
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
	// Nor is the folder taken for an organization's repository, even where
	// an organization's name makes it look like one.
	if err := os.MkdirAll(filepath.Join(dir, ".uploads", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
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

// A name already taken turns a world down once it has all been sent, and what
// was sent goes with it: otherwise it holds the room the next try needs.
func TestRefusedWorldLeavesNothing(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	send := func(method, path, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens["admin"])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		return rec.Code
	}
	staged := filepath.Join(dir, ".uploads", "alpha")
	const up = "/orgs/alpha/uploads?upload=island0123456789"
	const world = "/orgs/alpha/worlds/upload?root=Island&upload=island0123456789&size=5"
	if send("PUT", up+"&offset=0", "first") != http.StatusOK || send("POST", world+"&name=Island", "") != http.StatusCreated {
		t.Fatal("the first world was not kept")
	}
	if send("PUT", up+"&offset=0", "again") != http.StatusOK {
		t.Fatal("the second was not sent")
	}
	if code := send("POST", world+"&name=island", ""); code != http.StatusConflict {
		t.Fatalf("a name already taken: %d", code)
	}
	if left, _ := os.ReadDir(staged); len(left) != 0 {
		t.Errorf("left by a name already taken: %v", left)
	}
	// A template's: gone with the template it was for, too.
	if send("PUT", up+"&offset=0", "again") != http.StatusOK {
		t.Fatal("the third was not sent")
	}
	if code := send("PUT", "/orgs/alpha/templates/nothing/world?root=Island&upload=island0123456789&size=5", ""); code != http.StatusNotFound {
		t.Fatalf("no such template: %d", code)
	}
	if left, _ := os.ReadDir(staged); len(left) != 0 {
		t.Errorf("left by no such template: %v", left)
	}
}

// What was sent by someone who went away is cleared for every organization,
// not only when that organization next sends something; what is still being
// sent stays. An organization's go with it.
func TestStagedSweep(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Backups: &backup.Manager{Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	write := func(org, name string, age time.Duration) string {
		t.Helper()
		file := filepath.Join(dir, ".uploads", org, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(file, at, at); err != nil {
			t.Fatal(err)
		}
		return file
	}
	gone, going := write("alpha", "gone0123456789abcd", 2*stagedIdle), write("beta", "going0123456789abc", time.Minute)
	other := write("beta", "gone0123456789abcd", 2*stagedIdle)
	s.sweepStaged()
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("one left long ago stayed: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("another organization's stayed: %v", err)
	}
	if _, err := os.Stat(going); err != nil {
		t.Errorf("one being sent went: %v", err)
	}
	if err := s.Backups.Destroy("beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".uploads", "beta")); !os.IsNotExist(err) {
		t.Errorf("a deleted organization's stayed: %v", err)
	}
}

// filePart sends one part of a file to a server's files.
func filePart(t *testing.T, s *Server, sc *sftp.Client, full, query, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/x?"+query, strings.NewReader(body))
	err := s.writeFilePart(rec, req, &store.User{Username: "someone"}, &access{Namespace: "mc-" + t.Name(), Org: &store.Org{Slug: "alpha"}}, "survival", sc, full)
	if err != nil {
		var said *Error
		if !errors.As(err, &said) {
			return http.StatusInternalServerError, nil
		}
		return said.Status, nil
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The last part moves the file into place before saying so. If that answer
// is lost, the file is not sent all over again: the panel knows that upload
// finished, by its name. A file of the same size being there says nothing,
// so another upload of one is still sent.
func TestLastPartAnswerLost(t *testing.T) {
	s := newPanel(t).srv
	sc := memFiles(t)
	const full = "/world.zip"
	if code, out := filePart(t, s, sc, full, "upload=first0123456789abc&offset=0&size=5", "hello"); code != http.StatusOK || out["received"] != float64(5) {
		t.Fatalf("sent: %d %v", code, out)
	}
	// Sent again, as after a lost answer: nothing is written, and it is done.
	if code, out := filePart(t, s, sc, full, "upload=first0123456789abc&offset=0&size=5", "XXXXX"); code != http.StatusOK || out["received"] != float64(5) {
		t.Errorf("sent again: %d %v", code, out)
	}
	key := "mc-" + t.Name() + "/survival/" + fileUpload(full, "first0123456789abc")
	if got, err := fileSent(key, sc, fileUpload(full, "first0123456789abc")); err != nil || got != 5 {
		t.Errorf("asked after: %d %v", got, err)
	}
	read := func() string {
		t.Helper()
		f, err := sc.Open(full)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return string(b)
	}
	if got := read(); got != "hello" {
		t.Errorf("the file became %q", got)
	}
	// Another of the same size is a file of its own.
	if code, _ := filePart(t, s, sc, full, "upload=second0123456789ab&offset=0&size=5", "later"); code != http.StatusOK {
		t.Fatalf("the second: %d", code)
	}
	if got := read(); got != "later" {
		t.Errorf("the second became %q", got)
	}
}

// Files going out of reach partway are sent again once they are back, not
// given up on, and not taken for nothing being there.
func TestPartWhenFilesGoOutOfReach(t *testing.T) {
	s := newPanel(t).srv
	sc := memFiles(t)
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _ := filePart(t, s, sc, "/world.zip", "upload=lost0123456789abcd&offset=32&size=64", "part"); code != http.StatusBadGateway {
		t.Errorf("a part: %d", code)
	}
	if _, err := fileSent("lost", sc, fileUpload("/world.zip", "lost0123456789abcd")); err == nil {
		t.Error("files out of reach were taken for nothing being there")
	}
}

// The last part can be written and the files go out of reach before it is
// moved into place. Asked once they are back, with how large the whole is,
// it is moved then, and only once: not said to be all there while it is
// still under its upload's name.
func TestLastPartWrittenNotMoved(t *testing.T) {
	p := newPanel(t)
	s, sc := p.srv, memFiles(t)
	const full, name = "/world.zip", "unmoved0123456789a"
	tmp := fileUpload(full, name)
	f, err := sc.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	askAt := func(full, query string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x?"+query, nil)
		if err := s.filePartsReceived(rec, req, &store.User{Username: "someone"}, &access{Namespace: "mc-" + t.Name(), Org: &store.Org{Slug: "alpha"}}, "survival", sc, full); err != nil {
			var said *Error
			if !errors.As(err, &said) {
				return http.StatusInternalServerError, nil
			}
			return said.Status, nil
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ask := func(query string) (int, map[string]any) { t.Helper(); return askAt(full, query) }
	if code, out := ask("upload=" + name + "&size=5"); code != http.StatusOK || out["received"] != float64(5) {
		t.Fatalf("asked: %d %v", code, out)
	}
	b, err := func() ([]byte, error) {
		f, err := sc.Open(full)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}()
	if err != nil || string(b) != "hello" {
		t.Errorf("not moved into place: %q %v", b, err)
	}
	if _, err := sc.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("left under its upload's name: %v", err)
	}
	// Asked again, or its last part sent again, it is done, and written once.
	if code, out := ask("upload=" + name + "&size=5"); code != http.StatusOK || out["received"] != float64(5) {
		t.Errorf("asked again: %d %v", code, out)
	}
	if code, out := filePart(t, s, sc, full, "upload="+name+"&offset=0&size=5", "hello"); code != http.StatusOK || out["received"] != float64(5) {
		t.Errorf("sent again: %d %v", code, out)
	}
	if got, err := p.srv.Store.ServerActivity(t.Context(), "", "survival", time.Time{}, []string{"files.write"}, 10); err != nil || len(got) != 1 {
		t.Errorf("written %d times: %v", len(got), err)
	}
	// One that cannot be moved says so, not that it is all there, and what
	// was sent stays.
	if err := sc.MkdirAll("/taken.zip/inside"); err != nil {
		t.Fatal(err)
	}
	f, err = sc.Create(fileUpload("/taken.zip", name))
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("hello"))
	f.Close()
	if code, out := askAt("/taken.zip", "upload="+name+"&size=5"); code == http.StatusOK {
		t.Errorf("not moved, and said to be: %d %v", code, out)
	}
	if _, err := sc.Stat(fileUpload("/taken.zip", name)); err != nil {
		t.Errorf("what was sent went: %v", err)
	}
	// Part of one is only how much is there.
	f, err = sc.Create(fileUpload(full, "partial0123456789a"))
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("hel"))
	f.Close()
	if code, out := ask("upload=partial0123456789a&size=5"); code != http.StatusOK || out["received"] != float64(3) {
		t.Errorf("part of one: %d %v", code, out)
	}
	if _, err := sc.Stat(fileUpload(full, "partial0123456789a")); err != nil {
		t.Errorf("part of one was moved: %v", err)
	}
}
