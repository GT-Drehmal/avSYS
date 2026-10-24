package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// archiveName is a file that can be opened in place: what bsdtar, in the
// workspace, reads.
var archiveName = regexp.MustCompile(`(?i)\.(zip|tar|tar\.gz|tgz|tar\.xz|txz|tar\.bz2|tbz2|7z|rar)$`)

// archiveStem is the name of an archive without its kind: what the folder it
// is opened into is called.
func archiveStem(name string) string {
	if m := archiveName.FindStringIndex(name); m != nil && m[0] > 0 {
		return name[:m[0]]
	}
	return name
}

// archiveTop says the one folder everything in an archive is in, as it is
// listed by bsdtar -t, or "" if its files are not all in one. What a Mac
// adds to a zip it makes is not counted: it is left out.
func archiveTop(listing string) string {
	top := ""
	for _, line := range strings.Split(listing, "\n") {
		// A name is as it is, spaces and all: only the line's end is not
		// part of it.
		name := strings.TrimPrefix(strings.TrimSuffix(line, "\r"), "./")
		if name == "" || name == "." || macJunk(name) {
			continue
		}
		first, _, nested := strings.Cut(name, "/")
		// A file at the top, or a second folder: not all in one.
		if !nested || first == "" || first == ".." || (top != "" && first != top) {
			return ""
		}
		top = first
	}
	return top
}

func macJunk(name string) bool { return name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") }

// runIn runs a command in a server's workspace, as its user, giving what it
// wrote to stdout into out, and failing with what it wrote to stderr.
func (s *Server) runIn(ctx context.Context, conn *ssh.Client, command string, out io.Writer) error {
	// The panel's own: a plain sh, with nothing from the home directory.
	session, err := backup.CommandSession(conn)
	if err != nil {
		return err
	}
	defer session.Close()
	var stderr bytes.Buffer
	session.Stdout = out
	session.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			if msg == "" {
				msg = err.Error()
			}
			return errors.New(msg)
		}
		return nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		session.Close()
		return ctx.Err()
	}
}

// extractArchive opens an archive where it is: into the one folder it holds,
// if its files are all in one, as a world's usually are; otherwise into a
// new folder named after it. Nothing already there is written over.
func (s *Server) extractArchive(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, server, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	name := path.Base(full)
	if full == dataRoot || !archiveName.MatchString(name) {
		return errBadRequest("That is not an archive that can be opened here.")
	}
	if fi, err := sc.Stat(full); err != nil {
		return fileError(err)
	} else if fi.IsDir() {
		return errBadRequest("That is a folder.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	conn, err := s.Gateway.DialWorkspace(ctx, a.Namespace, server)
	if err != nil {
		return &Error{Status: http.StatusServiceUnavailable, Code: "workspace", Message: "The server's files are not reachable right now."}
	}
	defer conn.Close()
	var listing bytes.Buffer
	if err := s.runIn(ctx, conn, "bsdtar -tf "+shellQuote(full), &listing); err != nil {
		return errBadRequest("The archive could not be read: %s", err)
	}
	parent := path.Dir(full)
	into, made := parent, ""
	if top := archiveTop(listing.String()); top != "" {
		made = path.Join(parent, top)
	} else {
		into = path.Join(parent, archiveStem(name))
		made = into
	}
	if _, err := sc.Lstat(made); err == nil {
		return &Error{Status: http.StatusConflict, Code: "exists", Message: fmt.Sprintf("%s is already here. Rename or delete it first.", path.Base(made))}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fileError(err)
	}
	if into != parent {
		if err := sc.Mkdir(into); err != nil {
			return fileError(err)
		}
	}
	// Never outside where it is opened: no absolute names, no "..", and no
	// file written through a link. Owned by the workspace's user.
	// And no file that is there already is written over (-k).
	cmd := "bsdtar -xkf " + shellQuote(full) + " -C " + shellQuote(into) + " --no-same-owner --no-same-permissions --exclude __MACOSX"
	if err := s.runIn(ctx, conn, cmd, io.Discard); err != nil {
		_ = sc.RemoveAll(made)
		return errBadRequest("The archive could not be opened: %s", err)
	}
	s.audit(r.Context(), u, a.Org, "files.extract", server, relPath(full))
	writeJSON(w, http.StatusOK, map[string]string{"path": relPath(made)})
	return nil
}

// downloadFolder sends a folder as a zip, made as it is sent.
func (s *Server) downloadFolder(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, server, sc, full, err := s.files(r, u)
	if err != nil {
		return err
	}
	if fi, err := sc.Stat(full); err != nil {
		return fileError(err)
	} else if !fi.IsDir() {
		return errBadRequest("That is not a folder.")
	}
	name := path.Base(full)
	if full == dataRoot {
		name = server
	}
	conn, err := s.Gateway.DialWorkspace(r.Context(), a.Namespace, server)
	if err != nil {
		return &Error{Status: http.StatusServiceUnavailable, Code: "workspace", Message: "The server's files are not reachable right now."}
	}
	defer conn.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+".zip"))
	// "./" before its name, so that bsdtar takes it as a folder whatever it
	// is called (not "@a" as another archive, nor "-a" as an option), and
	// the "./" taken off the names in the zip.
	cmd := "cd " + shellQuote(path.Dir(full)) + " && bsdtar -cf - --format zip -s ',^\\./,,' " + shellQuote("./"+path.Base(full))
	if full == dataRoot {
		cmd = "cd " + shellQuote(dataRoot) + " && bsdtar -cf - --format zip -s ',^\\./,,' ."
	}
	sent := &countWriter{}
	if err := s.runIn(r.Context(), conn, cmd, io.MultiWriter(w, sent)); err != nil {
		s.Log.Warn("folder download failed", "server", server, "path", relPath(full), "error", err)
		// Nothing sent yet: said so. Once it has begun, the download is cut
		// off, so that the browser says it failed rather than keep a zip
		// without some of the files.
		if sent.n == 0 {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Content-Type")
			return errBadRequest("The folder could not be zipped: %s", err)
		}
		panic(http.ErrAbortHandler)
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
