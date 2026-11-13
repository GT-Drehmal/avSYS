package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
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
	return s.Backups.UploadsDir(org)
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
// ?size= says. done closes it.
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
		return nil, nil, errBadRequest("Only %s of the file arrived. Send the file again.", sizeOf(got))
	}
	return f, func() { f.Close() }, nil
}

// dropStaged removes the file sent in parts that a request was to take. It
// is taken once, kept or turned down: another try sends it again, and one
// left here would be in the way of that, in the room it needs.
func (s *Server) dropStaged(a *access, name string) {
	if name == "" {
		return
	}
	file, err := s.stagedFile(a, name)
	if err != nil {
		return
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.Log.Warn("could not remove a file sent in parts", "file", file, "error", err)
	}
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

// RunStagedSweep clears what was sent of files whose senders went away, for
// every organization, as the panel starts and every hour until ctx ends.
// One whose page was closed says nothing, and its organization may send
// nothing more that would clear it.
func (s *Server) RunStagedSweep(ctx context.Context) {
	if !s.Backups.Available() {
		return
	}
	for {
		s.sweepStaged()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func (s *Server) sweepStaged() {
	root := filepath.Dir(s.Backups.UploadsDir("-"))
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			s.dropStale(filepath.Join(root, e.Name()))
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

// filePartError is what is said of a server's files that could not be
// reached or written: what is the file's own doing as it is, and anything
// else as the files being out of reach, for the part to be sent again once
// they are back.
func (s *Server) filePartError(err error, server string) error {
	said := fileError(err)
	var known *Error
	if errors.As(said, &known) {
		return said
	}
	s.Log.Warn("an upload's part could not be written", "server", server, "error", err)
	return errWorkspace
}

// partsThere says how much of a file sent in parts is at tmp. Files out of
// reach are not the same as nothing there: said to be nothing, the file
// would be sent again from the start, and what was sent cut off.
func partsThere(sc *sftp.Client, tmp string) (int64, error) {
	fi, err := sc.Stat(tmp)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// sentWhole holds, for a while, the files sent in parts that reached their
// place. The last part moves one there before saying so, and that answer can
// be lost: asked after, it is not to be sent all over again, which there may
// no longer be the room for. Held by the upload's own name, as a file of the
// same size being there says nothing of whether this one arrived.
var sentWhole sync.Map

type wholeFile struct {
	size int64
	at   time.Time
}

// keptSentWhole remembers one, and forgets those long gone.
func keptSentWhole(key string, size int64) {
	now := time.Now()
	sentWhole.Store(key, wholeFile{size, now})
	sentWhole.Range(func(k, v any) bool {
		if now.Sub(v.(wholeFile).at) > stagedIdle {
			sentWhole.Delete(k)
		}
		return true
	})
}

// fileSent says how much of a file sent in parts has arrived.
func fileSent(key string, sc *sftp.Client, tmp string) (int64, error) {
	if v, whole := sentWhole.Load(key); whole {
		return v.(wholeFile).size, nil
	}
	return partsThere(sc, tmp)
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
	key := a.Namespace + "/" + server + "/" + tmp
	lock, _ := stagedLocks.LoadOrStore(key, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	if v, whole := sentWhole.Load(key); whole {
		writeJSON(w, http.StatusOK, uploaded{Received: v.(wholeFile).size})
		return nil
	}
	flags := os.O_WRONLY | os.O_APPEND
	if offset == 0 {
		if err := placeable(sc, full); err != nil {
			return s.filePartError(err, server)
		}
		dropStaleParts(sc, path.Dir(full))
		// There must be room for it on the server's volume.
		if vfs, err := sc.StatVFS(path.Dir(full)); err == nil {
			if free := int64(vfs.Bavail) * int64(vfs.Frsize); size > free {
				return errNoRoom(size, free, "the server")
			}
		}
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	} else {
		had, err := partsThere(sc, tmp)
		if err != nil {
			return s.filePartError(err, server)
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
		return s.filePartError(err, server)
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
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			_ = sc.Truncate(tmp, offset)
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "A part can be at most 64 MB."}
		case body.err != nil:
			_ = sc.Truncate(tmp, offset)
			return errBadRequest("The part did not arrive whole: %v.", body.err)
		default:
			// Said before what was written is cut off, which would make
			// room again.
			said := s.filePartError(notPlaced(sc, err, full), server)
			_ = sc.Truncate(tmp, offset)
			return said
		}
	}
	if offset+n == size {
		if err := s.placeFileParts(r, u, a, server, sc, tmp, full, key, size); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, uploaded{Received: offset + n})
	return nil
}

// placeFileParts moves a file sent in parts, all of it there, into place,
// and remembers that it arrived.
func (s *Server) placeFileParts(r *http.Request, u *store.User, a *access, server string, sc *sftp.Client, tmp, full, key string, size int64) error {
	if err := sc.PosixRename(tmp, full); err != nil {
		return s.filePartError(notPlaced(sc, err, full), server)
	}
	keptSentWhole(key, size)
	s.audit(r.Context(), u, a.Org, "files.write", server, relPath(full))
	return nil
}

// dropStaleParts removes from a folder what was sent of files whose senders
// went away without saying so, as a closed page does: hidden from the list of
// files, they would otherwise take up its room for good.
func dropStaleParts(sc *sftp.Client, dir string) {
	entries, err := sc.ReadDir(dir)
	if err != nil {
		return
	}
	for _, fi := range entries {
		if strings.Contains(fi.Name(), ".avsys-upload-") && time.Since(fi.ModTime()) > stagedIdle {
			_ = sc.Remove(path.Join(dir, fi.Name()))
		}
	}
}

// discardFileParts removes what was sent of a file given up on.
func (s *Server) discardFileParts(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, _, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	name := r.URL.Query().Get("upload")
	if !uploadName.MatchString(name) {
		return errBadRequest("Say which upload this is, as ?upload=.")
	}
	if err := sc.Remove(fileUpload(full, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fileError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// fileReceived says how much of a file sent in parts to a server's files is
// there.
func (s *Server) fileReceived(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, server, sc, full, release, err := s.writableFiles(r, u)
	if err != nil {
		return err
	}
	defer release()
	return s.filePartsReceived(w, r, u, a, server, sc, full)
}

// filePartsReceived says how much of the file ?upload= names has arrived.
// Asked with ?size=, the whole of it, one all written but not yet moved into
// place, as when the files went out of reach just after its last part, is
// moved now: said to be all there, it would otherwise be left under its
// upload's name.
func (s *Server) filePartsReceived(w http.ResponseWriter, r *http.Request, u *store.User, a *access, server string, sc *sftp.Client, full string) error {
	q := r.URL.Query()
	name := q.Get("upload")
	if !uploadName.MatchString(name) {
		return errBadRequest("Say which upload this is, as ?upload=.")
	}
	tmp := fileUpload(full, name)
	key := a.Namespace + "/" + server + "/" + tmp
	lock, _ := stagedLocks.LoadOrStore(key, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	got, err := fileSent(key, sc, tmp)
	if err != nil {
		return s.filePartError(err, server)
	}
	if _, whole := sentWhole.Load(key); !whole {
		if size, err := strconv.ParseInt(q.Get("size"), 10, 64); err == nil && size > 0 && got == size {
			if err := s.placeFileParts(r, u, a, server, sc, tmp, full, key, size); err != nil {
				return err
			}
		}
	}
	writeJSON(w, http.StatusOK, uploaded{Received: got})
	return nil
}
