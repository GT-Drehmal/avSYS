package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An archive too large to send in one request, or over a connection that may
// drop, is sent in parts: each is written at the end of what came before it,
// in the server's own volume, and a part lost is sent again from where the
// panel says it got to. The import is then asked for with ?from=upload, and
// takes what was sent as its archive.

// importUpload is where the parts of an archive are put together, and
// importUploadID says whose they are: each upload is named by the page that
// sends it, on every part, so that two sent at once are never mixed.
const (
	importUpload   = dataRoot + "/.avsys/import.upload"
	importUploadID = importUpload + ".id"
)

// uploadIdle is how long an upload may go without a part before another may
// take its place.
const uploadIdle = 2 * time.Minute

var (
	uploadName   = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	errUploading = &Error{Status: http.StatusConflict, Code: "uploading", Message: "Someone else is uploading to this server. Wait for that to finish."}
)

// uploadOf says whose parts are there, how much of them, and when the last
// came: "" and 0 when there are none.
func uploadOf(sc *sftp.Client) (name string, size int64, last time.Time, err error) {
	fi, err := sc.Stat(importUpload)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, time.Time{}, nil
	} else if err != nil {
		return "", 0, time.Time{}, err
	}
	f, err := sc.Open(importUploadID)
	if errors.Is(err, os.ErrNotExist) {
		return "", fi.Size(), fi.ModTime(), nil
	} else if err != nil {
		return "", 0, time.Time{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 128))
	return strings.TrimSpace(string(b)), fi.Size(), fi.ModTime(), err
}

// readCounted is a request body that keeps the error reading it gave, so
// that one cut short by whoever sent it is told apart from the server's
// files going out of reach.
type readCounted struct {
	r   io.Reader
	err error
}

func (b *readCounted) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		b.err = err
	}
	return n, err
}

// maxPart is how large one part may be.
const maxPart = 64 << 20

// uploadLocks holds a lock per server, by namespace/name, so that two parts
// are never written at once.
var uploadLocks sync.Map

var errWorkspace = &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. Try again."}

type uploaded struct {
	Received int64 `json:"received"`
}

// importUploaded says how much of the archive ?upload= names has been sent.
func (s *Server) importUploaded(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.serverMaybeNew(r, u, false)
	if err != nil {
		return err
	}
	sc, err := s.workspaces.get(r.Context(), mc.Namespace, mc.Name)
	if err != nil {
		return errWorkspace
	}
	name, size, _, err := uploadOf(sc)
	if err != nil {
		return fileError(err)
	}
	if name != r.URL.Query().Get("upload") {
		size = 0
	}
	writeJSON(w, http.StatusOK, uploaded{Received: size})
	return nil
}

// importUploadPart writes a part of the archive ?upload= names at ?offset=,
// which is where the last one ended; at 0 it starts again, in place of
// another's only once that has had no part for a while. With ?length=, a part that is not
// as long is taken as cut short. With ?wait=true, for a server made a moment
// ago, its volume is waited for.
func (s *Server) importUploadPart(w http.ResponseWriter, r *http.Request, u *store.User) error {
	wait := r.URL.Query().Get("wait") == "true"
	a, mc, err := s.serverMaybeNew(r, u, wait)
	if err != nil {
		return err
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		return errBadRequest("Say where this part goes, as ?offset=.")
	}
	name := r.URL.Query().Get("upload")
	if !uploadName.MatchString(name) {
		return errBadRequest("Say which upload this part is of, as ?upload=.")
	}
	length := int64(-1)
	if l := r.URL.Query().Get("length"); l != "" {
		if length, err = strconv.ParseInt(l, 10, 64); err != nil || length < 0 {
			return errBadRequest("Say how long this part is, as ?length=.")
		}
	}
	if mc.Spec.State != avsysv1.StateStopped {
		return &Error{Status: http.StatusConflict, Code: "running", Message: "Stop the server first."}
	}
	if running := s.ops.current(a.Namespace, mc.Name); running != nil {
		return heldBy(running.Kind)
	}
	// What is sent must fit in the server's storage, with room to unpack it.
	if room := mc.Spec.Storage.Size.Value(); room > 0 && offset+r.ContentLength > room {
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "The archive is larger than the server's storage. Give the server more."}
	}
	lock, _ := uploadLocks.LoadOrStore(a.Namespace+"/"+mc.Name, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	sc, done, err := s.filesOf(r.Context(), mc, wait && offset == 0)
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return errWorkspace
	}
	defer done()
	owner, had, last, err := uploadOf(sc)
	if err != nil {
		return fileError(err)
	}
	if owner != name {
		if offset != 0 {
			// Of an upload that is gone, or another's: begun again.
			had = 0
		} else if owner != "" && time.Since(last) < uploadIdle {
			return errUploading
		}
	}
	if offset != 0 && offset != had {
		writeJSON(w, http.StatusConflict, struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			Received int64  `json:"received"`
		}{"offset", "This part is not the next one.", had})
		return nil
	}
	if err := sc.MkdirAll(path.Dir(importUpload)); err != nil {
		return fileError(err)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if offset == 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		// Its name first: parts are only ever added to under it.
		if err := writeSmall(sc, importUploadID, name); err != nil {
			s.Log.Warn("could not start an upload", "server", mc.Name, "error", err)
			return errWorkspace
		}
	}
	f, err := sc.OpenFile(importUpload, flags)
	if err != nil {
		return fileError(err)
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
		// What of it was written is cut off again, so that it can be sent
		// again from where it began.
		_ = sc.Truncate(importUpload, offset)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "A part can be at most 64 MB."}
		case body.err != nil:
			return errBadRequest("The part did not arrive whole: %v.", body.err)
		default:
			// The server's files went out of reach: sent again once they
			// are back.
			s.Log.Warn("an upload's part could not be written", "server", mc.Name, "error", err)
			return errWorkspace
		}
	}
	writeJSON(w, http.StatusOK, uploaded{Received: offset + n})
	return nil
}

// importDiscardUpload removes what was sent of the archive ?upload= names.
func (s *Server) importDiscardUpload(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.serverMaybeNew(r, u, false)
	if err != nil {
		return err
	}
	sc, err := s.workspaces.get(r.Context(), mc.Namespace, mc.Name)
	if err != nil {
		return errWorkspace
	}
	if owner, _, _, err := uploadOf(sc); err != nil {
		return fileError(err)
	} else if owner != r.URL.Query().Get("upload") {
		return errUploading
	}
	for _, f := range []string{importUpload, importUploadID} {
		if err := sc.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fileError(err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// writeSmall writes a few bytes to a file, in place of what it held.
func writeSmall(sc *sftp.Client, file, text string) error {
	f, err := sc.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(text)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
