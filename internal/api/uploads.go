package api

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A file for the organization itself, such as a world for its library, is
// sent in parts as one for a server is: each written after the one before,
// beside the organization's snapshot repository, where it is kept once it is
// whole. It is then named, by ?upload=, to what takes it, which removes it.
// How large one can be is how much room there is, not a number.

// stagedIdle is how long a file sent in part is kept with no part coming.
const stagedIdle = 24 * time.Hour

// stagedLocks holds a lock per file being sent, so that two parts of it are
// never written at once.
var stagedLocks sync.Map

// stagedDir is where an organization's files sent in parts are put
// together: beside its repository, on the same disk.
func (s *Server) stagedDir(org string) string {
	return filepath.Join(s.Backups.Dir, ".uploads", org)
}

// stagedFile is one of them, by the name its sender gave it.
func (s *Server) stagedFile(a *access, name string) (string, error) {
	if !uploadName.MatchString(name) {
		return "", errBadRequest("Say which upload this is, as ?upload=.")
	}
	return filepath.Join(s.stagedDir(a.Org.Slug), name), nil
}

// freeSpace is how many bytes can still be written in dir.
func freeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// errNoRoom says how much was asked for and how much there is.
func errNoRoom(need, free int64, where string) error {
	return &Error{Status: http.StatusInsufficientStorage, Code: "no_room", Message: fmt.Sprintf("This needs %s and %s has %s free.", sizeOf(need), where, sizeOf(free))}
}

// stagedReceived says how much of a file sent in parts is there.
func (s *Server) stagedReceived(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	file, err := s.stagedFile(a, r.URL.Query().Get("upload"))
	if err != nil {
		return err
	}
	var got int64
	if fi, err := os.Stat(file); err == nil {
		got = fi.Size()
	}
	writeJSON(w, http.StatusOK, uploaded{Received: got})
	return nil
}

// stagePart writes a part of the file ?upload= names at ?offset=, which is
// where the last one ended; at 0 it starts again. With ?length=, a part that
// is not as long is taken as cut short. ?size=, the whole of it, is checked
// against the room there is as it starts.
func (s *Server) stagePart(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	q := r.URL.Query()
	file, err := s.stagedFile(a, q.Get("upload"))
	if err != nil {
		return err
	}
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		return errBadRequest("Say where this part goes, as ?offset=.")
	}
	length := int64(-1)
	if l := q.Get("length"); l != "" {
		if length, err = strconv.ParseInt(l, 10, 64); err != nil || length < 0 {
			return errBadRequest("Say how long this part is, as ?length=.")
		}
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, _ := stagedLocks.LoadOrStore(file, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	flags := os.O_WRONLY | os.O_APPEND
	if offset == 0 {
		s.dropStale(dir)
		// It is kept here while it is sent, and once more where it goes.
		if size, _ := strconv.ParseInt(q.Get("size"), 10, 64); size > 0 {
			if free, err := freeSpace(dir); err == nil && size > free/2 {
				return errNoRoom(min(size, math.MaxInt64/2)*2, free, "the panel")
			}
		}
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	} else {
		var had int64
		if fi, err := os.Stat(file); err == nil {
			had = fi.Size()
		}
		if offset != had {
			writeJSON(w, http.StatusConflict, struct {
				Code     string `json:"code"`
				Message  string `json:"message"`
				Received int64  `json:"received"`
			}{"offset", "This part is not the next one.", had})
			return nil
		}
	}
	f, err := os.OpenFile(file, flags, 0o600)
	if err != nil {
		return err
	}
	body := &readCounted{r: http.MaxBytesReader(w, r.Body, maxPart)}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && length >= 0 && n != length {
		err, body.err = io.ErrUnexpectedEOF, io.ErrUnexpectedEOF
	}
	if err != nil {
		// What of it was written is cut off again, for it to be sent again.
		_ = os.Truncate(file, offset)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "A part can be at most 64 MB."}
		case body.err != nil:
			return errBadRequest("The part did not arrive whole: %v.", body.err)
		default:
			return err
		}
	}
	writeJSON(w, http.StatusOK, uploaded{Received: offset + n})
	return nil
}

// discardStaged removes what was sent of the file ?upload= names.
func (s *Server) discardStaged(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	file, err := s.stagedFile(a, r.URL.Query().Get("upload"))
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// takeStaged opens the file ?upload= names, all of it sent: as large as
// ?size= says. done removes it.
func (s *Server) takeStaged(a *access, q interface{ Get(string) string }) (f *os.File, done func(), err error) {
	file, err := s.stagedFile(a, q.Get("upload"))
	if err != nil {
		return nil, nil, err
	}
	size, err := strconv.ParseInt(q.Get("size"), 10, 64)
	if err != nil || size < 0 {
		return nil, nil, errBadRequest("Say how large what was sent is, as ?size=.")
	}
	f, err = os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, errBadRequest("Nothing was sent by that name.")
	} else if err != nil {
		return nil, nil, err
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != size {
		f.Close()
		got := int64(0)
		if fi != nil {
			got = fi.Size()
		}
		return nil, nil, errBadRequest("Only %s of it arrived. Send it again.", sizeOf(got))
	}
	return f, func() {
		f.Close()
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.Log.Warn("could not remove a file sent in parts", "file", file, "error", err)
		}
	}, nil
}

// dropStale removes what was sent of files that have had no part for a day:
// their senders are gone.
func (s *Server) dropStale(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > stagedIdle {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// uploadBody is what is sent to be kept: what was sent in parts, where
// ?upload= names it, or else the request's own body, which can be at most
// 2 GB.
func (s *Server) uploadBody(w http.ResponseWriter, r *http.Request, a *access) (io.Reader, func(), error) {
	if r.URL.Query().Get("upload") == "" {
		return http.MaxBytesReader(w, r.Body, maxUpload), func() {}, nil
	}
	f, done, err := s.takeStaged(a, r.URL.Query())
	if err != nil {
		return nil, nil, err
	}
	return f, done, nil
}

// fileUpload is where the parts of a file sent to a server's files are put
// together, beside where it goes, under its sender's name for it: moved
// into place whole, so that one cut short never leaves half a file.
func fileUpload(full, name string) string { return full + ".avsys-upload-" + name }

// writeFilePart writes a part of a file sent to a server's files, as
// ?upload= names it, at ?offset=, of ?size= in all; the last part moves it
// into place. With ?length=, a part that is not as long is cut short.
func (s *Server) writeFilePart(w http.ResponseWriter, r *http.Request, u *store.User, a *access, server string, sc *sftp.Client, full string) error {
	q := r.URL.Query()
	name := q.Get("upload")
	if !uploadName.MatchString(name) {
		return errBadRequest("Say which upload this part is of, as ?upload=.")
	}
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		return errBadRequest("Say where this part goes, as ?offset=.")
	}
	size, err := strconv.ParseInt(q.Get("size"), 10, 64)
	if err != nil || size < 0 {
		return errBadRequest("Say how large the file is, as ?size=.")
	}
	length := int64(-1)
	if l := q.Get("length"); l != "" {
		if length, err = strconv.ParseInt(l, 10, 64); err != nil || length < 0 {
			return errBadRequest("Say how long this part is, as ?length=.")
		}
	}
	tmp := fileUpload(full, name)
	lock, _ := stagedLocks.LoadOrStore(a.Namespace+"/"+server+"/"+tmp, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	flags := os.O_WRONLY | os.O_APPEND
	if offset == 0 {
		// There must be room for it on the server's volume.
		if vfs, err := sc.StatVFS(path.Dir(full)); err == nil {
			if free := int64(vfs.Bavail) * int64(vfs.Frsize); size > free {
				return errNoRoom(size, free, "the server")
			}
		}
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	} else {
		var had int64
		if fi, err := sc.Stat(tmp); err == nil {
			had = fi.Size()
		}
		if offset != had {
			writeJSON(w, http.StatusConflict, struct {
				Code     string `json:"code"`
				Message  string `json:"message"`
				Received int64  `json:"received"`
			}{"offset", "This part is not the next one.", had})
			return nil
		}
	}
	f, err := sc.OpenFile(tmp, flags)
	if err != nil {
		return fileError(err)
	}
	body := &readCounted{r: http.MaxBytesReader(w, r.Body, maxPart)}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (length >= 0 && n != length || offset+n > size) {
		err, body.err = io.ErrUnexpectedEOF, io.ErrUnexpectedEOF
	}
	if err != nil {
		_ = sc.Truncate(tmp, offset)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "A part can be at most 64 MB."}
		case body.err != nil:
			return errBadRequest("The part did not arrive whole: %v.", body.err)
		default:
			return fileError(err)
		}
	}
	if offset+n == size {
		if err := sc.PosixRename(tmp, full); err != nil {
			return fileError(err)
		}
		s.audit(r.Context(), u, a.Org, "files.write", server, relPath(full))
	}
	writeJSON(w, http.StatusOK, uploaded{Received: offset + n})
	return nil
}

// fileReceived says how much of a file sent in parts to a server's files is
// there.
func (s *Server) fileReceived(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, _, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	name := r.URL.Query().Get("upload")
	if !uploadName.MatchString(name) {
		return errBadRequest("Say which upload this is, as ?upload=.")
	}
	var got int64
	if fi, err := sc.Stat(fileUpload(full, name)); err == nil {
		got = fi.Size()
	}
	writeJSON(w, http.StatusOK, uploaded{Received: got})
	return nil
}
