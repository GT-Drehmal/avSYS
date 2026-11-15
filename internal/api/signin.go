package api

import (
	"context"
	"errors"
	"strings"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// canSignInWith reports whether a linked provider account still signs in:
// its provider is configured, and, for OpenID Connect, it is from the
// issuer configured now.
func canSignInWith(providers map[string]*auth.Provider, id store.Identity) bool {
	p := providers[id.Provider]
	if p == nil {
		return false
	}
	if issuer := p.Issuer(); issuer != "" {
		return strings.HasPrefix(id.Subject, auth.OIDCSubject(issuer, ""))
	}
	return true
}

// CheckSignIn is the check, with passwords off, that the panel can still be
// signed in to and run. It returns the administrators who cannot sign in,
// having no linked account at a configured provider, to be warned about;
// and an error if no administrator can, or there is no provider at all.
// With no account yet, the first is made through a provider (see
// Config.SetupCode).
func CheckSignIn(ctx context.Context, st *store.Store, providers map[string]*auth.Provider) (lockedOut []string, err error) {
	if len(providers) == 0 {
		return nil, errors.New("passwords are off and no sign-in provider is configured, so no one could sign in: configure one, or turn passwords back on")
	}
	admins, err := st.Admins(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range admins {
		ids, err := st.Identities(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		can := false
		for _, id := range ids {
			can = can || canSignInWith(providers, id)
		}
		if !can {
			lockedOut = append(lockedOut, a.Username)
		}
	}
	if len(admins) > 0 && len(lockedOut) == len(admins) {
		return lockedOut, errors.New("passwords are off and no administrator has a linked account at a configured provider, so no administrator could sign in: start with passwords on, link a provider account on the Account page, and turn passwords off again")
	}
	return lockedOut, nil
}
