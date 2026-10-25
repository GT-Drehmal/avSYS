package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"sync"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An archive too large to send in one request, or over a connection that may
// drop, is sent in parts: each is written at the end of what came before it,
// in the server's own volume, and a part lost is sent again from where the
// panel says it got to. The import is then asked for with ?from=upload, and
// takes what was sent as its archive.

// importUpload is where the parts of an archive are put together.
const importUpload = dataRoot + "/.avsys/import.upload"

// maxPart is how large one part may be.
const maxPart = 64 << 20

// uploadLocks holds a lock per server, by namespace/name, so that two parts
// are never written at once.
var uploadLocks sync.Map

var errWorkspace = &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. Try again."}

type uploaded struct {
	Received int64 `json:"received"`
}

// importUploaded says how much of an archive has been sent.
func (s *Server) importUploaded(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.serverMaybeNew(r, u, false)
	if err != nil {
		return err
	}
	sc, err := s.workspaces.get(r.Context(), mc.Namespace, mc.Name)
	if err != nil {
		return errWorkspace
	}
	fi, err := sc.Stat(importUpload)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, uploaded{})
		return nil
	} else if err != nil {
		return fileError(err)
	}
	writeJSON(w, http.StatusOK, uploaded{Received: fi.Size()})
	return nil
}

// importUploadPart writes a part of an archive at ?offset=, which is where
// the last one ended; at 0 it starts again. With ?length=, a part that is not
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
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "The archive is larger than the server's storage. Give it more."}
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
	var had int64
	if fi, err := sc.Stat(importUpload); err == nil {
		had = fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fileError(err)
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
	}
	f, err := sc.OpenFile(importUpload, flags)
	if err != nil {
		return fileError(err)
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxPart))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && length >= 0 && n != length {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		// What of it was written is cut off again, so that it can be sent
		// again from where it began.
		_ = sc.Truncate(importUpload, offset)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "A part can be at most 64 MB."}
		}
		return errBadRequest("The part did not arrive whole: %v.", err)
	}
	writeJSON(w, http.StatusOK, uploaded{Received: offset + n})
	return nil
}

// importDiscardUpload removes what was sent of an archive.
func (s *Server) importDiscardUpload(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.serverMaybeNew(r, u, false)
	if err != nil {
		return err
	}
	sc, err := s.workspaces.get(r.Context(), mc.Namespace, mc.Name)
	if err != nil {
		return errWorkspace
	}
	if err := sc.Remove(importUpload); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fileError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
