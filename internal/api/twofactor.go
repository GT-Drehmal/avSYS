package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// settingAdminTwoFactor, "1", has every instance administrator who signs in
// with a password use two-factor.
const settingAdminTwoFactor = "signin.admin-two-factor"

var (
	errTwoFactorRequired = &Error{Status: http.StatusForbidden, Code: "two_factor_required", Message: "Set up two-factor to continue."}
	errSignInAgain       = &Error{Status: http.StatusUnauthorized, Code: "sign_in_again", Message: "Sign in again."}
	errWrongCode         = &Error{Status: http.StatusBadRequest, Code: "wrong_code", Message: "Wrong code. Try again."}
)

// twoFactorSetupRoutes are those an administrator who has to set up
// two-factor may use before they have: enough to do that.
var twoFactorSetupRoutes = map[string]bool{}

func init() {
	for _, route := range []string{"GET /me", "POST /me/two-factor/setup", "POST /me/two-factor"} {
		method, path, _ := strings.Cut(route, " ")
		twoFactorSetupRoutes[method+" /api/v1"+path] = true
	}
}

func (s *Server) adminTwoFactor(ctx context.Context) (bool, error) {
	v, err := s.Store.Setting(ctx, settingAdminTwoFactor)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return v == "1", err
}

// mustSetUpTwoFactor reports whether a session is of an administrator who
// has to set up two-factor before anything else: one who signed in with a
// password, where administrators are to have two-factor, and has not. Who
// signs in through a provider is not asked: the provider sees to it.
func (s *Server) mustSetUpTwoFactor(ctx context.Context, u *store.User, session *store.Session) (bool, error) {
	if session == nil || !u.IsAdmin || u.PasswordHash == "" || session.Method == store.SignedInWithProvider {
		return false, nil
	}
	if required, err := s.adminTwoFactor(ctx); err != nil || !required {
		return false, err
	}
	tf, err := s.Store.TwoFactor(ctx, u.ID)
	if err != nil {
		return false, err
	}
	return tf.Secret == "", nil
}

// A sign-in waiting for its code: the password was right, and the account
// has two-factor. It is held in memory, under a cookie, for a few minutes,
// with the password hash it was given for: a password changed or reset
// since ends it, so that the old password cannot sign in with a code.
type signInChallenge struct {
	userID       string
	passwordHash string
	expires      time.Time
	tries        int
}

type signInChallenges struct {
	mu sync.Mutex
	m  map[string]*signInChallenge
}

const (
	challengeCookie = "avsys_2fa"
	challengeTTL    = 5 * time.Minute
	// challengeTries is how many wrong codes one sign-in may have before
	// the password is asked for again.
	challengeTries = 5
)

var challenges signInChallenges

func (c *signInChallenges) open(u *store.User) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*signInChallenge{}
	}
	for token, ch := range c.m {
		if time.Now().After(ch.expires) {
			delete(c.m, token)
		}
	}
	token := store.NewID(32)
	c.m[token] = &signInChallenge{userID: u.ID, passwordHash: u.PasswordHash, expires: time.Now().Add(challengeTTL)}
	return token
}

// get returns the user a sign-in waiting for its code is of, and the
// password hash it was given for; or "" for none.
func (c *signInChallenges) get(token string) (userID, passwordHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.m[token]
	if ch == nil || time.Now().After(ch.expires) {
		delete(c.m, token)
		return "", ""
	}
	return ch.userID, ch.passwordHash
}

// fail counts a wrong code, and reports whether the sign-in may go on.
func (c *signInChallenges) fail(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.m[token]
	if ch == nil {
		return false
	}
	ch.tries++
	if ch.tries >= challengeTries {
		delete(c.m, token)
		return false
	}
	return true
}

// close ends a sign-in waiting for its code, and reports whether it was
// still waiting: of two codes sent at once, one goes on.
func (c *signInChallenges) close(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[token]
	delete(c.m, token)
	return ok
}

func (s *Server) setChallengeCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: challengeCookie, Value: token, Path: "/api/v1/auth", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteStrictMode,
	})
}

// passwordSignedIn goes on from a right password: to the code, for an
// account with two-factor, or straight in.
func (s *Server) passwordSignedIn(w http.ResponseWriter, r *http.Request, u *store.User) error {
	tf, err := s.Store.TwoFactor(r.Context(), u.ID)
	if err != nil {
		return err
	}
	if tf.Secret != "" {
		s.setChallengeCookie(w, challenges.open(u), int(challengeTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{"twoFactor": true})
		return nil
	}
	if err := s.openSession(w, r, u.ID, store.SignedInWithPassword); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

// useCode takes a code from the app, or a recovery code, for a user with
// two-factor on: each is taken once.
func (s *Server) useCode(ctx context.Context, userID, secret, code string) (bool, error) {
	if secret == "" {
		return false, nil
	}
	if step, ok := auth.TOTPStep(secret, code, time.Now()); ok {
		return s.Store.UseTwoFactorStep(ctx, userID, step)
	}
	if rc := auth.RecoveryCode(code); rc != "" {
		return s.Store.UseRecoveryCode(ctx, userID, rc)
	}
	return false, nil
}

// signInCode is the second step of signing in with a password, for an
// account with two-factor: the code.
func (s *Server) signInCode(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		return err
	}
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		return errSignInAgain
	}
	userID, passwordHash := challenges.get(c.Value)
	if userID == "" {
		s.setChallengeCookie(w, "", -1)
		return errSignInAgain
	}
	// Guesses are limited by account, from wherever they come.
	key := "code|" + userID
	if logins.blocked(key) {
		return errRateLimited
	}
	u, err := s.Store.User(r.Context(), userID)
	if err != nil {
		return err
	}
	tf, err := s.Store.TwoFactor(r.Context(), userID)
	if err != nil {
		return err
	}
	if tf.Secret == "" || u.PasswordHash != passwordHash {
		// Two-factor turned off, or the password changed, since the
		// password was given: start again.
		challenges.close(c.Value)
		s.setChallengeCookie(w, "", -1)
		return errSignInAgain
	}
	ok, err := s.useCode(r.Context(), userID, tf.Secret, in.Code)
	if err != nil {
		return err
	}
	if !ok {
		logins.fail(key)
		if !challenges.fail(c.Value) {
			s.setChallengeCookie(w, "", -1)
			return errSignInAgain
		}
		return errWrongCode
	}
	if !challenges.close(c.Value) {
		return errSignInAgain
	}
	s.setChallengeCookie(w, "", -1)
	if err := s.openSession(w, r, u.ID, store.SignedInWithPassword); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

// setUpTwoFactor starts turning two-factor on: a new secret, shown as a QR
// code and as text, kept until a code from it confirms it.
func (s *Server) setUpTwoFactor(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if u.PasswordHash == "" {
		return &Error{Status: http.StatusBadRequest, Code: "no_password", Message: "Set a password first."}
	}
	if err := s.checkCurrentPassword(r, u, in.Password); err != nil {
		return err
	}
	tf, err := s.Store.TwoFactor(r.Context(), u.ID)
	if err != nil {
		return err
	}
	if tf.Secret != "" {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: "Two-factor is already on."}
	}
	account := u.Username
	if pub, err := url.Parse(s.Config.PublicURL); err == nil && pub.Hostname() != "" {
		account += "@" + pub.Hostname()
	}
	key, err := auth.NewTOTPKey("avSYS", account)
	if err != nil {
		return err
	}
	if err := s.Store.SetPendingTwoFactor(r.Context(), u.ID, key.Secret); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": key.Secret, "url": key.URL, "qr": key.QR})
	return nil
}

// enableTwoFactor confirms the secret being set up with a code from it, and
// gives the recovery codes, shown this once.
func (s *Server) enableTwoFactor(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		return err
	}
	pending, err := s.Store.PendingTwoFactor(r.Context(), u.ID)
	if errors.Is(err, store.ErrNotFound) {
		return &Error{Status: http.StatusConflict, Code: "no_setup", Message: "Start again."}
	} else if err != nil {
		return err
	}
	key := "code|" + u.ID
	if logins.blocked(key) {
		return errRateLimited
	}
	step, ok := auth.TOTPStep(pending, in.Code, time.Now())
	if !ok {
		logins.fail(key)
		return errWrongCode
	}
	codes := auth.NewRecoveryCodes(10)
	if err := s.Store.EnableTwoFactor(r.Context(), u.ID, pending, step, codes); errors.Is(err, store.ErrNotFound) {
		return &Error{Status: http.StatusConflict, Code: "no_setup", Message: "Start again."}
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "account.two-factor-on", u.Username, "")
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
	return nil
}

// disableTwoFactor turns two-factor off, with the password.
func (s *Server) disableTwoFactor(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.checkCurrentPassword(r, u, in.Password); err != nil {
		return err
	}
	if u.IsAdmin {
		if required, err := s.adminTwoFactor(r.Context()); err != nil {
			return err
		} else if required {
			return &Error{Status: http.StatusConflict, Code: "required", Message: "Administrators need two-factor here."}
		}
	}
	if err := s.Store.DisableTwoFactor(r.Context(), u.ID); err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "account.two-factor-off", u.Username, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// newRecoveryCodes replaces the recovery codes, with the password.
func (s *Server) newRecoveryCodes(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.checkCurrentPassword(r, u, in.Password); err != nil {
		return err
	}
	tf, err := s.Store.TwoFactor(r.Context(), u.ID)
	if err != nil {
		return err
	}
	if tf.Secret == "" {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: "Two-factor is off."}
	}
	codes := auth.NewRecoveryCodes(10)
	if err := s.Store.SetRecoveryCodes(r.Context(), u.ID, codes); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
	return nil
}

// putAdminTwoFactor sets whether instance administrators have to use
// two-factor. Only one of them may.
func (s *Server) putAdminTwoFactor(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the panel's administrator can change this."}
	}
	var in struct{ Required bool }
	if err := decode(r, &in); err != nil {
		return err
	}
	var err error
	if in.Required {
		err = s.Store.SetSetting(r.Context(), settingAdminTwoFactor, "1")
	} else {
		err = s.Store.DeleteSetting(r.Context(), settingAdminTwoFactor)
	}
	if err != nil {
		return err
	}
	detail := "off"
	if in.Required {
		detail = "on"
	}
	s.audit(r.Context(), u, nil, "panel.admin-two-factor", "", detail)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// twoFactorOfMe adds to what /me says of someone their two-factor: whether
// it is on, how many recovery codes are left, whether they have to set it
// up before going on, and whether administrators are to have it.
func (s *Server) twoFactorOfMe(r *http.Request, u *store.User, me map[string]any) error {
	tf, err := s.Store.TwoFactor(r.Context(), u.ID)
	if err != nil {
		return err
	}
	_, session, err := s.currentSession(r)
	if err != nil {
		return err
	}
	must, err := s.mustSetUpTwoFactor(r.Context(), u, session)
	if err != nil {
		return err
	}
	required, err := s.adminTwoFactor(r.Context())
	if err != nil {
		return err
	}
	me["twoFactor"] = tf.Secret != ""
	me["recoveryCodes"] = tf.RecoveryCodes
	me["mustSetUpTwoFactor"] = must
	me["adminTwoFactor"] = required
	return nil
}
