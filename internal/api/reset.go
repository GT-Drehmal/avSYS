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
// be handed to them: as for someone who forgot theirs. It can also turn
// their two-factor off, as for a lost phone. Only an instance administrator
// may. The link is shown this once, and works once, for a day. With
// passwords off, it can only turn two-factor off.
func (s *Server) createResetLink(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the panel's administrator can do this."}
	}
	var in struct{ TwoFactorOff bool }
	if err := decode(r, &in); err != nil {
		return err
	}
	if s.Config.NoPasswords && !in.TwoFactorOff {
		return errPasswordsOff
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

// resetLink is the link of a token, and its user, as it can be used here:
// whether it sets a new password. One that turns two-factor off does not,
// for an account with no password or where passwords are off; it only
// turns two-factor off. One that would set a password where passwords are
// off cannot be used.
func (s *Server) resetLink(r *http.Request) (*store.User, *store.ResetLink, bool, error) {
	u, link, err := s.Store.ResetLinkOf(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, false, errResetLinkGone
	} else if err != nil {
		return nil, nil, false, err
	}
	newPassword := !link.TwoFactorOff || !s.Config.NoPasswords && u.PasswordHash != ""
	if newPassword && s.Config.NoPasswords {
		return nil, nil, false, errPasswordsOff
	}
	return u, link, newPassword, nil
}

// getResetLink says whose a reset link is, and what it does, for the page
// it opens.
func (s *Server) getResetLink(w http.ResponseWriter, r *http.Request) error {
	u, link, newPassword, err := s.resetLink(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": u.Username, "displayName": u.DisplayName, "newPassword": newPassword, "twoFactorOff": link.TwoFactorOff})
	return nil
}

// useResetLink sets a new password with a reset link, or only turns
// two-factor off with one that does no more. Every session of the account
// ends; the person then signs in again.
func (s *Server) useResetLink(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	// Looked at before the password is hashed, which takes a while: a link
	// that is not one costs nothing.
	_, _, newPassword, err := s.resetLink(r)
	if err != nil {
		return err
	}
	hash := ""
	if newPassword {
		if len(in.Password) < 8 {
			return errBadRequest("Use a password of at least 8 characters.")
		}
		if hash, err = auth.HashPassword(in.Password); err != nil {
			return err
		}
	}
	u, err := s.Store.UseResetLink(r.Context(), r.PathValue("token"), hash)
	if errors.Is(err, store.ErrNotFound) {
		return errResetLinkGone
	} else if err != nil {
		return err
	}
	if newPassword {
		s.audit(r.Context(), u, nil, "account.password-reset", u.Username, "")
	} else {
		s.audit(r.Context(), u, nil, "account.two-factor-reset", u.Username, "")
	}
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
