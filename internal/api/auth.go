package api

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)

type providerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) getMeta(w http.ResponseWriter, r *http.Request) error {
	count, err := s.Store.CountUsers(r.Context())
	if err != nil {
		return err
	}
	providers := []providerInfo{}
	for _, id := range auth.ProviderIDs {
		if p := s.Config.Providers[id]; p != nil {
			providers = append(providers, providerInfo{ID: p.ID, Name: p.Name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       s.Config.Version,
		"setupRequired": count == 0,
		"allowSignups":  s.Config.AllowSignups,
		"providers":     providers,
		"ssh":           map[string]any{"host": s.Config.SSHHost, "port": s.Config.SSHPort},
		// Whether this panel can take snapshots at all.
		"snapshots": s.Backups.Available(),
	})
	return nil
}

// signup says on what grounds a new account may be made, or why it may not.
// With an invite, only on that, and only while it can still be used. Without
// one, when signups are open, or, on a panel with no accounts yet, as the
// first: whoever sets the panel up, and nobody after.
func (s *Server) signup(r *http.Request, invite string) (store.Signup, error) {
	if invite != "" {
		inv, err := s.Store.Invite(r.Context(), invite)
		if errors.Is(err, store.ErrNotFound) || err == nil && !inv.Usable() {
			return store.Signup{}, errInviteUnusable
		}
		return store.Signup{Invite: invite}, err
	}
	if s.Config.AllowSignups {
		return store.Signup{}, nil
	}
	count, err := s.Store.CountUsers(r.Context())
	if err != nil {
		return store.Signup{}, err
	}
	if count > 0 {
		return store.Signup{}, errSignupClosed
	}
	return store.Signup{FirstOnly: true}, nil
}

// signUp makes the account on the grounds signup gave, failing as it would
// have if what they were has changed since: another account was made
// first, or the invite was used up or revoked.
func (s *Server) signUp(r *http.Request, u *store.User, how store.Signup) error {
	org, err := s.Store.SignUp(r.Context(), u, how)
	switch {
	case errors.Is(err, store.ErrNotFirst):
		return errSignupClosed
	case how.Invite != "" && (errors.Is(err, store.ErrInviteUnusable) || errors.Is(err, store.ErrNotFound)):
		return errInviteUnusable
	case err != nil:
		return err
	}
	if org != nil {
		s.syncOpsSoon(org)
	}
	return nil
}

var (
	errSignupClosed   = &Error{Status: http.StatusForbidden, Code: "signup_closed", Message: "You need an invite link to create an account here."}
	errInviteUnusable = &Error{Status: http.StatusGone, Code: "invite_unusable", Message: "This invite is no longer valid. Ask for a new one."}
)

func (s *Server) register(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Username, Password, DisplayName, Invite string }
	if err := decode(r, &in); err != nil {
		return err
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if !usernamePattern.MatchString(in.Username) {
		return errBadRequest("Usernames are 2 to 32 characters: lowercase letters, digits, - and _.")
	}
	if len(in.Password) < 8 {
		return errBadRequest("Use a password of at least 8 characters.")
	}
	how, err := s.signup(r, in.Invite)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	u := &store.User{Username: in.Username, DisplayName: strings.TrimSpace(in.DisplayName), PasswordHash: hash}
	if err := s.signUp(r, u, how); err != nil {
		return err
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, u)
	return nil
}

// loginLimiter slows password guessing: a handful of failures per address and
// username, then a wait.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	loginWindow      = 10 * time.Minute
	loginMaxFailures = 8
)

func (l *loginLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.failures[key][:0]
	for _, t := range l.failures[key] {
		if time.Since(t) < loginWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = recent
	}
	return len(recent) >= loginMaxFailures
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[string][]time.Time{}
	}
	l.failures[key] = append(l.failures[key], time.Now())
}

var logins loginLimiter

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Username, Password string }
	if err := decode(r, &in); err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := host + "|" + strings.ToLower(in.Username)
	if logins.blocked(key) {
		return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Too many attempts. Wait a few minutes and try again."}
	}
	u, err := s.Store.UserByUsername(r.Context(), strings.TrimSpace(in.Username))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if u == nil || u.PasswordHash == "" || !auth.CheckPassword(u.PasswordHash, in.Password) {
		logins.fail(key)
		return &Error{Status: http.StatusUnauthorized, Code: "bad_credentials", Message: "Wrong username or password."}
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Store.DeleteSession(r.Context(), c.Value); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

const oauthCookie = "avsys_oauth"

// safeNext keeps post-login redirects on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) error {
	p := s.Config.Providers[r.PathValue("provider")]
	if p == nil {
		return errNotFound
	}
	state := store.NewID(16)
	q := r.URL.Query()
	value := url.Values{"state": {state}, "invite": {q.Get("invite")}, "next": {safeNext(q.Get("next"))}}.Encode()
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: url.QueryEscape(value), Path: "/api/v1/auth",
		MaxAge: 600, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthURL(state), http.StatusFound)
	return nil
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) error {
	// Failures here land the browser back on the sign-in page with a reason,
	// since there is no script around to read a JSON error.
	bounce := func(msg string) error {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
		return nil
	}
	p := s.Config.Providers[r.PathValue("provider")]
	if p == nil {
		return errNotFound
	}
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		return bounce("The sign-in attempt expired. Try again.")
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: "/api/v1/auth", MaxAge: -1})
	raw, _ := url.QueryUnescape(c.Value)
	saved, _ := url.ParseQuery(raw)
	if saved.Get("state") == "" || saved.Get("state") != r.URL.Query().Get("state") {
		return bounce("The sign-in attempt could not be verified. Try again.")
	}
	if r.URL.Query().Get("code") == "" {
		return bounce(p.Name + " sign-in was cancelled.")
	}
	profile, err := p.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.Log.Warn("oauth exchange failed", "provider", p.ID, "error", err)
		return bounce("Could not sign in with " + p.Name + ".")
	}
	invite, next := saved.Get("invite"), safeNext(saved.Get("next"))
	identity := store.Identity{Provider: p.ID, Subject: profile.Subject, Login: profile.Login}

	user, err := s.Store.UserByIdentity(r.Context(), p.ID, profile.Subject)
	switch {
	case err == nil:
	case !errors.Is(err, store.ErrNotFound):
		return err
	default:
		// An unknown provider account either gets linked to whoever is signed
		// in already, or becomes a new user.
		if current, err := s.currentUser(r); err == nil {
			identity.UserID = current.ID
			if err := s.Store.LinkIdentity(r.Context(), identity); err != nil {
				return err
			}
			http.Redirect(w, r, next, http.StatusFound)
			return nil
		}
		how, err := s.signup(r, invite)
		var refused *Error
		if errors.As(err, &refused) {
			return bounce(refused.Message)
		} else if err != nil {
			return err
		}
		user, err = s.createOAuthUser(r, profile, how)
		if errors.As(err, &refused) {
			return bounce(refused.Message)
		} else if err != nil {
			return err
		}
		identity.UserID = user.ID
		if err := s.Store.LinkIdentity(r.Context(), identity); err != nil {
			return err
		}
		// The invite went with the account.
		invite = ""
	}
	if invite != "" {
		if org, err := s.Store.AcceptInvite(r.Context(), invite, user.ID); err != nil {
			s.Log.Warn("accepting invite at sign-in", "error", err)
		} else {
			// Someone who already has an account, and may have a player
			// linked to it, has just joined.
			s.syncOpsSoon(org)
		}
	}
	if err := s.startSession(w, r, user.ID); err != nil {
		return err
	}
	http.Redirect(w, r, next, http.StatusFound)
	return nil
}

var nonUsernameChars = regexp.MustCompile(`[^a-z0-9_-]+`)

func (s *Server) createOAuthUser(r *http.Request, p auth.Profile, how store.Signup) (*store.User, error) {
	base := p.Login
	if base == "" {
		base, _, _ = strings.Cut(p.Email, "@")
	}
	base = strings.Trim(nonUsernameChars.ReplaceAllString(strings.ToLower(base), "-"), "-_")
	if len(base) > 28 {
		base = base[:28]
	}
	if len(base) < 2 {
		base = "user"
	}
	for attempt := 0; attempt < 20; attempt++ {
		username := base
		if attempt > 0 {
			username = fmt.Sprintf("%s%d", base, attempt+1)
		}
		u := &store.User{Username: username, DisplayName: p.Name, Email: p.Email, AvatarURL: p.AvatarURL}
		err := s.signUp(r, u, how)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
	}
	return nil, errors.New("could not find a free username")
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request, u *store.User) error {
	orgs, err := s.Store.OrgsForUser(r.Context(), u)
	if err != nil {
		return err
	}
	identities, err := s.Store.Identities(r.Context(), u.ID)
	if err != nil {
		return err
	}
	me := map[string]any{
		"user": u, "orgs": orgs, "identities": identities, "hasPassword": u.PasswordHash != "",
	}
	// Their main one, left out, not null, for someone who has linked none;
	// and all of them.
	accounts, err := s.Store.MinecraftAccounts(r.Context(), u.ID)
	if err != nil {
		return err
	}
	me["minecraftAccounts"] = accounts
	if len(accounts) > 0 {
		me["minecraft"] = accounts[0]
	}
	writeJSON(w, http.StatusOK, me)
	return nil
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ DisplayName string }
	if err := decode(r, &in); err != nil {
		return err
	}
	u.DisplayName = strings.TrimSpace(in.DisplayName)
	if len(u.DisplayName) > 64 {
		return errBadRequest("Display names are at most 64 characters.")
	}
	if err := s.Store.UpdateUserProfile(r.Context(), u.ID, u.DisplayName); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, u)
	return nil
}

func (s *Server) setPassword(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Current, New string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if u.PasswordHash != "" && !auth.CheckPassword(u.PasswordHash, in.Current) {
		return errBadRequest("The current password is wrong.")
	}
	if len(in.New) < 8 {
		return errBadRequest("Use a password of at least 8 characters.")
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		return err
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, hash); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request, u *store.User) error {
	keys, err := s.Store.SSHKeys(r.Context(), u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, keys)
	return nil
}

// parseSSHKey validates one authorized_keys line.
func parseSSHKey(userID, name, line string) (*store.SSHKey, error) {
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, errBadRequest("That is not a valid SSH public key. Paste the contents of a .pub file.")
	}
	if name = strings.TrimSpace(name); name == "" {
		name = comment
	}
	if name == "" {
		name = pub.Type()
	}
	return &store.SSHKey{
		UserID:      userID,
		Name:        name,
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		Fingerprint: ssh.FingerprintSHA256(pub),
	}, nil
}

func (s *Server) addSSHKey(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Name, PublicKey string }
	if err := decode(r, &in); err != nil {
		return err
	}
	key, err := parseSSHKey(u.ID, in.Name, in.PublicKey)
	if err != nil {
		return err
	}
	if err := s.Store.AddSSHKey(r.Context(), key); errors.Is(err, store.ErrConflict) {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: "That key is already registered to an account."}
	} else if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, key)
	return nil
}

// importSSHKeys adds the public keys GitHub publishes for an account.
func (s *Server) importSSHKeys(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ GithubUsername string }
	if err := decode(r, &in); err != nil {
		return err
	}
	login := strings.TrimSpace(in.GithubUsername)
	if !regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`).MatchString(login) {
		return errBadRequest("Enter a GitHub username.")
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://github.com/"+login+".keys", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not reach GitHub."}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errBadRequest("GitHub has no user named %s.", login)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	added := 0
	for i, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, err := parseSSHKey(u.ID, fmt.Sprintf("github:%s #%d", login, i+1), line)
		if err != nil {
			continue
		}
		if err := s.Store.AddSSHKey(r.Context(), key); err == nil {
			added++
		} else if !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	keys, err := s.Store.SSHKeys(r.Context(), u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "keys": keys})
	return nil
}

func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.DeleteSSHKey(r.Context(), u.ID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request, u *store.User) error {
	tokens, err := s.Store.APITokens(r.Context(), u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, tokens)
	return nil
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 64 {
		return errBadRequest("Give the token a name of up to 64 characters.")
	}
	token := &store.APIToken{UserID: u.ID, Name: in.Name}
	secret, err := s.Store.CreateAPIToken(r.Context(), token)
	if err != nil {
		return err
	}
	// The secret is only ever shown in this response.
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "secret": secret})
	return nil
}

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.DeleteAPIToken(r.Context(), u.ID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
