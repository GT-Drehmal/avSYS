package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"

	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// AuthorizeWorkspace implements sshgw.Authorizer: shells and files need the
// operator role.
func (s *Server) AuthorizeWorkspace(ctx context.Context, u *store.User, orgSlug, server string) (string, error) {
	denied := errors.New("No server named " + server + "." + orgSlug + ", or you do not have shell access to it.")
	org, err := s.Store.OrgBySlug(ctx, orgSlug)
	if err != nil {
		return "", denied
	}
	role, err := s.roleIn(ctx, u, org)
	if err != nil || !role.AtLeast(store.RoleOperator) {
		return "", denied
	}
	namespace := s.Namespace(org.Slug)
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: server}, &avsysv1.MinecraftServer{}); err != nil {
		return "", denied
	}
	// No new shells or file transfers while a restore rewrites the files.
	if op := s.ops.current(namespace, server); op != nil && rewrites(op.Kind) {
		doing := "restored"
		if op.Kind == kindCreate {
			doing = "made from a snapshot"
		}
		return "", errors.New(server + "." + orgSlug + " is being " + doing + ". Try again when that has finished.")
	}
	return namespace, nil
}

// terminal bridges a browser terminal to a shell in the server's workspace.
// Binary frames carry terminal bytes in both directions; text frames from the
// browser are JSON control messages (resize).
func (s *Server) terminal(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if op := s.ops.current(a.Namespace, mc.Name); op != nil && rewrites(op.Kind) {
		return heldBy(op.Kind)
	}
	// Accept checks that the Origin header matches the Host, which is what
	// stops other sites from opening this socket with the user's cookie.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	fail := func(msg string) error {
		_ = ws.Write(ctx, websocket.MessageBinary, []byte("\r\n\x1b[31m"+msg+"\x1b[0m\r\n"))
		_ = ws.Close(websocket.StatusInternalError, "")
		return nil
	}
	conn, err := s.Gateway.DialWorkspace(ctx, a.Namespace, mc.Name)
	if err != nil {
		return fail("The workspace is not reachable right now. It may be restarting.")
	}
	defer conn.Close()
	session, err := conn.NewSession()
	if err != nil {
		return fail("Could not open a shell.")
	}
	defer session.Close()

	_ = session.Setenv("AVSYS_USER", u.Username)
	if err := session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		return fail("Could not allocate a terminal.")
	}
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Shell(); err != nil {
		return fail("Could not start a shell.")
	}
	s.audit(ctx, u, a.Org, "terminal.open", mc.Name, "")

	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer cancel()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := stdin.Write(data); err != nil {
					return
				}
				continue
			}
			var msg struct {
				Type       string
				Cols, Rows int
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				_ = session.WindowChange(msg.Rows, msg.Cols)
			}
		}
	}()
	<-ctx.Done()
	// The browser has usually gone already, so a failed close is expected.
	_ = ws.Close(websocket.StatusNormalClosure, "")
	return nil
}

// workspacePool keeps one SFTP connection per server around between file
// requests, so browsing a directory tree does not pay for an SSH handshake on
// every click.
type workspacePool struct {
	gateway *sshgw.Gateway
	mu      sync.Mutex
	conns   map[string]*pooledSFTP
}

type pooledSFTP struct {
	ssh      *ssh.Client
	sftp     *sftp.Client
	lastUsed time.Time
}

const sftpIdle = 2 * time.Minute

func newWorkspacePool(g *sshgw.Gateway) *workspacePool {
	p := &workspacePool{gateway: g, conns: map[string]*pooledSFTP{}}
	go func() {
		for range time.Tick(30 * time.Second) {
			p.mu.Lock()
			for key, c := range p.conns {
				if time.Since(c.lastUsed) > sftpIdle {
					c.close()
					delete(p.conns, key)
				}
			}
			p.mu.Unlock()
		}
	}()
	return p
}

func (c *pooledSFTP) close() {
	c.sftp.Close()
	c.ssh.Close()
}

func (p *workspacePool) get(ctx context.Context, namespace, server string) (*sftp.Client, error) {
	key := namespace + "/" + server
	p.mu.Lock()
	if c := p.conns[key]; c != nil {
		// A cheap round trip tells a live connection from one whose pod is gone.
		if _, err := c.sftp.Getwd(); err == nil {
			c.lastUsed = time.Now()
			p.mu.Unlock()
			return c.sftp, nil
		}
		c.close()
		delete(p.conns, key)
	}
	p.mu.Unlock()

	conn, err := p.gateway.DialWorkspace(ctx, namespace, server)
	if err != nil {
		return nil, err
	}
	sc, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.conns[key]; existing != nil {
		// Another request connected first; use theirs.
		sc.Close()
		conn.Close()
		existing.lastUsed = time.Now()
		return existing.sftp, nil
	}
	p.conns[key] = &pooledSFTP{ssh: conn, sftp: sc, lastUsed: time.Now()}
	return sc, nil
}

// dataRoot is the server directory. The file API does not reach outside it.
const dataRoot = "/data"

// dataPath resolves a client-supplied path to an absolute one under dataRoot.
func dataPath(p string) (string, error) {
	full := path.Join(dataRoot, path.Clean("/"+p))
	if full != dataRoot && !strings.HasPrefix(full, dataRoot+"/") {
		return "", errBadRequest("Path is outside the server directory.")
	}
	return full, nil
}

// files resolves the request to an SFTP client and the path it names.
func (s *Server) files(r *http.Request, u *store.User) (*access, string, *sftp.Client, string, error) {
	a, server, sc, full, _, err := s.filesFor(r, u, false)
	return a, server, sc, full, err
}

// writableFiles is files for a request that changes them. It is refused
// while a restore is putting the server's files back; the caller must call
// release when its change has been made.
func (s *Server) writableFiles(r *http.Request, u *store.User) (a *access, server string, sc *sftp.Client, full string, release func(), err error) {
	return s.filesFor(r, u, true)
}

func (s *Server) filesFor(r *http.Request, u *store.User, write bool) (*access, string, *sftp.Client, string, func(), error) {
	none := func() {}
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return nil, "", nil, "", none, err
	}
	release := none
	if write {
		if release, err = s.changing(mc, false); err != nil {
			return nil, "", nil, "", none, err
		}
	}
	a, server, sc, full, err := s.openFiles(r, a, mc)
	if err != nil {
		release()
		return nil, "", nil, "", none, err
	}
	return a, server, sc, full, release, nil
}

func (s *Server) openFiles(r *http.Request, a *access, mc *avsysv1.MinecraftServer) (*access, string, *sftp.Client, string, error) {
	full, err := dataPath(r.URL.Query().Get("path"))
	if err != nil {
		return nil, "", nil, "", err
	}
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return nil, "", nil, "", &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. It may be restarting."}
	}
	return a, mc.Name, sc, full, nil
}

func fileError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errNotFound
	case errors.Is(err, fs.ErrPermission):
		return &Error{Status: http.StatusForbidden, Code: "permission", Message: "Permission denied."}
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return errBadRequest("%s", status.Error())
	}
	return err
}

type fileEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Dir     bool      `json:"dir"`
	Link    bool      `json:"link,omitempty"`
	ModTime time.Time `json:"modTime"`
}

// relPath is the client-facing form of an absolute path under dataRoot.
func relPath(full string) string {
	if rel := strings.TrimPrefix(full, dataRoot); rel != "" {
		return rel
	}
	return "/"
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, _, sc, full, err := s.files(r, u)
	if err != nil {
		return err
	}
	infos, err := sc.ReadDir(full)
	if err != nil {
		return fileError(err)
	}
	entries := make([]fileEntry, 0, len(infos))
	for _, fi := range infos {
		e := fileEntry{Name: fi.Name(), Size: fi.Size(), Dir: fi.IsDir(), ModTime: fi.ModTime().UTC()}
		if fi.Mode()&fs.ModeSymlink != 0 {
			e.Link = true
			if target, err := sc.Stat(path.Join(full, fi.Name())); err == nil {
				e.Dir = target.IsDir()
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": relPath(full), "entries": entries})
	return nil
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, _, sc, full, err := s.files(r, u)
	if err != nil {
		return err
	}
	f, err := sc.Open(full)
	if err != nil {
		return fileError(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fileError(err)
	}
	if fi.IsDir() {
		return errBadRequest("That is a directory.")
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(full)))
	}
	// Never let a file from a game server be interpreted as a web page on
	// the panel's origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

// maxUpload bounds a single file write. World archives can be large; anything
// beyond this is better moved with rsync or scp over SSH.
const maxUpload = 2 << 30

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, server, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	if full == dataRoot {
		return errBadRequest("Name a file to write.")
	}
	// Written to a sibling temp file and renamed into place, so an upload
	// that is cut off never leaves a half-written config behind.
	tmp := full + ".avsys-upload"
	f, err := sc.Create(tmp)
	if err != nil {
		return fileError(err)
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sc.PosixRename(tmp, full)
	}
	if err != nil {
		_ = sc.Remove(tmp)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "Files over 2 GB are better copied with scp or rsync over SSH."}
		}
		return fileError(err)
	}
	s.audit(r.Context(), u, a.Org, "files.write", server, relPath(full))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) makeDir(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, _, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	if err := sc.MkdirAll(full); err != nil {
		return fileError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) renameFile(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, server, sc, from, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	to, err := dataPath(r.URL.Query().Get("to"))
	if err != nil {
		return err
	}
	if from == dataRoot || to == dataRoot {
		return errBadRequest("The server directory itself cannot be moved.")
	}
	if _, err := sc.Lstat(to); err == nil {
		return &Error{Status: http.StatusConflict, Code: "exists", Message: "Something with that name already exists."}
	}
	if err := sc.Rename(from, to); err != nil {
		return fileError(err)
	}
	s.audit(r.Context(), u, a.Org, "files.rename", server, relPath(from)+" -> "+relPath(to))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, server, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	if full == dataRoot {
		return errBadRequest("The server directory itself cannot be deleted.")
	}
	if err := sc.RemoveAll(full); err != nil {
		return fileError(err)
	}
	s.audit(r.Context(), u, a.Org, "files.delete", server, relPath(full))
	w.WriteHeader(http.StatusNoContent)
	return nil
}
