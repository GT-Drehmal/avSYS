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

// An image is kept by the browser only when asked for by the version it is
// now: changed, a new version is asked for, and one not asked for by its
// version is always looked at again.
func TestImagesKeptByVersion(t *testing.T) {
	p := newPanel(t)
	sc := filesFrom(t, p, sftp.InMemHandler())
	file := dataRoot + "/server-icon.png"
	put(t, sc, file, "\x89PNG\r\n\x1a\nicon")
	fi, err := sc.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	now := fileVersion(fi)
	kept := func(query string) string {
		t.Helper()
		return fileRead(t, p, query).Header().Get("Cache-Control")
	}
	if got := kept("path=/server-icon.png&view&v=" + now); !strings.Contains(got, "immutable") || !strings.Contains(got, "private") {
		t.Errorf("by the version it is: Cache-Control %q", got)
	}
	if got := kept("path=/server-icon.png&view"); got != "no-cache" {
		t.Errorf("by no version: Cache-Control %q", got)
	}
	if got := kept("path=/server-icon.png&view&v=1-1"); got != "no-cache" {
		t.Errorf("by another version: Cache-Control %q", got)
	}
	if got := kept("path=/server-icon.png&v=" + now); got != "no-cache" {
		t.Errorf("read, not looked at: Cache-Control %q", got)
	}
	// Bigger, the same second: another version.
	put(t, sc, file, "\x89PNG\r\n\x1a\nicon, changed")
	if got := kept("path=/server-icon.png&view&v=" + now); got != "no-cache" {
		t.Errorf("changed since: Cache-Control %q", got)
	}
	if got := fileRead(t, p, "path=/server-icon.png&view&v="+now).Body.String(); got != "\x89PNG\r\n\x1a\nicon, changed" {
		t.Errorf("changed since, served %q", got)
	}
}
