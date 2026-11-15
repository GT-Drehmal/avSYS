package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// resetLinkTTL is how long a reset link works for.
const resetLinkTTL = 24 * time.Hour

var errResetLinkGone = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "This link is no longer valid. Ask for a new one."}

// createResetLink makes a link for someone to set a new password with, to
// be handed to them: as for someone who forgot theirs. Only an instance
// administrator may. The link is shown this once, and works once, for a day.
func (s *Server) createResetLink(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the panel's administrator can do this."}
	}
	var in struct{ TwoFactorOff bool }
	if err := decode(r, &in); err != nil {
		return err
	}
	target, err := s.Store.User(r.Context(), r.PathValue("user"))
	if err != nil {
		return err
	}
	link := &store.ResetLink{UserID: target.ID, CreatedBy: u.ID, TwoFactorOff: in.TwoFactorOff}
	token, err := s.Store.CreateResetLink(r.Context(), link, resetLinkTTL)
	if err != nil {
		return err
	}
	detail := ""
	if in.TwoFactorOff {
		detail = "turning two-factor off"
	}
	s.audit(r.Context(), u, nil, "account.reset-link", target.Username, detail)
	writeJSON(w, http.StatusCreated, map[string]any{"url": s.Config.PublicURL + "/reset/" + token, "expiresAt": link.ExpiresAt})
	return nil
}

// getResetLink says whose a reset link is, for the page it opens.
func (s *Server) getResetLink(w http.ResponseWriter, r *http.Request) error {
	u, err := s.Store.ResetLinkUser(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		return errResetLinkGone
	} else if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "displayName": u.DisplayName})
	return nil
}

// useResetLink sets a new password with a reset link. Every session of the
// account ends; the person then signs in with it.
func (s *Server) useResetLink(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.Password) < 8 {
		return errBadRequest("Use a password of at least 8 characters.")
	}
	token := r.PathValue("token")
	// Looked at before the password is hashed, which takes a while: a link
	// that is not one costs nothing.
	if _, err := s.Store.ResetLinkUser(r.Context(), token); errors.Is(err, store.ErrNotFound) {
		return errResetLinkGone
	} else if err != nil {
		return err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	u, err := s.Store.UseResetLink(r.Context(), token, hash)
	if errors.Is(err, store.ErrNotFound) {
		return errResetLinkGone
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "account.password-reset", u.Username, "")
	// This browser may have been signed in as someone: no longer.
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Store.DeleteSession(r.Context(), c.Value); err != nil {
			return err
		}
		s.clearSessionCookie(w)
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
	return nil
}
