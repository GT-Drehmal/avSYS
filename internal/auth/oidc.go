package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCProviderID is the ID of the OpenID Connect provider, in its sign-in
// and callback addresses and in the identities linked through it.
const OIDCProviderID = "oidc"

// OIDCConfig is an install's own OpenID Connect provider: Authentik,
// Keycloak, Authelia, Google Workspace, Entra ID and the like.
type OIDCConfig struct {
	// Issuer is the provider's issuer URL, exactly as its discovery
	// document gives it: <Issuer>/.well-known/openid-configuration.
	Issuer       string
	ClientID     string
	ClientSecret string
	// Name is what the sign-in button calls the provider.
	Name string
	// Scopes asked for; "openid" is always among them.
	Scopes []string
	// AllowedGroups, if any, are the groups a person must be in one of to
	// sign in, as listed in the GroupsClaim of their ID token (or, failing
	// that, of the provider's user info).
	AllowedGroups []string
	GroupsClaim   string
}

// oidcProvider is the OpenID Connect side of a Provider. What discovery
// finds is looked up on first use and kept, so that a provider that is down
// when the panel starts does not stop the panel.
type oidcProvider struct {
	cfg OIDCConfig

	mu       sync.Mutex
	disc     *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

// NewOIDCProvider configures the OpenID Connect provider. publicURL is the
// panel's external base URL, which the callback address is built from.
func NewOIDCProvider(cfg OIDCConfig, publicURL string) (*Provider, error) {
	u, err := url.Parse(cfg.Issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the OpenID Connect issuer must be an http(s) URL with no query, not %q", cfg.Issuer)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("OpenID Connect sign-in needs a client ID and a client secret")
	}
	if cfg.Name == "" {
		cfg.Name = "SSO"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	scopes := []string{oidc.ScopeOpenID}
	for _, s := range cfg.Scopes {
		if s != "" && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	cfg.Scopes = scopes
	return &Provider{
		ID:   OIDCProviderID,
		Name: cfg.Name,
		config: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			RedirectURL: callbackURL(publicURL, OIDCProviderID), Scopes: scopes,
		},
		oidc: &oidcProvider{cfg: cfg},
	}, nil
}

// OIDCSubject is the identity subject the panel keeps for an OpenID Connect
// account: the issuer and the account's "sub", which is only unique within
// its issuer. Another issuer's accounts never match it, even with the same
// sub.
func OIDCSubject(issuer, sub string) string { return issuer + " " + sub }

// Issuer is the OpenID Connect provider's issuer, or "" for another kind of
// provider.
func (p *Provider) Issuer() string {
	if p.oidc == nil {
		return ""
	}
	return p.oidc.cfg.Issuer
}

// Discover looks the provider up now, rather than at the first sign-in. For
// other kinds of provider it does nothing.
func (p *Provider) Discover(ctx context.Context) error {
	if p.oidc == nil {
		return nil
	}
	_, _, err := p.oidc.discover(ctx)
	return err
}

func (o *oidcProvider) discover(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.disc != nil {
		return o.disc, o.verifier, nil
	}
	// The key set the verifier keeps outlives this request: it is given the
	// client, not the request's context.
	ctx, cancel := context.WithTimeout(oidc.ClientContext(context.WithoutCancel(ctx), httpClient), 15*time.Second)
	defer cancel()
	disc, err := oidc.NewProvider(ctx, o.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("discovering the provider: %w", err)
	}
	var endpoints struct {
		Auth  string `json:"authorization_endpoint"`
		Token string `json:"token_endpoint"`
		JWKS  string `json:"jwks_uri"`
	}
	if err := disc.Claims(&endpoints); err != nil {
		return nil, nil, err
	}
	if endpoints.Auth == "" || endpoints.Token == "" || endpoints.JWKS == "" {
		return nil, nil, errors.New("the provider does not give an authorization endpoint, a token endpoint and a key set")
	}
	// The signing algorithms are the ones the provider lists that the
	// library supports, never "none"; the audience must include the client.
	o.disc, o.verifier = disc, disc.Verifier(&oidc.Config{ClientID: o.cfg.ClientID})
	return o.disc, o.verifier, nil
}

func (o *oidcProvider) endpoint(ctx context.Context) (oauth2.Endpoint, error) {
	disc, _, err := o.discover(ctx)
	if err != nil {
		return oauth2.Endpoint{}, err
	}
	return disc.Endpoint(), nil
}

// exchange redeems the code with the PKCE verifier, and takes the person
// from the ID token that comes with the access token, once it is shown to
// be the provider's, for this client, current, and for this sign-in.
func (o *oidcProvider) exchange(ctx context.Context, config oauth2.Config, code string, f Flow) (Profile, error) {
	disc, verifier, err := o.discover(ctx)
	if err != nil {
		return Profile{}, err
	}
	if f.Verifier == "" || f.Nonce == "" {
		return Profile{}, errors.New("the sign-in has no PKCE verifier or nonce")
	}
	config.Endpoint = disc.Endpoint()
	token, err := config.Exchange(oidc.ClientContext(ctx, httpClient), code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return Profile{}, fmt.Errorf("exchanging code: %w", err)
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return Profile{}, errors.New("the token response has no ID token")
	}
	// The signature (against the provider's published keys, fetched again
	// for a key it has not seen), the issuer, the audience and the expiry.
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return Profile{}, fmt.Errorf("verifying the ID token: %w", err)
	}
	// That the token was made for this sign-in, and not another.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(f.Nonce)) != 1 {
		return Profile{}, errors.New("the ID token's nonce does not match the sign-in's")
	}
	if idToken.Subject == "" {
		return Profile{}, errors.New("the ID token has no subject")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return Profile{}, err
	}
	// A token for several audiences says which of them it was given to.
	azp, hasAzp := claims["azp"].(string)
	if (hasAzp || len(idToken.Audience) > 1) && azp != o.cfg.ClientID {
		return Profile{}, fmt.Errorf("the ID token was issued to %q, not this client", azp)
	}

	// Some providers put the profile only in the user info. It is taken
	// from there for what the ID token leaves out, and only if it is about
	// the same account.
	_, hasGroups := claims[o.cfg.GroupsClaim]
	needGroups := len(o.cfg.AllowedGroups) > 0 && !hasGroups
	if claims["name"] == nil || claims["preferred_username"] == nil || claims["email"] == nil || needGroups {
		info, err := disc.UserInfo(oidc.ClientContext(ctx, httpClient), oauth2.StaticTokenSource(token))
		switch {
		case err == nil && info.Subject != idToken.Subject:
			return Profile{}, errors.New("the user info is about another account than the ID token")
		case err == nil:
			var more map[string]any
			if err := info.Claims(&more); err == nil {
				for k, v := range more {
					if _, ok := claims[k]; !ok {
						claims[k] = v
					}
				}
			}
		case needGroups:
			return Profile{}, fmt.Errorf("reading the user info for groups: %w", err)
		}
	}
	if len(o.cfg.AllowedGroups) > 0 && !inGroups(claims[o.cfg.GroupsClaim], o.cfg.AllowedGroups) {
		return Profile{}, ErrNotAllowed
	}

	p := Profile{
		Subject: OIDCSubject(o.cfg.Issuer, idToken.Subject),
		Login:   str(claims["preferred_username"]),
		Name:    str(claims["name"]),
	}
	// An address the provider says it has not checked is not kept.
	if verified, ok := claims["email_verified"].(bool); !ok || verified {
		p.Email = str(claims["email"])
	}
	if picture := str(claims["picture"]); strings.HasPrefix(picture, "https://") || strings.HasPrefix(picture, "http://") {
		p.AvatarURL = picture
	}
	return p, nil
}

// inGroups reports whether a groups claim, a list or a single name, holds
// any of the allowed groups.
func inGroups(claim any, allowed []string) bool {
	var groups []string
	switch v := claim.(type) {
	case string:
		groups = []string{v}
	case []any:
		for _, g := range v {
			if s, ok := g.(string); ok {
				groups = append(groups, s)
			}
		}
	}
	for _, g := range groups {
		if slices.Contains(allowed, g) {
			return true
		}
	}
	return false
}
