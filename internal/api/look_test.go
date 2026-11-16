package api

import (
	"testing"
)

// lookSays is how the panel looks to someone, as /me says.
func lookSays(t *testing.T, p *panel, who string) map[string]any {
	t.Helper()
	code, me := p.get(who, "GET", "/me", "")
	if code != 200 {
		t.Fatalf("/me as %s: %d", who, code)
	}
	look, _ := me["look"].(map[string]any)
	return look
}

// Each person picks how the panel looks to them: the theme follows the
// system until they pick one, and the accent is the panel's. Picking one
// leaves the other as it is, and nobody else's changes.
func TestEachPicksTheirLook(t *testing.T) {
	p := newPanel(t)
	if look := lookSays(t, p, "viewer"); look["theme"] != "auto" || look["accent"] != "" {
		t.Fatalf("before picking: %v", look)
	}

	if code, got := p.get("viewer", "PATCH", "/me/look", `{"theme":"light"}`); code != 200 || got["theme"] != "light" || got["accent"] != "" {
		t.Fatalf("picking a theme: %d %v", code, got)
	}
	if code, got := p.get("viewer", "PATCH", "/me/look", `{"accent":"gold"}`); code != 200 || got["theme"] != "light" || got["accent"] != "gold" {
		t.Fatalf("picking an accent: %d %v", code, got)
	}
	if look := lookSays(t, p, "viewer"); look["theme"] != "light" || look["accent"] != "gold" {
		t.Errorf("after picking: %v", look)
	}
	if look := lookSays(t, p, "operator"); look["theme"] != "auto" || look["accent"] != "" {
		t.Errorf("someone else's: %v", look)
	}

	// Back to the panel's accent and the system's theme.
	if code, got := p.get("viewer", "PATCH", "/me/look", `{"theme":"auto","accent":""}`); code != 200 || got["theme"] != "auto" || got["accent"] != "" {
		t.Errorf("back to the defaults: %d %v", code, got)
	}

	for _, body := range []string{`{}`, `{"theme":"sepia"}`, `{"theme":""}`, `{"accent":"green"}`, `{"accent":"Gold"}`} {
		if code, _ := p.do("viewer", "PATCH", "/me/look", body); code != 400 {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
	if look := lookSays(t, p, "viewer"); look["theme"] != "auto" || look["accent"] != "" {
		t.Errorf("changed by what was turned down: %v", look)
	}
}

// The panel's accent is that of everyone who has not picked their own, and
// of the sign-in pages. Only the instance administrator sets it.
func TestThePanelsAccent(t *testing.T) {
	p := newPanel(t)
	meta := func() any {
		_, got := p.get("viewer", "GET", "/meta", "")
		return got["accent"]
	}
	if got := meta(); got != "amethyst" {
		t.Fatalf("before it is set: %v", got)
	}
	if v := panelSays(t, p, "root"); v["accent"] != "amethyst" {
		t.Errorf("the panel says %v", v["accent"])
	}

	for _, who := range []string{"owner", "admin", "viewer"} {
		if code, _ := p.do(who, "PATCH", "/panel", `{"accent":"teal"}`); code != 403 {
			t.Errorf("%s setting the accent: %d, want 403", who, code)
		}
	}
	for _, body := range []string{`{"accent":""}`, `{"accent":"green"}`, `{}`} {
		if code, _ := p.do("root", "PATCH", "/panel", body); code != 400 {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
	if got := meta(); got != "amethyst" {
		t.Errorf("after what was turned down: %v", got)
	}

	if code, got := p.get("root", "PATCH", "/panel", `{"accent":"teal"}`); code != 200 || got["accent"] != "teal" || got["autoUpdate"] != false {
		t.Fatalf("setting it: %d %v", code, got)
	}
	if got := meta(); got != "teal" {
		t.Errorf("/meta after: %v", got)
	}
	if v := panelSays(t, p, "root"); v["accent"] != "teal" {
		t.Errorf("the panel after: %v", v["accent"])
	}
	// Someone with no accent of their own is still told "", and goes by the
	// panel's: one picked later is theirs, whatever the panel's.
	if look := lookSays(t, p, "viewer"); look["accent"] != "" {
		t.Errorf("someone without one: %v", look)
	}
	// Updates are left as they were.
	if code, got := p.get("root", "PATCH", "/panel", `{"autoUpdate":true}`); code != 200 || got["accent"] != "teal" || got["autoUpdate"] != true {
		t.Errorf("setting updates after: %d %v", code, got)
	}
}
