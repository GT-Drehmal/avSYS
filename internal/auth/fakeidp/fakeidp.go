// Package fakeidp is an OpenID Connect provider for tests: it serves
// discovery, a key set, an authorization endpoint that signs in whoever it
// is told to, a token endpoint and user info, and signs ID tokens with keys
// of its own. It holds a client to the rules a careful provider does: the
// redirect address must be the registered one, PKCE (S256) is required, and
// a code is redeemed once.
//
// What a test wants to go wrong is set on it: claims to change before a
// token is signed, a key it signs with but does not publish, a new key.
package fakeidp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// IDP is a running provider. Its fields may be changed between sign-ins.
type IDP struct {
	// URL is the provider's issuer, and where it listens.
	URL          string
	ClientID     string
	ClientSecret string
	// RedirectURL is the one callback address the client may use.
	RedirectURL string

	mu sync.Mutex
	// Claims, if set, changes the claims of each ID token before it is
	// signed.
	Claims func(claims map[string]any)
	// Users holds extra claims for people, by login, in their ID tokens
	// and user info.
	Users map[string]map[string]any
	// UserInfoOnly lists claims given in the user info but left out of the
	// ID token, as some providers do.
	UserInfoOnly []string
	// UserInfo, if set, changes the claims of the user info.
	UserInfo func(claims map[string]any)
	// DiscoveryIssuer, if set, is the issuer the discovery document gives
	// instead of URL.
	DiscoveryIssuer string
	// SignWithUnpublished signs tokens with a key the key set does not list.
	SignWithUnpublished bool

	keys        []key // published; the last is the one tokens are signed with
	unpublished key
	codes       map[string]*grant
	tokens      map[string]string // access token: login
	keyFetches  int
	server      *http.Server
}

type key struct {
	id   string
	priv crypto.Signer
}

type grant struct {
	login, clientID, redirect, challenge, nonce string
	expires                                     time.Time
	used                                        bool
}

func newKey() key {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key{id: random()[:12], priv: priv}
}

func random() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Start listens on a free port of 127.0.0.1. redirectURL is the client's
// callback address.
func Start(clientID, clientSecret, redirectURL string) (*IDP, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	idp := &IDP{
		URL: "http://" + ln.Addr().String(), ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		Users: map[string]map[string]any{}, keys: []key{newKey()}, unpublished: newKey(),
		codes: map[string]*grant{}, tokens: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", idp.discovery)
	mux.HandleFunc("GET /keys", idp.keySet)
	mux.HandleFunc("GET /authorize", idp.authorizeForm)
	mux.HandleFunc("POST /authorize", idp.authorize)
	mux.HandleFunc("POST /token", idp.token)
	mux.HandleFunc("GET /userinfo", idp.userinfo)
	idp.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go idp.server.Serve(ln)
	return idp, nil
}

func (i *IDP) Close() error { return i.server.Close() }

// Rotate makes a new signing key. With keepOld the old ones stay published,
// as a provider does while tokens signed with them may still be about.
func (i *IDP) Rotate(keepOld bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if keepOld {
		i.keys = append(i.keys, newKey())
	} else {
		i.keys = []key{newKey()}
	}
}

// KeyFetches is how many times the key set has been asked for.
func (i *IDP) KeyFetches() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.keyFetches
}

// Set changes the provider's settings under its lock.
func (i *IDP) Set(f func(i *IDP)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	f(i)
}

func (i *IDP) discovery(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	issuer := i.URL
	if i.DiscoveryIssuer != "" {
		issuer = i.DiscoveryIssuer
	}
	i.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                i.URL + "/authorize",
		"token_endpoint":                        i.URL + "/token",
		"userinfo_endpoint":                     i.URL + "/userinfo",
		"jwks_uri":                              i.URL + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (i *IDP) keySet(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.keyFetches++
	set := jose.JSONWebKeySet{}
	for _, k := range i.keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: k.priv.Public(), KeyID: k.id, Algorithm: "RS256", Use: "sig"})
	}
	writeJSON(w, http.StatusOK, set)
}

// checkRequest is what a careful provider checks of an authorization
// request before showing its sign-in page.
func (i *IDP) checkRequest(q url.Values) error {
	switch {
	case q.Get("response_type") != "code":
		return errors.New("response_type must be code")
	case q.Get("client_id") != i.ClientID:
		return errors.New("unknown client")
	case q.Get("redirect_uri") != i.RedirectURL:
		return errors.New("redirect_uri is not the registered one")
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		return errors.New("PKCE with S256 is required")
	case !strings.Contains(" "+q.Get("scope")+" ", " openid "):
		return errors.New("the openid scope is required")
	}
	return nil
}

var page = template.Must(template.New("").Parse(`<!doctype html>
<title>Test ID</title>
<form method="post">
{{range $k, $v := .}}<input type="hidden" name="{{$k}}" value="{{index $v 0}}">
{{end}}<label>Username <input name="login" autofocus></label>
<button>Continue</button>
</form>`))

func (i *IDP) authorizeForm(w http.ResponseWriter, r *http.Request) {
	if err := i.checkRequest(r.URL.Query()); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page.Execute(w, r.URL.Query())
}

func (i *IDP) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	to, err := i.Approve(r.PostForm, r.PostForm.Get("login"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// Approve signs login in for an authorization request, given as its query,
// and returns the callback address with the code.
func (i *IDP) Approve(q url.Values, login string) (string, error) {
	if err := i.checkRequest(q); err != nil {
		return "", err
	}
	if login == "" {
		return "", errors.New("no login")
	}
	code := random()
	i.mu.Lock()
	i.codes[code] = &grant{
		login: login, clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"),
		challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), expires: time.Now().Add(time.Minute),
	}
	i.mu.Unlock()
	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	return q.Get("redirect_uri") + "?" + back.Encode(), nil
}

// ApproveURL is Approve for a whole authorization address.
func (i *IDP) ApproveURL(authURL, login string) (string, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(authURL, i.URL+"/authorize?") {
		return "", fmt.Errorf("%s is not this provider's", authURL)
	}
	return i.Approve(u.Query(), login)
}

func tokenError(w http.ResponseWriter, code, description string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": description})
}

func (i *IDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request", err.Error())
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != i.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(i.ClientSecret)) != 1 {
		w.Header().Set("WWW-Authenticate", "Basic")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, "unsupported_grant_type", "")
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	g := i.codes[r.PostForm.Get("code")]
	switch {
	case g == nil || time.Now().After(g.expires):
		tokenError(w, "invalid_grant", "unknown code")
		return
	case g.used:
		tokenError(w, "invalid_grant", "code already used")
		return
	}
	g.used = true
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case g.clientID != id:
		tokenError(w, "invalid_grant", "code issued to another client")
		return
	case r.PostForm.Get("redirect_uri") != g.redirect:
		tokenError(w, "invalid_grant", "redirect_uri differs")
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		tokenError(w, "invalid_grant", "PKCE verifier does not match")
		return
	}
	claims := i.claims(g.login)
	for _, k := range i.UserInfoOnly {
		delete(claims, k)
	}
	now := time.Now()
	claims["iss"], claims["aud"], claims["iat"], claims["exp"] = i.URL, i.ClientID, now.Unix(), now.Add(5*time.Minute).Unix()
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	if i.Claims != nil {
		i.Claims(claims)
	}
	signer := i.keys[len(i.keys)-1]
	if i.SignWithUnpublished {
		signer = i.unpublished
	}
	idToken, err := sign(signer, claims)
	if err != nil {
		tokenError(w, "server_error", err.Error())
		return
	}
	access := random()
	i.tokens[access] = g.login
	writeJSON(w, http.StatusOK, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

// claims are a person's own: their sub is their login.
func (i *IDP) claims(login string) map[string]any {
	c := map[string]any{
		"sub": login, "preferred_username": login, "name": strings.ToUpper(login[:1]) + login[1:],
		"email": login + "@idp.test", "email_verified": true,
	}
	for k, v := range i.Users[login] {
		c[k] = v
	}
	return c
}

func (i *IDP) userinfo(w http.ResponseWriter, r *http.Request) {
	access, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	i.mu.Lock()
	defer i.mu.Unlock()
	login, ok := i.tokens[access]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	claims := i.claims(login)
	if i.UserInfo != nil {
		i.UserInfo(claims)
	}
	writeJSON(w, http.StatusOK, claims)
}

func sign(k key, claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k.priv}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", k.id))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
