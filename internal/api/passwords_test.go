package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// With passwords off, no password is taken, made or changed, even one an
// account has from before.
func TestPasswordsOffRefusesPasswords(t *testing.T) {
	p := newPanel(t)
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.srv.Store.SetPassword(context.Background(), p.users["viewer"].ID, hash); err != nil {
		t.Fatal(err)
	}
	login := `{"username":"viewer","password":"correct horse battery staple"}`
	if got, _ := p.do("", "POST", "/auth/login", login); got != http.StatusOK {
		t.Fatalf("with passwords on, signing in: %d", got)
	}

	p.srv.Config.NoPasswords = true
	for _, c := range []struct{ who, path, body string }{
		{"", "/auth/login", login},
		{"", "/auth/register", `{"username":"newbie","password":"correct horse battery staple"}`},
		{"viewer", "/me/password", `{"current":"correct horse battery staple","new":"another long password"}`},
		{"outsider", "/me/password", `{"new":"another long password"}`},
	} {
		if got, code := p.do(c.who, "POST", c.path, c.body); got != http.StatusForbidden || code != "passwords_off" {
			t.Errorf("%s as %q: %d %q, want 403 passwords_off", c.path, c.who, got, code)
		}
	}
	if _, err := p.srv.Store.UserByUsername(context.Background(), "newbie"); err == nil {
		t.Error("an account was made with a password")
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	var meta struct{ Passwords *bool }
	json.Unmarshal(rec.Body.Bytes(), &meta)
	if meta.Passwords == nil || *meta.Passwords {
		t.Errorf("meta says passwords = %v", meta.Passwords)
	}
}

const setupCode = "the-setup-code"

// newEmptyPanel is a panel with no account yet and passwords off.
func newEmptyPanel(t *testing.T) *panel {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := &panel{
		t: t,
		srv: &Server{
			Config:  Config{PublicURL: "http://panel.test", NoPasswords: true, SetupCode: setupCode},
			Store:   db,
			Events:  NewHub(),
			Log:     log,
			Backups: &backup.Manager{Dir: t.TempDir(), Restic: "restic-is-not-installed", Log: log},
		},
		users: map[string]*store.User{}, tokens: map[string]string{},
	}
	p.h = p.srv.Handler()
	return p
}

// With passwords off, the first account is made through a provider by
// someone with the setup code, and is the administrator. Without the code,
// from the start or slipped into the cookie later, no account is made.
func TestPasswordsOffFirstRun(t *testing.T) {
	p := newEmptyPanel(t)
	idp := withIDP(t, p)

	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	var meta struct{ SetupRequired, Passwords bool }
	json.Unmarshal(rec.Body.Bytes(), &meta)
	if !meta.SetupRequired || meta.Passwords {
		t.Errorf("meta = %+v", meta)
	}

	for _, query := range []string{"", "setup=", "setup=wrong"} {
		b := p.browser()
		b.addr = "192.0.2.10:1234"
		_, to := b.get("/api/v1/auth/oidc/start?" + query)
		refusedWith(t, to, "setup link")
	}

	// The right code at the start, and another in the cookie by the end.
	b := p.browser()
	start := b.start("setup=" + setupCode)
	cookie, _ := url.QueryUnescape(b.cookies[oauthCookie])
	values, _ := url.ParseQuery(cookie)
	values.Set("setup", "wrong")
	b.cookies[oauthCookie] = url.QueryEscape(values.Encode())
	back, err := idp.ApproveURL(start, "mallory")
	if err != nil {
		t.Fatal(err)
	}
	_, to := b.get(back)
	refusedWith(t, to, "setup link")
	if p.userCount() != 0 {
		t.Fatal("an account was made without the setup code")
	}

	b = p.browser()
	if to := b.signIn(idp, "boss", "setup="+setupCode); to != "/" {
		t.Errorf("sent to %q", to)
	}
	if b.me() != "boss" {
		t.Fatal("the first sign-in did not make an account")
	}
	if admins, _ := p.srv.Store.Admins(context.Background()); len(admins) != 1 || admins[0].Username != "boss" {
		t.Errorf("admins = %+v", admins)
	}

	// From then on the code does nothing: the next person needs an invite.
	b = p.browser()
	refusedWith(t, b.signIn(idp, "mallory", "setup="+setupCode), "invite")
	if p.userCount() != 1 {
		t.Error("a second account was made")
	}
	// And the administrator signs in again.
	b = p.browser()
	b.signIn(idp, "boss", "")
	if b.me() != "boss" {
		t.Error("the administrator could not sign in again")
	}
}

// With open signups too, the first account still takes the setup code,
// even from a browser that skipped the start and made up its own cookie.
// Later ones do not.
func TestPasswordsOffFirstRunOpenSignups(t *testing.T) {
	p := newEmptyPanel(t)
	p.srv.Config.AllowSignups = true
	idp := withIDP(t, p)

	b := p.browser()
	b.addr = "192.0.2.30:1234"
	_, to := b.get("/api/v1/auth/oidc/start")
	refusedWith(t, to, "setup link")

	// A flow of the browser's own: its state, nonce and verifier, and no
	// code, as a cookie it sets itself.
	b = p.browser()
	start := b.start("setup=" + setupCode)
	cookie, _ := url.QueryUnescape(b.cookies[oauthCookie])
	values, _ := url.ParseQuery(cookie)
	values.Del("setup")
	b.cookies[oauthCookie] = url.QueryEscape(values.Encode())
	back, err := idp.ApproveURL(start, "mallory")
	if err != nil {
		t.Fatal(err)
	}
	_, to = b.get(back)
	refusedWith(t, to, "setup link")
	if p.userCount() != 0 || b.me() != "" {
		t.Fatal("the first account was made without the setup code")
	}

	b = p.browser()
	b.signIn(idp, "boss", "setup="+setupCode)
	if admins, _ := p.srv.Store.Admins(context.Background()); b.me() != "boss" || len(admins) != 1 || admins[0].Username != "boss" {
		t.Fatalf("with the code: signed in as %q, admins %+v", b.me(), admins)
	}
	b = p.browser()
	b.signIn(idp, "member", "")
	if b.me() != "member" {
		t.Error("with open signups, a later account was not made")
	}
}

// Two people with the setup code sign in at the same moment: one account is
// made, and it is the administrator.
func TestPasswordsOffFirstRunRace(t *testing.T) {
	p := newEmptyPanel(t)
	idp := withIDP(t, p)
	const n = 8
	browsers, backs := make([]*browser, n), make([]string, n)
	for i := range n {
		browsers[i] = p.browser()
		back, err := idp.ApproveURL(browsers[i].start("setup="+setupCode), "person"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		backs[i] = back
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			browsers[i].get(backs[i])
		}()
	}
	wg.Wait()
	signedIn := 0
	for _, b := range browsers {
		if b.me() != "" {
			signedIn++
		}
	}
	admins, err := p.srv.Store.Admins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.userCount() != 1 || len(admins) != 1 || signedIn != 1 {
		t.Errorf("%d accounts, %d admins, %d signed in; want one of each", p.userCount(), len(admins), signedIn)
	}
}

// Guessing the setup code is slowed as guessing a password is.
func TestPasswordsOffSetupCodeGuessing(t *testing.T) {
	p := newEmptyPanel(t)
	withIDP(t, p)
	b := p.browser()
	b.addr = "192.0.2.20:1234"
	for range loginMaxFailures {
		_, to := b.get("/api/v1/auth/oidc/start?setup=guess")
		refusedWith(t, to, "setup link")
	}
	_, to := b.get("/api/v1/auth/oidc/start?setup=" + setupCode)
	refusedWith(t, to, "Too many attempts")
}

func TestCheckSignIn(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oidc, err := auth.NewOIDCProvider(auth.OIDCConfig{Issuer: "https://idp.test", ClientID: "a", ClientSecret: "b"}, "http://panel.test")
	if err != nil {
		t.Fatal(err)
	}
	github, err := auth.NewProvider("github", "a", "b", "http://panel.test")
	if err != nil {
		t.Fatal(err)
	}
	providers := map[string]*auth.Provider{oidc.ID: oidc, github.ID: github}

	if _, err := CheckSignIn(ctx, db, nil); err == nil {
		t.Error("started with passwords off and no provider")
	}
	// No account yet: the first is made through a provider.
	if locked, err := CheckSignIn(ctx, db, providers); err != nil || len(locked) != 0 {
		t.Errorf("no accounts: %v, %v", locked, err)
	}

	first, second := &store.User{Username: "first"}, &store.User{Username: "second", IsAdmin: true}
	for _, u := range []*store.User{first, second, {Username: "member"}} {
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	link := func(u *store.User, provider, subject string) {
		if err := db.LinkIdentity(ctx, store.Identity{Provider: provider, Subject: subject, UserID: u.ID}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, wantLocked string, wantErr bool) {
		t.Helper()
		locked, err := CheckSignIn(ctx, db, providers)
		if strings.Join(locked, ",") != wantLocked || (err != nil) != wantErr {
			t.Errorf("%s: locked out %v, error %v; want %q, error %v", name, locked, err, wantLocked, wantErr)
		}
	}
	check("no administrator linked", "first,second", true)
	// Linked at another issuer, or a provider not configured, is no way in.
	link(first, "oidc", auth.OIDCSubject("https://old-idp.test", "first"))
	link(first, "discord", "1")
	check("linked only where they cannot sign in", "first,second", true)
	link(second, "github", "2")
	check("one administrator linked", "first", false)
	link(first, "oidc", auth.OIDCSubject("https://idp.test", "first"))
	check("both linked", "", false)
}
