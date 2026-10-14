package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The page answers for any address the app might route to, but not for a
// script or stylesheet that is not there: that belongs to another build, and
// answering it with the page would hand a browser HTML where it asked for
// code.
func TestStaticFallsBackOnlyForPages(t *testing.T) {
	s := &Server{Static: fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>avSYS</title>")},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
		"logo.svg":            {Data: []byte("<svg/>")},
	}}
	h := s.staticHandler()
	for _, c := range []struct {
		path, cache, body string
		status            int
	}{
		{"/", "no-cache", "<!doctype html>", http.StatusOK},
		{"/o/friends/s/creative/terminal", "no-cache", "<!doctype html>", http.StatusOK},
		{"/assets/index-abc.js", "public, max-age=31536000, immutable", "console.log", http.StatusOK},
		{"/logo.svg", "no-cache", "<svg/>", http.StatusOK},
		{"/assets/Terminal-old.js", "", "", http.StatusNotFound},
		{"/assets/", "", "", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, w.Code, c.status)
			continue
		}
		if c.status != http.StatusOK {
			// Not to be kept at all: a browser that remembered the answer
			// would not ask again once the file is there.
			if cache := w.Header().Get("Cache-Control"); cache != "no-store" {
				t.Errorf("%s: a missing file may be kept: Cache-Control %q", c.path, cache)
			}
			continue
		}
		if got := w.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.cache)
		}
		if !strings.HasPrefix(w.Body.String(), c.body) {
			t.Errorf("%s: body %q, want it to start with %q", c.path, w.Body.String(), c.body)
		}
	}
}
