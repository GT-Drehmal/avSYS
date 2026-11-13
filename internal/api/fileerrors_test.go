package api

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// filesFrom gives the test panel's server "world" files served by these
// handlers, with its folder there.
func filesFrom(t *testing.T, p *panel, h sftp.Handlers) *sftp.Client {
	t.Helper()
	c, s := net.Pipe()
	server := sftp.NewRequestServer(s, h)
	go func() { _ = server.Serve() }()
	sc, err := sftp.NewClientPipe(c, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close(); server.Close() })
	if err := sc.MkdirAll(dataRoot); err != nil {
		t.Fatal(err)
	}
	used := &atomic.Int64{}
	used.Store(time.Now().UnixNano())
	p.srv.workspaces.mu.Lock()
	p.srv.workspaces.conns[p.srv.Namespace("alpha")+"/world"] = &pooledSFTP{sftp: sc, lastUsed: used}
	p.srv.workspaces.mu.Unlock()
	return sc
}

// filesCall asks the panel something of world's files, as an operator.
func filesCall(t *testing.T, p *panel, method, path string, body string, headers ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/orgs/alpha/servers/world/files"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.tokens["operator"])
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	var out struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Message
}

func put(t *testing.T, sc *sftp.Client, file, text string) {
	t.Helper()
	f, err := sc.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, sc *sftp.Client, file string) string {
	t.Helper()
	f, err := sc.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

// What goes wrong in a server's files is said in plain words, by name, and
// why where the files tell: never the file server's own words.
func TestFileErrorsSaidPlainly(t *testing.T) {
	p := newPanel(t)
	sc := filesFrom(t, p, sftp.InMemHandler())
	if err := sc.MkdirAll(dataRoot + "/world/region"); err != nil {
		t.Fatal(err)
	}
	put(t, sc, dataRoot+"/eula.txt", "eula=true\n")
	put(t, sc, dataRoot+"/world/level.dat", "level")

	for _, c := range []struct {
		name, method, path, body string
		headers                  []string
		code                     int
		message                  string
	}{
		{"an upload where a folder of its name is", "PUT", "/content?path=/world", "zip", nil, http.StatusConflict, "A folder named world already exists."},
		{"a new file where one of its name is", "PUT", "/content?path=/eula.txt", "", []string{"If-None-Match", "*"}, http.StatusConflict, "A file named eula.txt already exists."},
		{"a new file where a folder of its name is", "PUT", "/content?path=/world", "", []string{"If-None-Match", "*"}, http.StatusConflict, "A folder named world already exists."},
		{"a file into a folder that is not there", "PUT", "/content?path=/plugins/a.yml", "a", nil, http.StatusBadRequest, "There is no folder /plugins."},
		{"a file into a file", "PUT", "/content?path=/eula.txt/a.yml", "a", nil, http.StatusBadRequest, "There is no folder /eula.txt."},
		{"a folder where one of its name is", "POST", "/mkdir?path=/world", "", nil, http.StatusConflict, "A folder named world already exists."},
		{"a folder where a file of its name is", "POST", "/mkdir?path=/eula.txt", "", nil, http.StatusConflict, "A file named eula.txt already exists."},
		{"a folder in a file", "POST", "/mkdir?path=/eula.txt/more", "", nil, http.StatusConflict, "eula.txt is a file, not a folder."},
		{"renamed onto a folder", "POST", "/rename?path=/eula.txt&to=/world", "", nil, http.StatusConflict, "A folder named world already exists."},
		{"moved onto a file", "POST", "/rename?path=/world/level.dat&to=/level.dat", "", nil, 0, ""},
		{"moved back onto one there", "POST", "/rename?path=/level.dat&to=/world/region", "", nil, http.StatusConflict, "A folder named region already exists."},
		{"renamed, gone meanwhile", "POST", "/rename?path=/ghost.txt&to=/spirit.txt", "", nil, http.StatusNotFound, "ghost.txt was not found."},
		{"opened, gone meanwhile", "GET", "/content?path=/ghost.txt", "", nil, http.StatusNotFound, "ghost.txt was not found."},
		{"a folder opened, gone meanwhile", "GET", "?path=/ghosts", "", nil, http.StatusNotFound, "ghosts was not found."},
		{"deleted, gone meanwhile", "DELETE", "?path=/ghost.txt", "", nil, http.StatusNotFound, "ghost.txt was not found."},
	} {
		code, message := filesCall(t, p, c.method, c.path, c.body, c.headers...)
		if c.code == 0 {
			if code >= 300 {
				t.Fatalf("%s: %d %s", c.name, code, message)
			}
			continue
		}
		if code != c.code || message != c.message {
			t.Errorf("%s: %d %q, want %d %q", c.name, code, message, c.code, c.message)
		}
	}
	// The file made new was not written over.
	if got := read(t, sc, dataRoot+"/eula.txt"); got != "eula=true\n" {
		t.Errorf("a new file wrote over one there: %q", got)
	}
	// Nor was anything left beside it.
	entries, _ := sc.ReadDir(dataRoot)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".avsys-upload") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	// A new folder in new folders is still made.
	if code, message := filesCall(t, p, "POST", "/mkdir?path=/plugins/LuckPerms", ""); code != http.StatusNoContent {
		t.Errorf("new folders: %d %s", code, message)
	}
}

// A file sent in parts is told, as its first part is, that a folder of its
// name is there; and so if one is made there before its last part.
func TestPartsWhereAFolderIs(t *testing.T) {
	s := newPanel(t).srv
	sc := memFiles(t)
	if err := sc.Mkdir("/world"); err != nil {
		t.Fatal(err)
	}
	if code, _ := filePart(t, s, sc, "/world", "upload=first0123456789abc&offset=0&size=10", "hello"); code != http.StatusConflict {
		t.Errorf("first part: %d", code)
	}
	if code, out := filePart(t, s, sc, "/later.zip", "upload=later0123456789abc&offset=0&size=10", "hello"); code != http.StatusOK || out["received"] != float64(5) {
		t.Fatalf("first part: %d %v", code, out)
	}
	if err := sc.Mkdir("/later.zip"); err != nil {
		t.Fatal(err)
	}
	code, said := filePartSaid(t, s, sc, "/later.zip", "upload=later0123456789abc&offset=5&size=10", "world")
	if code != http.StatusConflict || said != "A folder named later.zip already exists." {
		t.Errorf("last part: %d %q", code, said)
	}
}

// filePartSaid is filePart, with what the panel says of one refused.
func filePartSaid(t *testing.T, s *Server, sc *sftp.Client, full, query, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/x?"+query, strings.NewReader(body))
	err := s.writeFilePart(rec, req, nil, &access{Namespace: "mc-" + t.Name()}, "survival", sc, full)
	if said, ok := err.(*Error); ok {
		return said.Status, said.Message
	}
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return rec.Code, ""
}

// A link deleted is the link: what is in the folder it points to stays. One
// that points nowhere can be deleted too.
func TestDeletedLinkIsTheLink(t *testing.T) {
	p := newPanel(t)
	sc := filesFrom(t, p, sftp.InMemHandler())
	if err := sc.MkdirAll(dataRoot + "/worlds/survival"); err != nil {
		t.Fatal(err)
	}
	put(t, sc, dataRoot+"/worlds/survival/level.dat", "level")
	if err := sc.Symlink(dataRoot+"/worlds/survival", dataRoot+"/world"); err != nil {
		t.Fatal(err)
	}
	if err := sc.Symlink(dataRoot+"/nowhere", dataRoot+"/broken"); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"/world", "/broken"} {
		if code, message := filesCall(t, p, "DELETE", "?path="+link, ""); code != http.StatusNoContent {
			t.Errorf("%s: %d %s", link, code, message)
		}
		if _, err := sc.Lstat(dataRoot + link); err == nil {
			t.Errorf("%s is still there", link)
		}
	}
	if got := read(t, sc, dataRoot+"/worlds/survival/level.dat"); got != "level" {
		t.Errorf("what the link pointed to went with it: %q", got)
	}
	// A folder and all in it, links and all.
	if code, message := filesCall(t, p, "DELETE", "?path=/worlds", ""); code != http.StatusNoContent {
		t.Errorf("a folder: %d %s", code, message)
	}
	if _, err := sc.Lstat(dataRoot + "/worlds"); err == nil {
		t.Error("the folder is still there")
	}
}

// addedWhileDeleted puts a folder in the one at dir as it is about to be
// removed, once, as a running game might.
type addedWhileDeleted struct {
	sftp.FileCmder
	dir  string
	done bool
}

func (a *addedWhileDeleted) Filecmd(r *sftp.Request) error {
	if r.Method == "Rmdir" && r.Filepath == a.dir && !a.done {
		a.done = true
		if err := a.FileCmder.Filecmd(sftp.NewRequest("Mkdir", a.dir+"/new")); err != nil {
			return err
		}
	}
	return a.FileCmder.Filecmd(r)
}

func (a *addedWhileDeleted) PosixRename(r *sftp.Request) error {
	return a.FileCmder.(sftp.PosixRenameFileCmder).PosixRename(r)
}

// A folder something is put in as it is deleted says so, and is deleted
// when tried again.
func TestFolderAddedToWhileDeleted(t *testing.T) {
	p := newPanel(t)
	h := sftp.InMemHandler()
	h.FileCmd = &addedWhileDeleted{FileCmder: h.FileCmd, dir: dataRoot + "/world"}
	sc := filesFrom(t, p, h)
	if err := sc.MkdirAll(dataRoot + "/world/region"); err != nil {
		t.Fatal(err)
	}
	put(t, sc, dataRoot+"/world/level.dat", "level")
	code, message := filesCall(t, p, "DELETE", "?path=/world", "")
	if code != http.StatusConflict || message != "Files were added to world as it was deleted. Try again." {
		t.Errorf("deleted: %d %q", code, message)
	}
	if code, message := filesCall(t, p, "DELETE", "?path=/world", ""); code != http.StatusNoContent {
		t.Errorf("tried again: %d %s", code, message)
	}
}

// fullDisk is files on a disk that fills up as the first write to it fails.
type fullDisk struct {
	sftp.FileWriter
	full atomic.Bool
}

type noRoom struct{ disk *fullDisk }

func (n noRoom) WriteAt([]byte, int64) (int, error) {
	n.disk.full.Store(true)
	return 0, syscall.ENOSPC
}

func (d *fullDisk) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	if _, err := d.FileWriter.Filewrite(r); err != nil {
		return nil, err
	}
	return noRoom{d}, nil
}

type fullDiskCmds struct {
	sftp.FileCmder
	disk *fullDisk
}

func (c fullDiskCmds) PosixRename(r *sftp.Request) error {
	return c.FileCmder.(sftp.PosixRenameFileCmder).PosixRename(r)
}

func (c fullDiskCmds) StatVFS(*sftp.Request) (*sftp.StatVFS, error) {
	free := uint64(1 << 20)
	if c.disk.full.Load() {
		free = 0
	}
	return &sftp.StatVFS{Bsize: 1, Frsize: 1, Blocks: 1 << 30, Bfree: free, Bavail: free, Files: 1000, Ffree: 900, Favail: 900}, nil
}

// A disk that fills up as something is written to it is said to be full,
// whether the file was sent at once or in parts.
func TestDiskFullSaidSo(t *testing.T) {
	p := newPanel(t)
	h := sftp.InMemHandler()
	disk := &fullDisk{FileWriter: h.FilePut}
	h.FilePut = disk
	h.FileCmd = fullDiskCmds{FileCmder: h.FileCmd, disk: disk}
	sc := filesFrom(t, p, h)
	code, message := filesCall(t, p, "PUT", "/content?path=/world.zip", "a world")
	if code != http.StatusInsufficientStorage || message != "The server's disk is full." {
		t.Errorf("sent at once: %d %q", code, message)
	}
	disk.full.Store(false)
	code, message = filePartSaid(t, p.srv, sc, dataRoot+"/world.zip", "upload=full0123456789abcd&offset=0&size=10", "a world")
	if code != http.StatusInsufficientStorage || message != "The server's disk is full." {
		t.Errorf("sent in parts: %d %q", code, message)
	}
}
