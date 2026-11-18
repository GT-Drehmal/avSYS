package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// tab is someone signing in to the panel from a browser of their own:
// it keeps the cookies the panel sets, and sends what the UI does.
type tab struct {
	p       *panel
	addr    string
	agent   string
	cookies map[string]string
	// header is sent with every request, as a proxy in front would add.
	header map[string]string
}

const testPassword = "correct horse battery"

// withPasswords gives the panel's people the password testPassword.
func (p *panel) withPasswords() {
	p.t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, u := range p.users {
		if err := p.srv.Store.SetPassword(context.Background(), u.ID, hash); err != nil {
			p.t.Fatal(err)
		}
		u.PasswordHash = hash
	}
}

// tab opens a new browser, at an address of its own, so that the limits
// on guesses of one test are not another's.
func (p *panel) tab(agent string) *tab {
	return &tab{p: p, agent: agent, cookies: map[string]string{},
		addr: fmt.Sprintf("10.%d.%d.%d:4242", rand.IntN(250), rand.IntN(250), rand.IntN(250)+1)}
}

// call sends a request and decodes a JSON answer into out, if given. It
// returns the status and the error code of a failure.
func (b *tab) call(method, path string, body any, out any) (int, string) {
	b.p.t.Helper()
	var payload string
	if body != nil {
		raw, _ := json.Marshal(body)
		payload = string(raw)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(payload))
	req.RemoteAddr = b.addr
	req.Header.Set("X-Avsys", "1")
	req.Header.Set("User-Agent", b.agent)
	for name, value := range b.header {
		req.Header.Set(name, value)
	}
	for name, value := range b.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	b.p.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	var e struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			b.p.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return rec.Code, e.Code
}

// signIn signs in with a password, and the code of secret if it is given.
func (b *tab) signIn(username, secret string) {
	b.p.t.Helper()
	var out map[string]any
	if got, code := b.call("POST", "/auth/login", map[string]string{"username": username, "password": testPassword}, &out); got != 200 {
		b.p.t.Fatalf("signing in as %s: %d %s", username, got, code)
	}
	if out["twoFactor"] == true {
		if secret == "" {
			b.p.t.Fatalf("%s has two-factor, and no secret was given", username)
		}
		// The code of the next step: that of this one went to setting it up.
		if got, code := b.call("POST", "/auth/login/code", map[string]string{"code": codeOf(b.p.t, secret, 1)}, nil); got != 200 {
			b.p.t.Fatalf("the code of %s: %d %s", username, got, code)
		}
	}
	if b.cookies[sessionCookie] == "" {
		b.p.t.Fatalf("signing in as %s left no session", username)
	}
}

func (b *tab) sessions() []store.Session {
	b.p.t.Helper()
	var out []store.Session
	if got, code := b.call("GET", "/me/sessions", nil, &out); got != 200 {
		b.p.t.Fatalf("listing sessions: %d %s", got, code)
	}
	return out
}

func (b *tab) signedIn() bool {
	b.p.t.Helper()
	got, _ := b.call("GET", "/me", nil, nil)
	return got == 200
}

// fromProvider is a request on the callback of a sign-in through a
// provider, as startSession is given there.
func fromProvider() *http.Request {
	// See TestProviderCallbackIsTheRouteServed: this is the one the panel
	// serves.
	r := httptest.NewRequest("GET", "/api/v1/auth/github/callback", nil)
	r.Pattern = providerCallback
	return r
}

// forgetGuesses forgets the wrong guesses under a key of the limiter.
func forgetGuesses(key string) {
	logins.mu.Lock()
	defer logins.mu.Unlock()
	delete(logins.failures, key)
}

// codeOf is the code of secret, steps of 30 seconds from now.
func codeOf(t *testing.T, secret string, steps int) string {
	t.Helper()
	code, err := auth.TOTPCode(secret, time.Now().Add(time.Duration(steps)*30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// turnOnTwoFactor sets two-factor up from a signed-in browser, and returns
// the secret and the recovery codes.
func (b *tab) turnOnTwoFactor() (string, []string) {
	b.p.t.Helper()
	var setup struct{ Secret, URL, QR string }
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{"password": testPassword}, &setup); got != 200 {
		b.p.t.Fatalf("setting up two-factor: %d %s", got, code)
	}
	var done struct{ RecoveryCodes []string }
	if got, code := b.call("POST", "/me/two-factor", map[string]string{"code": codeOf(b.p.t, setup.Secret, 0)}, &done); got != 200 {
		b.p.t.Fatalf("confirming two-factor: %d %s", got, code)
	}
	return setup.Secret, done.RecoveryCodes
}

func TestSessionsAreListedAndEndedOneByOne(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	laptop := p.tab("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Firefox/140.0")
	phone := p.tab("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Version/18.0 Mobile Safari/604.1")
	laptop.signIn("viewer", "")
	phone.signIn("viewer", "")
	// Someone else's are not among them.
	other := p.tab("curl/8.0")
	other.signIn("operator", "")

	list := laptop.sessions()
	if len(list) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(list), list)
	}
	byAgent := map[string]store.Session{}
	for _, s := range list {
		byAgent[s.UserAgent] = s
		if s.Method != store.SignedInWithPassword || s.ID == "" || s.CreatedAt.IsZero() || s.LastUsedAt.IsZero() {
			t.Errorf("session %+v is missing what it is", s)
		}
	}
	mine, theirs := byAgent[laptop.agent], byAgent[phone.agent]
	if !mine.Current || theirs.Current {
		t.Errorf("this device is %v and the phone %v, want true and false", mine.Current, theirs.Current)
	}
	if mine.Address != strings.Split(laptop.addr, ":")[0] {
		t.Errorf("address %q, want %q", mine.Address, laptop.addr)
	}
	// The other's session cannot be ended from here.
	otherID := other.sessions()[0].ID
	if got, _ := laptop.call("DELETE", "/me/sessions/"+otherID, nil, nil); got != 404 {
		t.Errorf("ending someone else's session: got %d, want 404", got)
	}
	if !other.signedIn() {
		t.Error("someone else's session was ended")
	}
	// The phone's can.
	if got, code := laptop.call("DELETE", "/me/sessions/"+theirs.ID, nil, nil); got != 204 {
		t.Fatalf("ending the phone's session: %d %s", got, code)
	}
	if phone.signedIn() {
		t.Error("the phone is still signed in")
	}
	if !laptop.signedIn() {
		t.Error("the laptop was signed out with the phone")
	}
	// And this one, which signs out here.
	if got, _ := laptop.call("DELETE", "/me/sessions/"+mine.ID, nil, nil); got != 204 {
		t.Fatalf("ending this session: %d", got)
	}
	if laptop.signedIn() || laptop.cookies[sessionCookie] != "" {
		t.Error("ending this session left the laptop signed in")
	}
}

func TestSigningOutEverywhereElse(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	here, there, elsewhere := p.tab("here"), p.tab("there"), p.tab("elsewhere")
	for _, b := range []*tab{here, there, elsewhere} {
		b.signIn("viewer", "")
	}
	if got, _ := here.call("DELETE", "/me/sessions", nil, nil); got != 204 {
		t.Fatalf("signing out everywhere else: %d", got)
	}
	if !here.signedIn() || there.signedIn() || elsewhere.signedIn() {
		t.Errorf("signed in: here %v, there %v, elsewhere %v; want only here", here.signedIn(), there.signedIn(), elsewhere.signedIn())
	}
	if list := here.sessions(); len(list) != 1 || !list[0].Current {
		t.Errorf("sessions left: %+v", list)
	}
}

func TestChangingThePasswordEndsTheOtherSessions(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	here, there := p.tab("here"), p.tab("there")
	here.signIn("viewer", "")
	there.signIn("viewer", "")
	if got, code := here.call("POST", "/me/password", map[string]string{"current": "wrong", "new": "a new password"}, nil); got != 400 || code != "wrong_password" {
		t.Errorf("a wrong current password: %d %s", got, code)
	}
	if !there.signedIn() {
		t.Fatal("a refused change ended a session")
	}
	if got, code := here.call("POST", "/me/password", map[string]string{"current": testPassword, "new": "a new password"}, nil); got != 204 {
		t.Fatalf("changing the password: %d %s", got, code)
	}
	if !here.signedIn() {
		t.Error("changing the password signed out the browser it was changed in")
	}
	if there.signedIn() {
		t.Error("the other browser is still signed in after the password changed")
	}
}

// Guessing the current password from a session someone left open is
// limited as signing in is.
func TestCurrentPasswordGuessesAreLimited(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	b := p.tab("guesser")
	b.signIn("operator", "")
	for i := 0; i < loginMaxFailures; i++ {
		b.call("POST", "/me/password", map[string]string{"current": fmt.Sprint("guess", i), "new": "a new password"}, nil)
	}
	if got, code := b.call("POST", "/me/password", map[string]string{"current": testPassword, "new": "a new password"}, nil); got != 429 || code != "rate_limited" {
		t.Errorf("after %d guesses: %d %s, want 429", loginMaxFailures, got, code)
	}
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{"password": testPassword}, nil); got != 429 {
		t.Errorf("setting up two-factor after the guesses: %d %s, want 429", got, code)
	}
}

func TestTwoFactorSignIn(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	b := p.tab("setter")
	b.signIn("viewer", "")

	// Setting it up takes the password.
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{"password": "wrong"}, nil); got != 400 || code != "wrong_password" {
		t.Errorf("setting up with a wrong password: %d %s", got, code)
	}
	var setup struct{ Secret, URL, QR string }
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{"password": testPassword}, &setup); got != 200 {
		t.Fatalf("setting up: %d %s", got, code)
	}
	if setup.Secret == "" || !strings.HasPrefix(setup.URL, "otpauth://totp/avSYS:viewer@panel.test?") || !strings.HasPrefix(setup.QR, "data:image/png;base64,") {
		t.Errorf("setup gave %+v", setup)
	}
	// Not on until a code confirms it.
	var me map[string]any
	b.call("GET", "/me", nil, &me)
	if me["twoFactor"] != false {
		t.Errorf("two-factor is %v before it was confirmed", me["twoFactor"])
	}
	if got, code := b.call("POST", "/me/two-factor", map[string]string{"code": "000000"}, nil); got != 400 || code != "wrong_code" {
		t.Errorf("confirming with a wrong code: %d %s", got, code)
	}
	var done struct{ RecoveryCodes []string }
	if got, code := b.call("POST", "/me/two-factor", map[string]string{"code": codeOf(t, setup.Secret, 0)}, &done); got != 200 {
		t.Fatalf("confirming: %d %s", got, code)
	}
	if len(done.RecoveryCodes) != 10 {
		t.Errorf("got %d recovery codes, want 10", len(done.RecoveryCodes))
	}
	b.call("GET", "/me", nil, &me)
	if me["twoFactor"] != true || me["recoveryCodes"] != float64(10) {
		t.Errorf("after confirming: twoFactor %v, recoveryCodes %v", me["twoFactor"], me["recoveryCodes"])
	}
	// Confirming again, with nothing being set up, is refused.
	if got, code := b.call("POST", "/me/two-factor", map[string]string{"code": codeOf(t, setup.Secret, 0)}, nil); got != 409 || code != "no_setup" {
		t.Errorf("confirming twice: %d %s", got, code)
	}
	if got, _ := b.call("POST", "/me/two-factor/setup", map[string]string{"password": testPassword}, nil); got != 409 {
		t.Errorf("setting up again while on: %d, want 409", got)
	}

	// Signing in now asks for the code after the password, and gives no
	// session until it is given.
	c := p.tab("signer")
	var out map[string]any
	if got, _ := c.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, &out); got != 200 || out["twoFactor"] != true {
		t.Fatalf("signing in: %d %v", got, out)
	}
	if c.cookies[sessionCookie] != "" || c.signedIn() {
		t.Fatal("a password alone signed in an account with two-factor")
	}
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": "123456"}, nil); got != 400 || code != "wrong_code" {
		t.Errorf("a wrong code: %d %s", got, code)
	}
	// The code that confirmed the setup was used: not again.
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, setup.Secret, 0)}, nil); got != 400 || code != "wrong_code" {
		t.Errorf("the code used to confirm, again: %d %s", got, code)
	}
	next := codeOf(t, setup.Secret, 1)
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": next}, nil); got != 200 {
		t.Fatalf("the next code: %d %s", got, code)
	}
	if !c.signedIn() || c.cookies[challengeCookie] != "" {
		t.Error("the right code did not sign in, or left the sign-in waiting")
	}
	if list := c.sessions(); list[0].Method != store.SignedInWithPassword {
		t.Errorf("the session is of %q", list[0].Method)
	}

	// A code, once used, does not sign in again: nor one of before it.
	d := p.tab("replayer")
	d.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
	for _, code := range []string{next, codeOf(t, setup.Secret, 0), codeOf(t, setup.Secret, -1)} {
		if got, _ := d.call("POST", "/auth/login/code", map[string]string{"code": code}, nil); got != 400 {
			t.Errorf("a used code, or one before it: %d, want 400", got)
		}
	}
	if d.signedIn() {
		t.Fatal("a used code signed in")
	}

	// The wrong codes so far, as many as are let through, are forgotten.
	forgetGuesses("code|" + p.users["viewer"].ID)

	// A recovery code signs in, once.
	if got, code := d.call("POST", "/auth/login/code", map[string]string{"code": strings.ToUpper(done.RecoveryCodes[0])}, nil); got != 200 {
		t.Fatalf("a recovery code: %d %s", got, code)
	}
	e := p.tab("recoverer")
	e.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
	if got, _ := e.call("POST", "/auth/login/code", map[string]string{"code": done.RecoveryCodes[0]}, nil); got != 400 {
		t.Errorf("a recovery code used twice: %d, want 400", got)
	}
	b.call("GET", "/me", nil, &me)
	if me["recoveryCodes"] != float64(9) {
		t.Errorf("recovery codes left: %v, want 9", me["recoveryCodes"])
	}

	// New recovery codes take the place of the old.
	var fresh struct{ RecoveryCodes []string }
	if got, _ := b.call("POST", "/me/two-factor/recovery-codes", map[string]string{"password": testPassword}, &fresh); got != 200 || len(fresh.RecoveryCodes) != 10 {
		t.Fatalf("new recovery codes: %d %v", got, fresh.RecoveryCodes)
	}
	if got, _ := e.call("POST", "/auth/login/code", map[string]string{"code": done.RecoveryCodes[1]}, nil); got != 400 {
		t.Errorf("an old recovery code after new ones were made: %d, want 400", got)
	}
	if got, code := e.call("POST", "/auth/login/code", map[string]string{"code": fresh.RecoveryCodes[0]}, nil); got != 200 {
		t.Errorf("a new recovery code: %d %s", got, code)
	}

	// Turning it off takes the password, and then a password alone signs in.
	if got, code := b.call("POST", "/me/two-factor/off", map[string]string{"password": "wrong"}, nil); got != 400 || code != "wrong_password" {
		t.Errorf("turning off with a wrong password: %d %s", got, code)
	}
	if got, _ := b.call("POST", "/me/two-factor/off", map[string]string{"password": testPassword}, nil); got != 204 {
		t.Fatalf("turning off: %d", got)
	}
	p.tab("after").signIn("viewer", "")
}

// Signing in through a provider is not asked for a code: the provider sees
// to a second factor.
func TestTwoFactorIsForPasswordSignIns(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	b := p.tab("b")
	b.signIn("viewer", "")
	b.turnOnTwoFactor()
	rec := httptest.NewRecorder()
	if err := p.srv.startSession(rec, fromProvider(), p.users["viewer"].ID); err != nil {
		t.Fatal(err)
	}
	via := p.tab("provider")
	for _, c := range rec.Result().Cookies() {
		via.cookies[c.Name] = c.Value
	}
	if !via.signedIn() {
		t.Error("a sign-in through a provider was held up for a code")
	}
}

func TestASignInWaitingForItsCodeEnds(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	b := p.tab("b")
	b.signIn("viewer", "")
	secret, _ := b.turnOnTwoFactor()

	// With no sign-in waiting, there is nothing to give a code to.
	none := p.tab("none")
	if got, code := none.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 401 || code != "sign_in_again" {
		t.Errorf("a code with no sign-in: %d %s", got, code)
	}
	none.cookies[challengeCookie] = "made-up"
	if got, code := none.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 401 || code != "sign_in_again" {
		t.Errorf("a code with a made-up sign-in: %d %s", got, code)
	}

	// One that has waited too long is gone.
	late := p.tab("late")
	late.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
	challenges.mu.Lock()
	challenges.m[late.cookies[challengeCookie]].expires = time.Now().Add(-time.Second)
	challenges.mu.Unlock()
	if got, code := late.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 401 || code != "sign_in_again" {
		t.Errorf("a code after the sign-in expired: %d %s", got, code)
	}

	// Too many wrong codes, and the password is asked for again.
	wrong := p.tab("wrong")
	wrong.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
	for i := 1; i < challengeTries; i++ {
		if got, code := wrong.call("POST", "/auth/login/code", map[string]string{"code": "000000"}, nil); got != 400 || code != "wrong_code" {
			t.Fatalf("wrong code %d: %d %s", i, got, code)
		}
	}
	if got, code := wrong.call("POST", "/auth/login/code", map[string]string{"code": "000000"}, nil); got != 401 || code != "sign_in_again" {
		t.Errorf("wrong code %d: %d %s, want sign_in_again", challengeTries, got, code)
	}
	if got, _ := wrong.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 401 || wrong.signedIn() {
		t.Error("the right code went through after the sign-in was ended")
	}

	// The password changed while a sign-in waits, as by a reset: the old
	// password and a code do not sign in.
	before := p.tab("before")
	before.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
	hash, err := auth.HashPassword("a new password")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.srv.Store.SetPassword(context.Background(), p.users["viewer"].ID, hash); err != nil {
		t.Fatal(err)
	}
	if got, code := before.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 401 || code != "sign_in_again" || before.signedIn() {
		t.Errorf("a code after the password changed: %d %s", got, code)
	}

	// Two-factor turned off while a sign-in waits: that sign-in starts again.
	waiting := p.tab("waiting")
	waiting.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": "a new password"}, nil)
	if err := p.srv.Store.DisableTwoFactor(context.Background(), p.users["viewer"].ID); err != nil {
		t.Fatal(err)
	}
	if got, code := waiting.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 401 || code != "sign_in_again" {
		t.Errorf("a code after two-factor was turned off: %d %s", got, code)
	}
}

// Codes are guessed against the account, wherever the guesses come from.
func TestCodeGuessesAreLimited(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	b := p.tab("b")
	b.signIn("viewer", "")
	secret, _ := b.turnOnTwoFactor()
	for i := 0; i < loginMaxFailures; i++ {
		// A new browser each time, from a new address, with a new sign-in.
		g := p.tab("guesser")
		g.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
		if got, code := g.call("POST", "/auth/login/code", map[string]string{"code": fmt.Sprintf("%06d", i)}, nil); got != 400 {
			t.Fatalf("guess %d: %d %s", i, got, code)
		}
	}
	g := p.tab("guesser")
	g.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil)
	if got, code := g.call("POST", "/auth/login/code", map[string]string{"code": codeOf(t, secret, 1)}, nil); got != 429 || code != "rate_limited" {
		t.Errorf("after %d wrong codes: %d %s, want 429", loginMaxFailures, got, code)
	}
}

func TestAdministratorsCanBeMadeToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	root, viewer := p.tab("root"), p.tab("viewer")
	root.signIn("root", "")
	viewer.signIn("viewer", "")

	// Only an instance administrator may say so.
	if got, _ := viewer.call("PUT", "/settings/admin-two-factor", map[string]bool{"required": true}, nil); got != 403 {
		t.Errorf("a member requiring two-factor: %d, want 403", got)
	}
	if got, _ := root.call("PUT", "/settings/admin-two-factor", map[string]bool{"required": true}, nil); got != 204 {
		t.Fatalf("requiring two-factor: %d", got)
	}

	// The administrator, signed in with a password and without it, is held
	// to setting it up: /me says so, and nothing else answers.
	var me map[string]any
	if got, _ := root.call("GET", "/me", nil, &me); got != 200 || me["mustSetUpTwoFactor"] != true || me["adminTwoFactor"] != true {
		t.Errorf("/me: %d %v", got, me)
	}
	for _, route := range [][2]string{{"GET", "/orgs/alpha"}, {"POST", "/orgs"}, {"GET", "/me/sessions"}, {"POST", "/me/password"}, {"PUT", "/settings/admin-two-factor"}} {
		if got, code := root.call(route[0], route[1], map[string]any{}, nil); got != 403 || code != "two_factor_required" {
			t.Errorf("%s %s before setting up two-factor: %d %s", route[0], route[1], got, code)
		}
	}
	// Members are not held up, nor the administrator's API token.
	if !viewer.signedIn() || viewer.sessions() == nil {
		t.Error("a member was held up")
	}
	if got, _ := p.do("root", "GET", "/orgs/alpha", ""); got != 200 {
		t.Errorf("the administrator's API token: %d, want 200", got)
	}
	// Nor is the administrator signed in through a provider.
	rec := httptest.NewRecorder()
	if err := p.srv.startSession(rec, fromProvider(), p.users["root"].ID); err != nil {
		t.Fatal(err)
	}
	via := p.tab("provider")
	for _, c := range rec.Result().Cookies() {
		via.cookies[c.Name] = c.Value
	}
	if got, _ := via.call("GET", "/orgs/alpha", nil, nil); got != 200 {
		t.Errorf("the administrator signed in through a provider: %d, want 200", got)
	}

	// Once set up, it goes on.
	secret, _ := root.turnOnTwoFactor()
	if got, _ := root.call("GET", "/orgs/alpha", nil, nil); got != 200 {
		t.Errorf("after setting up two-factor: %d, want 200", got)
	}
	root.call("GET", "/me", nil, &me)
	if me["mustSetUpTwoFactor"] != false {
		t.Error("/me still says to set up two-factor")
	}
	// And it cannot be turned off while it is required.
	if got, code := root.call("POST", "/me/two-factor/off", map[string]string{"password": testPassword}, nil); got != 409 || code != "required" {
		t.Errorf("turning it off while required: %d %s", got, code)
	}
	// Not required, it can.
	if got, _ := root.call("PUT", "/settings/admin-two-factor", map[string]bool{"required": false}, nil); got != 204 {
		t.Fatal(got)
	}
	if got, _ := root.call("POST", "/me/two-factor/off", map[string]string{"password": testPassword}, nil); got != 204 {
		t.Errorf("turning it off once not required: %d", got)
	}
	// Signing in again then takes no code.
	_ = secret
	p.tab("again").signIn("root", "")
}

func TestResetLinks(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	ctx := context.Background()
	root := p.tab("root")
	root.signIn("root", "")
	viewerID := p.users["viewer"].ID

	// Only an instance administrator makes them; an organization's owner
	// does not.
	for who, want := range map[string]int{"owner": 403, "viewer": 403, "": 401} {
		if got, _ := p.do(who, "POST", "/users/"+viewerID+"/reset-link", "{}"); got != want {
			t.Errorf("%q making a reset link: %d, want %d", who, got, want)
		}
	}
	if got, _ := root.call("POST", "/users/nobody/reset-link", map[string]any{}, nil); got != 404 {
		t.Errorf("a link for no one: %d, want 404", got)
	}

	// The viewer, signed in on two browsers with two-factor on, forgot
	// their password.
	phone, laptop := p.tab("phone"), p.tab("laptop")
	phone.signIn("viewer", "")
	secret, _ := phone.turnOnTwoFactor()
	laptop.signIn("viewer", secret)

	var link struct {
		URL       string
		ExpiresAt time.Time
	}
	if got, code := root.call("POST", "/users/"+viewerID+"/reset-link", map[string]any{}, &link); got != 201 {
		t.Fatalf("making a link: %d %s", got, code)
	}
	if !strings.HasPrefix(link.URL, "http://panel.test/reset/") || time.Until(link.ExpiresAt) < 23*time.Hour || time.Until(link.ExpiresAt) > 25*time.Hour {
		t.Errorf("link %+v", link)
	}
	token := strings.TrimPrefix(link.URL, "http://panel.test/reset/")
	// A second link takes the place of the first.
	first := token
	root.call("POST", "/users/"+viewerID+"/reset-link", map[string]any{}, &link)
	token = strings.TrimPrefix(link.URL, "http://panel.test/reset/")
	if got, _ := p.tab("x").call("GET", "/reset-links/"+first, nil, nil); got != 404 {
		t.Errorf("the first link after a second was made: %d, want 404", got)
	}

	// Opened, it says whose it is.
	user := p.tab("user")
	var whose map[string]string
	if got, _ := user.call("GET", "/reset-links/"+token, nil, &whose); got != 200 || whose["username"] != "viewer" {
		t.Errorf("opening the link: %d %v", got, whose)
	}
	if got, _ := user.call("GET", "/reset-links/made-up", nil, nil); got != 404 {
		t.Errorf("a made-up link: %d, want 404", got)
	}
	if got, _ := user.call("POST", "/reset-links/"+token, map[string]string{"password": "short"}, nil); got != 400 {
		t.Errorf("a short password: %d, want 400", got)
	}
	if got, code := user.call("POST", "/reset-links/"+token, map[string]string{"password": "a brand new password"}, nil); got != 200 {
		t.Fatalf("setting the password: %d %s", got, code)
	}
	// Every session of theirs has ended.
	if phone.signedIn() || laptop.signedIn() {
		t.Error("a session of the account is still signed in after the reset")
	}
	// The link works once.
	if got, _ := user.call("POST", "/reset-links/"+token, map[string]string{"password": "another new password"}, nil); got != 404 {
		t.Errorf("the link used twice: %d, want 404", got)
	}
	if got, _ := user.call("GET", "/reset-links/"+token, nil, nil); got != 404 {
		t.Errorf("the link opened after it was used: %d, want 404", got)
	}
	// The new password signs in; two-factor is still on, as the link
	// did not say to turn it off.
	var out map[string]any
	if got, _ := user.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": "a brand new password"}, &out); got != 200 || out["twoFactor"] != true {
		t.Errorf("signing in with the new password: %d %v", got, out)
	}

	// A link that turns two-factor off does, once it is used.
	var off struct{ URL string }
	root.call("POST", "/users/"+viewerID+"/reset-link", map[string]any{"twoFactorOff": true}, &off)
	if tf, _ := p.srv.Store.TwoFactor(ctx, viewerID); tf.Secret == "" {
		t.Error("two-factor was turned off by making the link, before it was used")
	}
	if got, _ := user.call("POST", "/reset-links/"+strings.TrimPrefix(off.URL, "http://panel.test/reset/"), map[string]string{"password": "a third new password"}, nil); got != 200 {
		t.Fatalf("using the link that turns two-factor off: %d", got)
	}
	if tf, _ := p.srv.Store.TwoFactor(ctx, viewerID); tf.Secret != "" || tf.RecoveryCodes != 0 {
		t.Errorf("two-factor after the reset: %+v", tf)
	}
	out = nil
	if got, _ := user.call("POST", "/auth/login", map[string]string{"username": "viewer", "password": "a third new password"}, &out); got != 200 || out["twoFactor"] != nil {
		t.Errorf("signing in after two-factor was turned off: %d %v", got, out)
	}

	// An expired link does nothing.
	expired, err := p.srv.Store.CreateResetLink(ctx, &store.ResetLink{UserID: viewerID, CreatedBy: p.users["root"].ID}, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := user.call("GET", "/reset-links/"+expired, nil, nil); got != 404 {
		t.Errorf("an expired link opened: %d, want 404", got)
	}
	if got, _ := user.call("POST", "/reset-links/"+expired, map[string]string{"password": "a fourth new password"}, nil); got != 404 {
		t.Errorf("an expired link used: %d, want 404", got)
	}
}

// The limit on guesses does not hold on to keys it no longer needs, whose
// number anyone could raise by guessing at made-up names.
func TestLoginLimiterLetsGoOfOldKeys(t *testing.T) {
	var l loginLimiter
	for i := 0; i < loginKeysSwept; i++ {
		l.fail(fmt.Sprint("old", i))
	}
	for _, times := range l.failures {
		times[0] = time.Now().Add(-loginWindow - time.Second)
	}
	l.fail("new")
	if len(l.failures) != 1 {
		t.Errorf("the limiter holds %d keys, want 1", len(l.failures))
	}
	if !func() bool { l.fail("new"); return len(l.failures["new"]) == 2 }() {
		t.Error("a key in use was let go of")
	}
}

// Sessions are told apart by the route they were made on: the provider's
// callback is the one served.
func TestProviderCallbackIsTheRouteServed(t *testing.T) {
	p := newPanel(t)
	method, path, _ := strings.Cut(providerCallback, " ")
	if !slices.Contains(p.srv.routes, method+" "+strings.TrimPrefix(path, "/api/v1")) {
		t.Errorf("%s is not served", providerCallback)
	}
	// A sign-up is a password's.
	b := p.tab("new")
	alpha, err := p.srv.Store.OrgBySlug(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	inv := &store.Invite{OrgID: alpha.ID, Role: store.RoleViewer, CreatedBy: "test"}
	if err := p.srv.Store.CreateInvite(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	if got, code := b.call("POST", "/auth/register", map[string]string{"username": "newcomer", "password": testPassword, "invite": inv.Token}, nil); got != 201 {
		t.Fatalf("signing up: %d %s", got, code)
	}
	if list := b.sessions(); len(list) != 1 || list[0].Method != store.SignedInWithPassword {
		t.Errorf("the session of a sign-up: %+v", list)
	}
}
