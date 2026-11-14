package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

// Profile is what avSYS takes from a provider account.
type Profile struct {
	// Subject is the provider's stable account ID.
	Subject   string
	Login     string
	Name      string
	Email     string
	AvatarURL string
}

// Provider is one configured sign-in option: an OAuth provider whose
// profile is read from an API, or an OpenID Connect one (see oidc.go).
type Provider struct {
	ID     string
	Name   string
	config oauth2.Config
	// pkce is whether the provider is sent a code challenge and the
	// exchange its verifier. OpenID Connect providers always are.
	pkce    bool
	userURL string
	parse   func(raw map[string]any) Profile
	oidc    *oidcProvider
}

// ErrNotAllowed is returned by Exchange for a provider account that is not
// let in: one outside the groups the panel allows.
var ErrNotAllowed = errors.New("this account is not allowed to sign in here")

// httpClient is what the panel talks to providers with: never without a
// time limit.
var httpClient = &http.Client{Timeout: 20 * time.Second}

func withClient(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, httpClient)
}

func str(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	return ""
}

var known = map[string]Provider{
	"github": {
		Name:    "GitHub",
		config:  oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token"}, Scopes: []string{"read:user"}},
		pkce:    true,
		userURL: "https://api.github.com/user",
		parse: func(r map[string]any) Profile {
			return Profile{Subject: str(r["id"]), Login: str(r["login"]), Name: str(r["name"]), Email: str(r["email"]), AvatarURL: str(r["avatar_url"])}
		},
	},
	// Discord does not document PKCE, so it is not sent there.
	"discord": {
		Name:    "Discord",
		config:  oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://discord.com/oauth2/authorize", TokenURL: "https://discord.com/api/oauth2/token"}, Scopes: []string{"identify"}},
		userURL: "https://discord.com/api/users/@me",
		parse: func(r map[string]any) Profile {
			p := Profile{Subject: str(r["id"]), Login: str(r["username"]), Name: str(r["global_name"]), Email: str(r["email"])}
			if avatar := str(r["avatar"]); avatar != "" {
				p.AvatarURL = fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png?size=128", p.Subject, avatar)
			}
			return p
		},
	},
	"google": {
		Name:    "Google",
		config:  oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token"}, Scopes: []string{"openid", "profile", "email"}},
		pkce:    true,
		userURL: "https://openidconnect.googleapis.com/v1/userinfo",
		parse: func(r map[string]any) Profile {
			return Profile{Subject: str(r["sub"]), Name: str(r["name"]), Email: str(r["email"]), AvatarURL: str(r["picture"])}
		},
	},
}

// ProviderIDs lists the named providers avSYS knows how to talk to.
var ProviderIDs = []string{"github", "discord", "google"}

// Offered lists every provider in the order sign-in offers them: the
// install's own OpenID Connect provider first, then the named ones.
var Offered = append([]string{OIDCProviderID}, ProviderIDs...)

// NewProvider configures a known provider. publicURL is the panel's external
// base URL, used to build the callback address registered with the provider.
func NewProvider(id, clientID, clientSecret, publicURL string) (*Provider, error) {
	p, ok := known[id]
	if !ok {
		return nil, fmt.Errorf("unknown OAuth provider %q", id)
	}
	p.ID = id
	p.config.ClientID = clientID
	p.config.ClientSecret = clientSecret
	p.config.RedirectURL = callbackURL(publicURL, id)
	return &p, nil
}

func callbackURL(publicURL, id string) string { return publicURL + "/api/v1/auth/" + id + "/callback" }

// Flow is what one sign-in attempt keeps between sending the browser to the
// provider and the browser coming back. The panel keeps it in a cookie of
// that browser, so that only the browser that set out can finish.
type Flow struct {
	// State is sent to the provider and must come back unchanged.
	State string
	// Verifier is the PKCE code verifier: the provider is sent its hash,
	// and redeeming the code takes the verifier itself.
	Verifier string
	// Nonce is sent to an OpenID Connect provider and must be in the ID
	// token it signs.
	Nonce string
}

func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Begin starts a sign-in: the flow to keep, and the provider's address to
// send the browser to.
func (p *Provider) Begin(ctx context.Context) (Flow, string, error) {
	f := Flow{State: random()}
	config := p.config
	var opts []oauth2.AuthCodeOption
	if p.oidc != nil {
		endpoint, err := p.oidc.endpoint(ctx)
		if err != nil {
			return Flow{}, "", err
		}
		config.Endpoint = endpoint
		f.Nonce = random()
		opts = append(opts, oauth2.SetAuthURLParam("nonce", f.Nonce))
	}
	if p.pkce || p.oidc != nil {
		f.Verifier = oauth2.GenerateVerifier()
		opts = append(opts, oauth2.S256ChallengeOption(f.Verifier))
	}
	return f, config.AuthCodeURL(f.State, opts...), nil
}

// Exchange trades an authorization code for the account's profile. The
// flow is the one Begin made for the browser the code came back to.
func (p *Provider) Exchange(ctx context.Context, code string, f Flow) (Profile, error) {
	ctx = withClient(ctx)
	if p.oidc != nil {
		return p.oidc.exchange(ctx, p.config, code, f)
	}
	var opts []oauth2.AuthCodeOption
	if p.pkce {
		opts = append(opts, oauth2.VerifierOption(f.Verifier))
	}
	token, err := p.config.Exchange(ctx, code, opts...)
	if err != nil {
		return Profile{}, fmt.Errorf("exchanging code: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userURL, nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.config.Client(ctx, token).Do(req)
	if err != nil {
		return Profile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Profile{}, fmt.Errorf("%s profile request: %s", p.Name, resp.Status)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Profile{}, err
	}
	profile := p.parse(raw)
	if profile.Subject == "" {
		return Profile{}, fmt.Errorf("%s returned no account ID", p.Name)
	}
	return profile, nil
}
