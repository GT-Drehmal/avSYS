package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

// Every answer of the API says how long it took, an error as much as one
// that went through; what is not the API says nothing.
func TestServerTiming(t *testing.T) {
	p := newPanel(t)
	timing := regexp.MustCompile(`^app;dur=\d+\.\d$`)
	for _, c := range []struct {
		path, who string
		want      bool
	}{
		{"/api/v1/meta", "", true},
		{"/api/v1/orgs/alpha/servers", "viewer", true},
		{"/api/v1/orgs/alpha/servers", "", true},
		{"/api/v1/nothing-here", "", true},
		{"/packs/nothing.zip", "", false},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		if c.who != "" {
			req.Header.Set("Authorization", "Bearer "+p.tokens[c.who])
		}
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		got := rec.Header().Values("Server-Timing")
		if c.want && (len(got) != 1 || !timing.MatchString(got[0])) {
			t.Errorf("%s as %q: %d %v", c.path, c.who, rec.Code, got)
		}
		if !c.want && len(got) != 0 {
			t.Errorf("%s: %v", c.path, got)
		}
	}
}

// What streams and websockets ask of the writer still reaches the one
// underneath.
func TestTimingWriterPassesThrough(t *testing.T) {
	var w http.ResponseWriter = &timingWriter{ResponseWriter: httptest.NewRecorder()}
	if _, ok := w.(http.Flusher); !ok {
		t.Error("not a Flusher")
	}
	if _, ok := w.(http.Hijacker); !ok {
		t.Error("not a Hijacker")
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Errorf("flush: %v", err)
	}
}
