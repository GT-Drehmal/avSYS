package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/GT-Drehmal/avSYS/internal/auth/fakeidp"
)

const (
	testClient   = "avsys"
	testSecret   = "test-secret"
	testPanelURL = "http://panel.test"
)

// newIDP starts a provider for one test, and the panel's side of it.
func newIDP(t *testing.T, change ...func(*OIDCConfig)) (*fakeidp.IDP, *Provider) {
	t.Helper()
	idp, err := fakeidp.Start(testClient, testSecret, testPanelURL+"/api/v1/auth/oidc/callback")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idp.Close() })
	cfg := OIDCConfig{Issuer: idp.URL, ClientID: testClient, ClientSecret: testSecret, Name: "Test ID", Scopes: []string{"profile", "email"}}
	for _, f := range change {
		f(&cfg)
	}
	p, err := NewOIDCProvider(cfg, testPanelURL)
	if err != nil {
		t.Fatal(err)
	}
	return idp, p
}

// begin starts a sign-in and has the provider sign login in. It returns the
// flow and the code the browser comes back with.
func begin(t *testing.T, idp *fakeidp.IDP, p *Provider, login string) (Flow, string) {
	t.Helper()
	f, to, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	back, err := idp.ApproveURL(to, login)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(back)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("state"); got != f.State {
		t.Fatalf("state came back as %q, want %q", got, f.State)
	}
	return f, u.Query().Get("code")
}

func signIn(t *testing.T, idp *fakeidp.IDP, p *Provider, login string) (Profile, error) {
	t.Helper()
	f, code := begin(t, idp, p, login)
	return p.Exchange(context.Background(), code, f)
}

func TestOIDCSignIn(t *testing.T) {
	idp, p := newIDP(t)
	idp.Set(func(i *fakeidp.IDP) {
		i.Users["steve"] = map[string]any{"name": "Steve Smith", "picture": "https://img.idp.test/steve.png"}
	})
	got, err := signIn(t, idp, p, "steve")
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Subject: idp.URL + " steve", Login: "steve", Name: "Steve Smith", Email: "steve@idp.test", AvatarURL: "https://img.idp.test/steve.png"}
	if got != want {
		t.Errorf("profile = %+v, want %+v", got, want)
	}
}

// What the browser is sent to the provider with.
func TestOIDCAuthorizationRequest(t *testing.T) {
	_, p := newIDP(t)
	f, to, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(to)
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": testClient, "redirect_uri": testPanelURL + "/api/v1/auth/oidc/callback",
		"scope": "openid profile email", "state": f.State, "nonce": f.Nonce,
		"code_challenge": oauth2.S256ChallengeFromVerifier(f.Verifier), "code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Has("client_secret") || strings.Contains(to, testSecret) {
		t.Error("the client secret is in the address the browser is sent to")
	}
	// Each sign-in has values of its own.
	g, _, _ := p.Begin(context.Background())
	if g.State == f.State || g.Nonce == f.Nonce || g.Verifier == f.Verifier || f.State == f.Nonce {
		t.Errorf("two sign-ins share values: %+v, %+v", f, g)
	}
	if len(f.State) < 32 || len(f.Nonce) < 32 || len(f.Verifier) < 43 {
		t.Errorf("values too short to be unguessable: %+v", f)
	}
}

// An ID token is refused unless everything about it checks out.
func TestOIDCRefusesBadIDTokens(t *testing.T) {
	for name, change := range map[string]func(c map[string]any){
		"another issuer":            func(c map[string]any) { c["iss"] = "https://evil.test" },
		"another audience":          func(c map[string]any) { c["aud"] = "someone-else" },
		"several audiences, no azp": func(c map[string]any) { c["aud"] = []string{testClient, "someone-else"} },
		"issued to another party":   func(c map[string]any) { c["azp"] = "someone-else" },
		"expired":                   func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"not valid yet":             func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() },
		"another sign-in's nonce":   func(c map[string]any) { c["nonce"] = "not-this-one" },
		"no nonce":                  func(c map[string]any) { delete(c, "nonce") },
		"no subject":                func(c map[string]any) { delete(c, "sub") },
	} {
		t.Run(name, func(t *testing.T) {
			idp, p := newIDP(t)
			idp.Set(func(i *fakeidp.IDP) { i.Claims = change })
			if got, err := signIn(t, idp, p, "steve"); err == nil {
				t.Errorf("signed in as %+v", got)
			}
		})
	}
	t.Run("a key the provider does not publish", func(t *testing.T) {
		idp, p := newIDP(t)
		idp.Set(func(i *fakeidp.IDP) { i.SignWithUnpublished = true })
		if got, err := signIn(t, idp, p, "steve"); err == nil {
			t.Errorf("signed in as %+v", got)
		}
	})
	t.Run("several audiences, for this client", func(t *testing.T) {
		idp, p := newIDP(t)
		idp.Set(func(i *fakeidp.IDP) {
			i.Claims = func(c map[string]any) { c["aud"], c["azp"] = []string{testClient, "someone-else"}, testClient }
		})
		if _, err := signIn(t, idp, p, "steve"); err != nil {
			t.Error(err)
		}
	})
}

// A code is redeemed with the verifier of the sign-in it was made for, once.
func TestOIDCCodes(t *testing.T) {
	idp, p := newIDP(t)
	f, code := begin(t, idp, p, "steve")
	other := f
	other.Verifier = oauth2.GenerateVerifier()
	if _, err := p.Exchange(context.Background(), code, other); err == nil {
		t.Error("a code was redeemed with another verifier")
	}

	f, code = begin(t, idp, p, "steve")
	if _, err := p.Exchange(context.Background(), code, f); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exchange(context.Background(), code, f); err == nil {
		t.Error("a code was redeemed twice")
	}

	// Another sign-in's code, with this one's flow: its token has the
	// other nonce, and its challenge another verifier.
	a, _ := begin(t, idp, p, "steve")
	_, codeB := begin(t, idp, p, "mallory")
	if got, err := p.Exchange(context.Background(), codeB, a); err == nil {
		t.Errorf("signed in with another sign-in's code as %+v", got)
	}
	// A flow without its verifier or nonce is refused before the provider is asked.
	f, code = begin(t, idp, p, "steve")
	if _, err := p.Exchange(context.Background(), code, Flow{State: f.State}); err == nil {
		t.Error("signed in without a verifier or nonce")
	}
}

// The provider rotates its keys: tokens signed with the new key are taken
// once the key set is fetched again, and tokens signed with a key it no
// longer publishes are not.
func TestOIDCKeyRotation(t *testing.T) {
	idp, p := newIDP(t)
	if _, err := signIn(t, idp, p, "steve"); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(t, idp, p, "steve"); err != nil {
		t.Fatal(err)
	}
	if n := idp.KeyFetches(); n != 1 {
		t.Errorf("the key set was fetched %d times for one key, want 1", n)
	}
	idp.Rotate(false)
	if _, err := signIn(t, idp, p, "steve"); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if n := idp.KeyFetches(); n != 2 {
		t.Errorf("the key set was fetched %d times, want 2", n)
	}
}

func TestOIDCDiscovery(t *testing.T) {
	idp, p := newIDP(t)
	idp.Set(func(i *fakeidp.IDP) { i.DiscoveryIssuer = "https://evil.test" })
	if _, _, err := p.Begin(context.Background()); err == nil {
		t.Error("began a sign-in with a provider that names another issuer")
	}
	// Not kept: once it answers properly, sign-in works.
	idp.Set(func(i *fakeidp.IDP) { i.DiscoveryIssuer = "" })
	if _, err := signIn(t, idp, p, "steve"); err != nil {
		t.Error(err)
	}

	down, err := NewOIDCProvider(OIDCConfig{Issuer: "http://127.0.0.1:1", ClientID: "a", ClientSecret: "b"}, testPanelURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := down.Discover(context.Background()); err == nil {
		t.Error("discovered a provider that is not there")
	}
}

func TestOIDCGroups(t *testing.T) {
	allow := func(c *OIDCConfig) { c.AllowedGroups = []string{"minecraft", "admins"} }
	idp, p := newIDP(t, allow)
	idp.Set(func(i *fakeidp.IDP) {
		i.Users["in"] = map[string]any{"groups": []string{"family", "minecraft"}}
		i.Users["one"] = map[string]any{"groups": "admins"}
		i.Users["out"] = map[string]any{"groups": []string{"family"}}
	})
	for login, want := range map[string]error{"in": nil, "one": nil, "out": ErrNotAllowed, "nogroups": ErrNotAllowed} {
		if _, err := signIn(t, idp, p, login); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", login, err, want)
		}
	}

	// Groups given only in the user info are read from there, if it is
	// about the same account.
	idp, p = newIDP(t, allow)
	idp.Set(func(i *fakeidp.IDP) {
		i.Users["in"] = map[string]any{"groups": []string{"minecraft"}}
		i.UserInfoOnly = []string{"groups", "name", "email"}
	})
	got, err := signIn(t, idp, p, "in")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "In" || got.Email != "in@idp.test" {
		t.Errorf("profile from the user info = %+v", got)
	}
	idp.Set(func(i *fakeidp.IDP) { i.UserInfo = func(c map[string]any) { c["sub"] = "someone-else" } })
	if _, err := signIn(t, idp, p, "in"); err == nil {
		t.Error("took groups from user info about another account")
	}

	// Another claim, if the provider calls it something else.
	idp, p = newIDP(t, allow, func(c *OIDCConfig) { c.GroupsClaim = "roles" })
	idp.Set(func(i *fakeidp.IDP) {
		i.Users["in"] = map[string]any{"roles": []string{"admins"}, "groups": []string{"family"}}
		i.Users["out"] = map[string]any{"groups": []string{"admins"}}
	})
	for login, want := range map[string]error{"in": nil, "out": ErrNotAllowed} {
		if _, err := signIn(t, idp, p, login); !errors.Is(err, want) {
			t.Errorf("roles, %s: %v, want %v", login, err, want)
		}
	}
}

// An address the provider has not checked is not taken; nor a picture that
// is not a web address.
func TestOIDCProfile(t *testing.T) {
	idp, p := newIDP(t)
	idp.Set(func(i *fakeidp.IDP) {
		i.Users["steve"] = map[string]any{"email_verified": false, "picture": "javascript:alert(1)"}
	})
	got, err := signIn(t, idp, p, "steve")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "" || got.AvatarURL != "" {
		t.Errorf("profile = %+v", got)
	}
}

func TestNewOIDCProvider(t *testing.T) {
	for _, bad := range []OIDCConfig{
		{Issuer: "", ClientID: "a", ClientSecret: "b"},
		{Issuer: "idp.test", ClientID: "a", ClientSecret: "b"},
		{Issuer: "ftp://idp.test", ClientID: "a", ClientSecret: "b"},
		{Issuer: "https://idp.test/?x=1", ClientID: "a", ClientSecret: "b"},
		{Issuer: "https://idp.test", ClientSecret: "b"},
		{Issuer: "https://idp.test", ClientID: "a"},
	} {
		if _, err := NewOIDCProvider(bad, testPanelURL); err == nil {
			t.Errorf("%+v was taken", bad)
		}
	}
	p, err := NewOIDCProvider(OIDCConfig{Issuer: "https://idp.test/application/o/avsys/", ClientID: "a", ClientSecret: "b", Scopes: []string{"email", "groups", "email"}}, testPanelURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.config.Scopes, " "); got != "openid email groups" {
		t.Errorf("scopes = %q", got)
	}
	if p.Name != "SSO" || p.ID != OIDCProviderID || p.Issuer() != "https://idp.test/application/o/avsys/" {
		t.Errorf("provider = %+v", p)
	}
}

// GitHub and Google are sent a PKCE challenge and given its verifier when
// the code is redeemed; Discord, which does not document PKCE, is sent
// neither.
func TestNamedProvidersPKCE(t *testing.T) {
	for _, id := range ProviderIDs {
		t.Run(id, func(t *testing.T) {
			var verifier string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					r.ParseForm()
					verifier = r.PostForm.Get("code_verifier")
					w.Write([]byte(`{"access_token":"x","token_type":"Bearer"}`))
					return
				}
				w.Write([]byte(`{"id":1,"sub":"1","login":"steve"}`))
			}))
			defer api.Close()
			p, err := NewProvider(id, "client", "secret", testPanelURL)
			if err != nil {
				t.Fatal(err)
			}
			p.config.Endpoint = oauth2.Endpoint{AuthURL: api.URL + "/authorize", TokenURL: api.URL + "/token"}
			p.userURL = api.URL + "/user"
			f, to, err := p.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			q, _ := url.Parse(to)
			challenge := q.Query().Get("code_challenge")
			if _, err := p.Exchange(context.Background(), "code", f); err != nil {
				t.Fatal(err)
			}
			pkce := id != "discord"
			if pkce && (challenge != oauth2.S256ChallengeFromVerifier(verifier) || q.Query().Get("code_challenge_method") != "S256" || verifier == "") {
				t.Errorf("challenge %q, verifier %q", challenge, verifier)
			}
			if !pkce && (challenge != "" || verifier != "") {
				t.Errorf("sent PKCE: challenge %q, verifier %q", challenge, verifier)
			}
		})
	}
}
