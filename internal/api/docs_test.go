package api

import (
	"os"
	"strings"
	"testing"
)

// TestEveryRouteIsDocumented keeps docs/api.md up with the routes: each one
// the panel serves has a heading there, written as the route is registered,
// as "### `GET /orgs/{org}/servers`".
func TestEveryRouteIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	headings := map[string]bool{}
	for line := range strings.SplitSeq(string(doc), "\n") {
		if route, ok := strings.CutPrefix(line, "### `"); ok {
			headings[strings.TrimSuffix(route, "`")] = true
		}
	}
	p := newEmptyPanel(t)
	for _, route := range p.srv.routes {
		if !headings[route] {
			t.Errorf("%s is served but docs/api.md has no heading for it", route)
		}
	}
}
