package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/auth/fakeidp"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// withIDP gives the panel an OpenID Connect provider of its own.
func withIDP(t *testing.T, p *panel, change ...func(*auth.OIDCConfig)) *fakeidp.IDP {
	t.Helper()
	idp, err := fakeidp.Start("avsys", "test-secret", p.srv.Config.PublicURL+"/api/v1/auth/oidc/callback")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idp.Close() })
	cfg := auth.OIDCConfig{Issuer: idp.URL, ClientID: "avsys", ClientSecret: "test-secret", Name: "Test ID", Scopes: []string{"profile", "email"}}
	for _, f := range change {
		f(&cfg)
	}
	provider, err := auth.NewOIDCProvider(cfg, p.srv.Config.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	p.srv.Config.Providers = map[string]*auth.Provider{provider.ID: provider}
	return idp
}

// browser keeps the cookies the panel sets, as a browser would, and can
// be signed in with an API token instead.
type browser struct {
	t       *testing.T
	p       *panel
	cookies map[string]string
	token   string
	// addr is the address requests come from, if not httptest's.
	addr string
}

func (p *panel) browser() *browser {
	return &browser{t: p.t, p: p, cookies: map[string]string{}}
}

// get opens an address on the panel and returns where it sends the browser.
func (b *browser) get(address string) (int, string) {
	b.t.Helper()
	req := httptest.NewRequest(http.MethodGet, address, nil)
	if b.addr != "" {
		req.RemoteAddr = b.addr
	}
	for name, value := range b.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
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
	return rec.Code, rec.Header().Get("Location")
}

// start sets out to sign in and returns the provider's address.
func (b *browser) start(query string) string {
	b.t.Helper()
	code, to := b.get("/api/v1/auth/oidc/start?" + query)
	if code != http.StatusFound || to == "" {
		b.t.Fatalf("start: %d to %q", code, to)
	}
	return to
}

// signIn goes the whole way round as login, and returns where the panel
// sends the browser at the end.
func (b *browser) signIn(idp *fakeidp.IDP, login, query string) string {
	b.t.Helper()
	back, err := idp.ApproveURL(b.start(query), login)
	if err != nil {
		b.t.Fatal(err)
	}
	_, to := b.get(back)
	return to
}

// me is who the browser is signed in as, or "".
func (b *browser) me() string {
	b.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	if c, ok := b.cookies[sessionCookie]; ok {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c})
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	rec := httptest.NewRecorder()
	b.p.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return ""
	}
	var me struct{ User store.User }
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		b.t.Fatal(err)
	}
	return me.User.Username
}

func (p *panel) userCount() int {
	p.t.Helper()
	n, err := p.srv.Store.CountUsers(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	return n
}

func refusedWith(t *testing.T, to, want string) {
	t.Helper()
	u, err := url.Parse(to)
	if err != nil || !strings.Contains(u.Query().Get("error"), want) {
		t.Errorf("sent to %q, want an error saying %q", to, want)
	}
}

// Someone new signs in through the provider and has an account made; the
// next time, they are signed in to it.
func TestOIDCSignInMakesAnAccountOnce(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.AllowSignups = true
	idp := withIDP(t, p)
	before := p.userCount()

	b := p.browser()
	if to := b.signIn(idp, "newbie", "next=/o/alpha"); to != "/o/alpha" {
		t.Errorf("sent to %q, want /o/alpha", to)
	}
	if got := b.me(); got != "newbie" {
		t.Fatalf("signed in as %q, want newbie", got)
	}
	u, err := p.srv.Store.UserByUsername(context.Background(), "newbie")
	if err != nil {
		t.Fatal(err)
	}
	if u.IsAdmin || u.Email != "newbie@idp.test" || u.DisplayName != "Newbie" {
		t.Errorf("account = %+v", u)
	}
	again := p.browser()
	again.signIn(idp, "newbie", "")
	if got := again.me(); got != "newbie" || p.userCount() != before+1 {
		t.Errorf("second sign-in: as %q, %d accounts, want newbie and %d", got, p.userCount(), before+1)
	}
}

// An account is never matched by its email address or username: a new
// provider account with the same ones as someone here is someone new.
func TestOIDCSignInNeverMatchesByEmail(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.AllowSignups = true
	idp := withIDP(t, p)
	if err := p.srv.Store.CreateUser(context.Background(), &store.User{Username: "steve", Email: "steve@idp.test"}); err != nil {
		t.Fatal(err)
	}
	b := p.browser()
	b.signIn(idp, "steve", "")
	if got := b.me(); got != "steve2" {
		t.Errorf("signed in as %q, want a new account, steve2", got)
	}
}

func TestOIDCSignInNeedsAnInvite(t *testing.T) {
	p := newPanel(t)
	idp := withIDP(t, p)
	before := p.userCount()
	b := p.browser()
	refusedWith(t, b.signIn(idp, "newbie", ""), "invite")
	if b.me() != "" || p.userCount() != before {
		t.Error("an account was made without an invite")
	}

	alpha, err := p.srv.Store.OrgBySlug(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	inv := &store.Invite{OrgID: alpha.ID, Role: store.RoleViewer, CreatedBy: "test"}
	if err := p.srv.Store.CreateInvite(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	b.signIn(idp, "newbie", "invite="+inv.Token)
	if b.me() != "newbie" {
		t.Fatal("not signed in with an invite")
	}
	u, _ := p.srv.Store.UserByUsername(context.Background(), "newbie")
	if role, err := p.srv.Store.RoleIn(context.Background(), alpha.ID, u.ID); err != nil || role != store.RoleViewer {
		t.Errorf("role in alpha = %q, %v", role, err)
	}
}

// Someone signed in links a provider account to theirs, and can then sign
// in with it. A provider account linked to someone else is not moved, and
// the one linking stays who they are.
func TestOIDCLinking(t *testing.T) {
	p := newPanel(t)
	idp := withIDP(t, p)
	before := p.userCount()

	viewer := p.browser()
	viewer.token = p.tokens["viewer"]
	if to := viewer.signIn(idp, "vee", "next=/account"); to != "/account" {
		t.Errorf("linked, sent to %q", to)
	}
	b := p.browser()
	b.signIn(idp, "vee", "")
	if got := b.me(); got != "viewer" {
		t.Errorf("signed in with the linked account as %q, want viewer", got)
	}

	// The operator tries to link the viewer's.
	operator := p.browser()
	operator.token = p.tokens["operator"]
	to := operator.signIn(idp, "vee", "next=/account")
	refusedWith(t, to, "linked to someone else")
	if !strings.HasPrefix(to, "/account?") {
		t.Errorf("sent to %q, want back to /account", to)
	}
	if _, ok := operator.cookies[sessionCookie]; ok {
		t.Error("the operator was given a session as someone")
	}
	b = p.browser()
	b.signIn(idp, "vee", "")
	if got := b.me(); got != "viewer" || p.userCount() != before {
		t.Errorf("after the attempt: signed in as %q with %d accounts", got, p.userCount())
	}
}

// What is checked of the browser coming back: that it set out from here,
// for this provider, and that the state it brings is the one it was given.
func TestOIDCCallbackChecks(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.AllowSignups = true
	idp := withIDP(t, p)
	github, err := auth.NewProvider("github", "client", "secret", p.srv.Config.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	p.srv.Config.Providers["github"] = github
	before := p.userCount()

	approve := func(b *browser) string {
		back, err := idp.ApproveURL(b.start(""), "newbie")
		if err != nil {
			t.Fatal(err)
		}
		return back
	}
	withState := func(back, state string) string {
		u, _ := url.Parse(back)
		q := u.Query()
		if state == "" {
			q.Del("state")
		} else {
			q.Set("state", state)
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	// No cookie: a browser that did not set out from here.
	b := p.browser()
	back := approve(b)
	stranger := p.browser()
	_, to := stranger.get(back)
	refusedWith(t, to, "expired")

	for name, change := range map[string]func(back string) string{
		"no state":      func(back string) string { return withState(back, "") },
		"another state": func(back string) string { return withState(back, "x"+b.cookies[oauthCookie][:8]) },
		"another provider's callback": func(back string) string {
			return strings.Replace(back, "/auth/oidc/", "/auth/github/", 1)
		},
	} {
		b := p.browser()
		_, to := b.get(change(approve(b)))
		refusedWith(t, to, "could not be verified")
		if b.me() != "" {
			t.Errorf("%s: signed in", name)
		}
	}

	// Another browser's state and code, with this browser's cookie.
	victim, attacker := p.browser(), p.browser()
	victim.start("")
	_, to = victim.get(approve(attacker))
	refusedWith(t, to, "could not be verified")

	// The cookie is the attempt's: it is gone once used, and a second
	// visit to the same address is refused.
	b = p.browser()
	back = approve(b)
	saved := b.cookies[oauthCookie]
	b.get(back)
	if b.me() != "newbie" {
		t.Fatal("not signed in")
	}
	if _, ok := b.cookies[oauthCookie]; ok {
		t.Error("the sign-in cookie was kept")
	}
	replay := p.browser()
	replay.cookies[oauthCookie] = saved
	_, to = replay.get(back)
	refusedWith(t, to, "Could not sign in")
	if replay.me() != "" {
		t.Error("a code was redeemed twice")
	}

	// The verifier in the cookie must be the one the challenge was made from.
	b = p.browser()
	back = approve(b)
	cookie, _ := url.QueryUnescape(b.cookies[oauthCookie])
	values, _ := url.ParseQuery(cookie)
	values.Set("verifier", strings.Repeat("A", 43))
	b.cookies[oauthCookie] = url.QueryEscape(values.Encode())
	_, to = b.get(back)
	refusedWith(t, to, "Could not sign in")
	if b.me() != "" {
		t.Error("signed in with another verifier")
	}

	// The provider says no.
	b = p.browser()
	start, _ := url.Parse(b.start(""))
	_, to = b.get("/api/v1/auth/oidc/callback?error=access_denied&state=" + url.QueryEscape(start.Query().Get("state")))
	refusedWith(t, to, "cancelled")

	if n := p.userCount(); n != before+1 {
		t.Errorf("%d accounts made, want 1", n-before)
	}
}

func TestOIDCAllowedGroups(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.AllowSignups = true
	idp := withIDP(t, p, func(c *auth.OIDCConfig) { c.AllowedGroups = []string{"minecraft"} })
	idp.Set(func(i *fakeidp.IDP) { i.Users["in"] = map[string]any{"groups": []string{"minecraft"}} })
	b := p.browser()
	refusedWith(t, b.signIn(idp, "out", ""), "not allowed")
	if b.me() != "" {
		t.Error("signed in outside the allowed groups")
	}
	b.signIn(idp, "in", "")
	if b.me() != "in" {
		t.Error("not signed in from an allowed group")
	}
	// Taken out of the group, they can no longer sign in, linked or not.
	idp.Set(func(i *fakeidp.IDP) { i.Users["in"] = nil })
	b = p.browser()
	refusedWith(t, b.signIn(idp, "in", ""), "not allowed")
	if b.me() != "" {
		t.Error("signed in after leaving the group")
	}
}

func TestOIDCProviderDown(t *testing.T) {
	p := newPanel(t)
	provider, err := auth.NewOIDCProvider(auth.OIDCConfig{Issuer: "http://127.0.0.1:1", ClientID: "a", ClientSecret: "b", Name: "Test ID"}, p.srv.Config.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	p.srv.Config.Providers = map[string]*auth.Provider{provider.ID: provider}
	_, to := p.browser().get("/api/v1/auth/oidc/start")
	refusedWith(t, to, "Could not reach Test ID")
}

// The provider is offered on the sign-in page, under its own name.
func TestOIDCInMeta(t *testing.T) {
	p := newPanel(t)
	withIDP(t, p)
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	var meta struct{ Providers []providerInfo }
	json.Unmarshal(rec.Body.Bytes(), &meta)
	if len(meta.Providers) != 1 || meta.Providers[0] != (providerInfo{ID: "oidc", Name: "Test ID"}) {
		t.Errorf("providers = %+v", meta.Providers)
	}
}
