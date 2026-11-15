package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// fileRead reads one of world's files as an operator, with its headers.
func fileRead(t *testing.T, p *panel, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/files/content?"+query, nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["operator"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
	}
	return rec
}

// An image asked for to be looked at is said to be one, so the browser
// draws it; anything else, or one downloaded, is only bytes. None can run
// script on the panel's origin, even opened in a tab of its own.
func TestImagesShownNeverRun(t *testing.T) {
	p := newPanel(t)
	sc := filesFrom(t, p, sftp.InMemHandler())
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><script>alert(1)</script><rect width="8" height="8"/></svg>`
	put(t, sc, dataRoot+"/server-icon.png", "\x89PNG\r\n\x1a\nicon")
	put(t, sc, dataRoot+"/Logo.SVG", svg)
	put(t, sc, dataRoot+"/page.html", "<script>alert(1)</script>")
	put(t, sc, dataRoot+"/level.dat", "level")

	for _, c := range []struct {
		name, query, kind string
	}{
		{"a png to look at", "path=/server-icon.png&view", "image/png"},
		{"an svg to look at, whatever the case of its name", "path=/Logo.SVG&view", "image/svg+xml"},
		{"a png read", "path=/server-icon.png", "application/octet-stream"},
		{"a png downloaded", "path=/server-icon.png&view&download", "application/octet-stream"},
		{"a page to look at", "path=/page.html&view", "application/octet-stream"},
		{"a world's file to look at", "path=/level.dat&view", "application/octet-stream"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := fileRead(t, p, c.query).Header()
			if got := h.Get("Content-Type"); got != c.kind {
				t.Errorf("Content-Type %q, want %q", got, c.kind)
			}
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options %q", got)
			}
			csp := h.Get("Content-Security-Policy")
			for _, want := range []string{"sandbox", "default-src 'none'"} {
				if !strings.Contains(csp, want) {
					t.Errorf("Content-Security-Policy %q has no %s", csp, want)
				}
			}
			if strings.Contains(csp, "script-src") || strings.Contains(csp, "allow-scripts") {
				t.Errorf("Content-Security-Policy %q lets script run", csp)
			}
		})
	}
	if got := fileRead(t, p, "path=/Logo.SVG&view").Body.String(); got != svg {
		t.Errorf("the svg is served as %q", got)
	}
}

// An image looked at again is sent again only if what it holds has
// changed, even to the same size in the same second, and whatever its size.
func TestImagesKeptByWhatTheyHold(t *testing.T) {
	p := newPanel(t)
	sc := filesFrom(t, p, sftp.InMemHandler())
	file := dataRoot + "/server-icon.png"
	again := func(tag string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/files/content?path=/server-icon.png&view", nil)
		req.Header.Set("Authorization", "Bearer "+p.tokens["operator"])
		req.Header.Set("If-None-Match", tag)
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		return rec
	}
	for _, size := range []int{12, maxEditable + 10} {
		first := "\x89PNG" + strings.Repeat("a", size-4)
		put(t, sc, file, first)
		rec := fileRead(t, p, "path=/server-icon.png&view")
		tag := rec.Header().Get("ETag")
		if tag != fileTag([]byte(first)) {
			t.Fatalf("%d bytes: ETag %q, want the hash of what it holds", size, tag)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%d bytes: Cache-Control %q, want it looked at again each time", size, got)
		}
		if rec.Body.String() != first {
			t.Errorf("%d bytes: not served whole", size)
		}
		if got := again(tag); got.Code != http.StatusNotModified {
			t.Errorf("%d bytes, unchanged: %d, want %d", size, got.Code, http.StatusNotModified)
		}
		// The same size, at once: its time may be the same, but not its hash.
		changed := "\x89PNG" + strings.Repeat("b", size-4)
		put(t, sc, file, changed)
		got := again(tag)
		if got.Code != http.StatusOK || got.Body.String() != changed {
			t.Errorf("%d bytes, changed: %d, served the change: %v", size, got.Code, got.Body.String() == changed)
		}
	}
}
