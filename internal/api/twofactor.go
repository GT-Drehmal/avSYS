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

// Who has to use two-factor is the setting store.SettingTwoFactorLevel: one
// of these, each the lowest role, in any organization, of who has to. The
// panel's administrator has to whenever it is not nobody. It holds for every
// way of signing in: a password, and every provider.
var twoFactorLevels = map[string]store.Role{
	store.TwoFactorNobody:    "",
	store.TwoFactorAdmins:    store.RoleAdmin,
	store.TwoFactorOperators: store.RoleOperator,
	store.TwoFactorEveryone:  store.RoleViewer,
}

var (
	errTwoFactorRequired = &Error{Status: http.StatusForbidden, Code: "two_factor_required", Message: "Set up two-factor to continue."}
	errSignInAgain       = &Error{Status: http.StatusUnauthorized, Code: "sign_in_again", Message: "Sign in again."}
	errWrongCode         = &Error{Status: http.StatusBadRequest, Code: "wrong_code", Message: "Wrong code. Try again."}
	// errSignInFirst is for someone with no password to give, who proves it
	// is them by having signed in a moment ago: see confirmIdentity.
	errSignInFirst = &Error{Status: http.StatusForbidden, Code: "recent_sign_in", Message: "Sign in again to do this."}
)

// recentSignIn is how long after signing in someone with no password may
// set up two-factor, turn it off or make new recovery codes: in place of
// the password, the sign-in itself, just done.
var recentSignIn = 10 * time.Minute

// twoFactorSetupRoutes are those someone who has to set up two-factor may
// use before they have: enough to do that.
var twoFactorSetupRoutes = map[string]bool{}

func init() {
	for _, route := range []string{"GET /me", "POST /me/two-factor/setup", "POST /me/two-factor"} {
		method, path, _ := strings.Cut(route, " ")
		twoFactorSetupRoutes[method+" /api/v1"+path] = true
	}
}

// twoFactorLevel is who has to use two-factor: one of twoFactorLevels. A
// value that is not one is taken as admins and owners.
func (s *Server) twoFactorLevel(ctx context.Context) (string, error) {
	v, err := s.Store.Setting(ctx, store.SettingTwoFactorLevel)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, ok := twoFactorLevels[v]; !ok {
		return store.TwoFactorAdmins, nil
	}
	return v, nil
}

// twoFactorRequired reports whether someone has to use two-factor: the
// panel's administrator, unless nobody has to, and anyone whose highest
// role in any organization is at least the level's.
func (s *Server) twoFactorRequired(ctx context.Context, u *store.User) (bool, error) {
	level, err := s.twoFactorLevel(ctx)
	if err != nil || level == store.TwoFactorNobody {
		return false, err
	}
	if u.IsAdmin || level == store.TwoFactorEveryone {
		return true, nil
	}
	role, err := s.Store.HighestRole(ctx, u.ID)
	if err != nil {
		return false, err
	}
	return role != "" && role.AtLeast(twoFactorLevels[level]), nil
}

// needsTwoFactor reports whether someone has to use two-factor and has not
// set it up.
func (s *Server) needsTwoFactor(ctx context.Context, u *store.User) (bool, error) {
	required, err := s.twoFactorRequired(ctx, u)
	if err != nil || !required {
		return false, err
	}
	on, err := s.Store.HasTwoFactor(ctx, u.ID)
	return !on, err
}

// mustSetUpTwoFactor reports whether a browser session is of someone who
// has to set up two-factor before anything else: however they signed in,
// and whenever they came to have to, as by being made an admin. API tokens
// are not held to it: they act as their owner, who could make them only
// with two-factor set up, if they had to have it then.
func (s *Server) mustSetUpTwoFactor(ctx context.Context, u *store.User, session *store.Session) (bool, error) {
	if session == nil {
		return false, nil
	}
	return s.needsTwoFactor(ctx, u)
}

// confirmIdentity is what someone signed in gives to change their
// two-factor: their password, or, for someone with none (who signs in only
// through a provider, or where passwords are off), a sign-in of this
// session in the last few minutes, through the provider and with a code if
// two-factor is on. A session taken over later, as from a browser left
// open, cannot do it; signing in again can.
func (s *Server) confirmIdentity(r *http.Request, u *store.User, password string) error {
	if u.PasswordHash != "" && !s.Config.NoPasswords {
		return s.checkCurrentPassword(r, u, password)
	}
	_, session, err := s.currentSession(r)
	if err != nil {
		return err
	}
	if !recentlySignedIn(session) {
		return errSignInFirst
	}
	return nil
}

func recentlySignedIn(session *store.Session) bool {
	return session != nil && time.Since(session.CreatedAt) < recentSignIn
}

// A sign-in waiting for its code: the password was right, or the provider
// said who it is, and the account has two-factor. It is held in memory,
// under a cookie, for a few minutes. One of a password is held with the
// password hash it was given for: a password changed or reset since ends
// it, so that the old password cannot sign in with a code.
type signInChallenge struct {
	userID string
	// method is how they signed in: one of the store.SignedInWith values.
	method       string
	passwordHash string
	// invite is one the sign-in came with, taken once the code is given.
	invite  string
	expires time.Time
	tries   int
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

func (c *signInChallenges) open(u *store.User, method, invite string) string {
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
	ch := &signInChallenge{userID: u.ID, method: method, invite: invite, expires: time.Now().Add(challengeTTL)}
	if method == store.SignedInWithPassword {
		ch.passwordHash = u.PasswordHash
	}
	c.m[token] = ch
	return token
}

// get returns a sign-in waiting for its code, or nil for none.
func (c *signInChallenges) get(token string) *signInChallenge {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.m[token]
	if ch == nil || time.Now().After(ch.expires) {
		delete(c.m, token)
		return nil
	}
	copied := *ch
	return &copied
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

// setChallengeCookie keeps a sign-in waiting for its code. Lax, not
// Strict: a sign-in through a provider sets it at the end of a navigation
// from the provider's site. It is only ever read by a POST, which another
// site cannot make with it.
func (s *Server) setChallengeCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: challengeCookie, Value: token, Path: "/api/v1/auth", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
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
		s.setChallengeCookie(w, challenges.open(u, store.SignedInWithPassword, ""), int(challengeTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{"twoFactor": true})
		return nil
	}
	if err := s.openSession(w, r, u.ID, store.SignedInWithPassword); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

// providerSignedIn goes on from a sign-in through a provider, on its
// callback: for an account with two-factor, to the sign-in page for the
// code; otherwise straight in, to next. An invite it came with is taken
// once they are in.
func (s *Server) providerSignedIn(w http.ResponseWriter, r *http.Request, u *store.User, invite, next string) error {
	tf, err := s.Store.TwoFactor(r.Context(), u.ID)
	if err != nil {
		return err
	}
	if tf.Secret != "" {
		s.setChallengeCookie(w, challenges.open(u, store.SignedInWithProvider, invite), int(challengeTTL.Seconds()))
		http.Redirect(w, r, "/login?"+url.Values{"step": {"code"}, "next": {next}}.Encode(), http.StatusFound)
		return nil
	}
	s.acceptInviteAtSignIn(r, invite, u.ID)
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	http.Redirect(w, r, next, http.StatusFound)
	return nil
}

// acceptInviteAtSignIn takes an invite someone who has an account signed
// in with.
func (s *Server) acceptInviteAtSignIn(r *http.Request, invite, userID string) {
	if invite == "" {
		return
	}
	if org, err := s.Store.AcceptInvite(r.Context(), invite, userID); err != nil {
		s.Log.Warn("accepting invite at sign-in", "error", err)
	} else {
		// Someone who already has an account, and may have a player
		// linked to it, has just joined.
		s.syncOpsSoon(org)
	}
}

// codeTime is the time two-factor codes are checked at: see TwoFactorClock.
func (s *Server) codeTime() time.Time {
	if s.TwoFactorClock != nil {
		return s.TwoFactorClock()
	}
	return time.Now()
}

// useCode takes a code from the app, or a recovery code, for a user with
// two-factor on: each is taken once.
func (s *Server) useCode(ctx context.Context, userID, secret, code string) (bool, error) {
	if secret == "" {
		return false, nil
	}
	if step, ok := auth.TOTPStep(secret, code, s.codeTime()); ok {
		return s.Store.UseTwoFactorStep(ctx, userID, step)
	}
	if rc := auth.RecoveryCode(code); rc != "" {
		return s.Store.UseRecoveryCode(ctx, userID, rc)
	}
	return false, nil
}

// signInCode is the second step of signing in, with a password or through
// a provider, for an account with two-factor: the code.
func (s *Server) signInCode(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		return err
	}
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		return errSignInAgain
	}
	ch := challenges.get(c.Value)
	if ch == nil {
		s.setChallengeCookie(w, "", -1)
		return errSignInAgain
	}
	if ch.method == store.SignedInWithPassword && s.Config.NoPasswords {
		return errPasswordsOff
	}
	// Guesses are limited by account, from wherever they come.
	key := "code|" + ch.userID
	if logins.blocked(key) {
		return errRateLimited
	}
	u, err := s.Store.User(r.Context(), ch.userID)
	if err != nil {
		return err
	}
	tf, err := s.Store.TwoFactor(r.Context(), ch.userID)
	if err != nil {
		return err
	}
	if tf.Secret == "" || ch.method == store.SignedInWithPassword && u.PasswordHash != ch.passwordHash {
		// Two-factor turned off, or the password changed, since the
		// sign-in began: start again.
		challenges.close(c.Value)
		s.setChallengeCookie(w, "", -1)
		return errSignInAgain
	}
	ok, err := s.useCode(r.Context(), ch.userID, tf.Secret, in.Code)
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
	s.acceptInviteAtSignIn(r, ch.invite, u.ID)
	if err := s.openSession(w, r, u.ID, ch.method); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

// setUpTwoFactor starts turning two-factor on: a new secret, shown as a QR
// code and as text, kept until a code from it confirms it. It takes the
// password, or for someone with none a recent sign-in: see confirmIdentity.
func (s *Server) setUpTwoFactor(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.confirmIdentity(r, u, in.Password); err != nil {
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
	step, ok := auth.TOTPStep(pending, in.Code, s.codeTime())
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

// disableTwoFactor turns two-factor off, with the password or a recent
// sign-in, for someone who need not have it.
func (s *Server) disableTwoFactor(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.confirmIdentity(r, u, in.Password); err != nil {
		return err
	}
	if required, err := s.twoFactorRequired(r.Context(), u); err != nil {
		return err
	} else if required {
		return &Error{Status: http.StatusConflict, Code: "required", Message: "You need two-factor here."}
	}
	if err := s.Store.DisableTwoFactor(r.Context(), u.ID); err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "account.two-factor-off", u.Username, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// newRecoveryCodes replaces the recovery codes, with the password or a
// recent sign-in.
func (s *Server) newRecoveryCodes(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.confirmIdentity(r, u, in.Password); err != nil {
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

// putTwoFactorLevel sets who has to use two-factor. Only the panel's
// administrator may.
func (s *Server) putTwoFactorLevel(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the panel's administrator can change this."}
	}
	var in struct{ Level string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if _, ok := twoFactorLevels[in.Level]; !ok {
		return errBadRequest("Choose nobody, admins, operators or everyone.")
	}
	if err := s.Store.SetSetting(r.Context(), store.SettingTwoFactorLevel, in.Level); err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "panel.two-factor", "", in.Level)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// twoFactorOfMe adds to what /me says of someone their two-factor: whether
// it is on, how many recovery codes are left, whether they have to have
// it, and set it up before going on, who has to here, and whether this
// session was signed in a moment ago (see confirmIdentity).
func (s *Server) twoFactorOfMe(r *http.Request, u *store.User, me map[string]any) error {
	tf, err := s.Store.TwoFactor(r.Context(), u.ID)
	if err != nil {
		return err
	}
	_, session, err := s.currentSession(r)
	if err != nil {
		return err
	}
	required, err := s.twoFactorRequired(r.Context(), u)
	if err != nil {
		return err
	}
	level, err := s.twoFactorLevel(r.Context())
	if err != nil {
		return err
	}
	me["twoFactor"] = tf.Secret != ""
	me["recoveryCodes"] = tf.RecoveryCodes
	me["twoFactorRequired"] = required
	me["mustSetUpTwoFactor"] = session != nil && required && tf.Secret == ""
	me["twoFactorLevel"] = level
	me["recentSignIn"] = recentlySignedIn(session)
	return nil
}
