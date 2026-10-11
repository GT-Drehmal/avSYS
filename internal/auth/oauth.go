package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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

// Provider is one configured OAuth sign-in option.
type Provider struct {
	ID      string
	Name    string
	config  oauth2.Config
	userURL string
	parse   func(raw map[string]any) Profile
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
		userURL: "https://api.github.com/user",
		parse: func(r map[string]any) Profile {
			return Profile{Subject: str(r["id"]), Login: str(r["login"]), Name: str(r["name"]), Email: str(r["email"]), AvatarURL: str(r["avatar_url"])}
		},
	},
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
		userURL: "https://openidconnect.googleapis.com/v1/userinfo",
		parse: func(r map[string]any) Profile {
			return Profile{Subject: str(r["sub"]), Name: str(r["name"]), Email: str(r["email"]), AvatarURL: str(r["picture"])}
		},
	},
}

// ProviderIDs lists the providers avSYS knows how to talk to.
var ProviderIDs = []string{"github", "discord", "google"}

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
	p.config.RedirectURL = publicURL + "/api/v1/auth/" + id + "/callback"
	return &p, nil
}

func (p *Provider) AuthURL(state string) string { return p.config.AuthCodeURL(state) }

// Exchange trades an authorization code for the account's profile.
func (p *Provider) Exchange(ctx context.Context, code string) (Profile, error) {
	token, err := p.config.Exchange(ctx, code)
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
