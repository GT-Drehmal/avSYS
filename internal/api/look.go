package api

import (
	"context"
	"net/http"
	"slices"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// themes are the ways the panel can look: as the system does, or always
// light or dark.
var themes = []string{"auto", "light", "dark"}

// accents are the colours the panel can be picked out in, the first the one
// it has until its administrator picks another. web/src/app.css has each.
var accents = []string{"amethyst", "gold", "teal", "blue", "rose"}

// accentKey is the setting that holds the accent of the panel: the one of
// anyone who has not picked their own.
const accentKey = "panel.accent"

// panelAccent is the accent of anyone who has not picked their own.
func (s *Server) panelAccent(ctx context.Context) string {
	v, err := s.Store.Setting(ctx, accentKey)
	if err != nil || !slices.Contains(accents, v) {
		return accents[0]
	}
	return v
}

// setPanelAccent sets the accent of everyone who has not picked their own.
func (s *Server) setPanelAccent(ctx context.Context, u *store.User, accent string) error {
	if !slices.Contains(accents, accent) {
		return errBadRequest("There is no such accent.")
	}
	if err := s.Store.SetSetting(ctx, accentKey, accent); err != nil {
		return err
	}
	s.audit(ctx, u, nil, "panel.accent", accent, "")
	return nil
}

// setLook sets how the panel looks to whoever asks: its theme, its accent,
// or both. An accent of "" is the panel's.
func (s *Server) setLook(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct {
		Theme  *string `json:"theme"`
		Accent *string `json:"accent"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Theme == nil && in.Accent == nil {
		return errBadRequest("Say what to change.")
	}
	if in.Theme != nil && !slices.Contains(themes, *in.Theme) {
		return errBadRequest("There is no such theme.")
	}
	if in.Accent != nil && *in.Accent != "" && !slices.Contains(accents, *in.Accent) {
		return errBadRequest("There is no such accent.")
	}
	// Only what was given, so that a theme picked in one tab and an accent
	// in another at the same time are both kept.
	if err := s.Store.SetUserLook(r.Context(), u.ID, in.Theme, in.Accent); err != nil {
		return err
	}
	now, err := s.Store.User(r.Context(), u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, lookOf(now))
	return nil
}

// lookOf is how the panel looks to someone, as /me says.
func lookOf(u *store.User) map[string]string {
	theme := u.Theme
	if !slices.Contains(themes, theme) {
		theme = themes[0]
	}
	accent := u.Accent
	if !slices.Contains(accents, accent) {
		accent = ""
	}
	return map[string]string{"theme": theme, "accent": accent}
}
