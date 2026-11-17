package api

import (
	"net/http"
	"strings"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

var errRateLimited = &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Too many attempts. Wait a few minutes and try again."}

// sessionInfo is what a session made or used by r notes of it.
func (s *Server) sessionInfo(r *http.Request, method string) store.SessionInfo {
	ua := r.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	return store.SessionInfo{UserAgent: ua, Address: s.clientAddress(r), Method: method}
}

// sessionToken is the session token a request carries in its cookie, or "".
func sessionToken(r *http.Request) string {
	if bearerToken(r) != "" {
		return ""
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})
}

// checkCurrentPassword checks the password of someone signed in, as when
// they change it or turn two-factor on or off: guesses are limited as they
// are at sign-in. Someone with no password has none to give.
func (s *Server) checkCurrentPassword(r *http.Request, u *store.User, password string) error {
	if u.PasswordHash == "" {
		return nil
	}
	key := s.clientAddress(r) + "|" + strings.ToLower(u.Username)
	if logins.blocked(key) {
		return errRateLimited
	}
	if !auth.CheckPassword(u.PasswordHash, password) {
		logins.fail(key)
		return &Error{Status: http.StatusBadRequest, Code: "wrong_password", Message: "The current password is wrong."}
	}
	return nil
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, u *store.User) error {
	sessions, err := s.Store.Sessions(r.Context(), u.ID, sessionToken(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sessions)
	return nil
}

// endSession signs one of the caller's sessions out; their own, this one,
// signs them out here too.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, current, err := s.currentSession(r)
	if err != nil {
		return err
	}
	if err := s.Store.EndSession(r.Context(), u.ID, r.PathValue("id")); err != nil {
		return err
	}
	if current != nil && current.ID == r.PathValue("id") {
		s.clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// endOtherSessions signs the caller out everywhere but here.
func (s *Server) endOtherSessions(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.EndOtherSessions(r.Context(), u.ID, sessionToken(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
