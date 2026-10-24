package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// panel is the API running in-process against a fake cluster and a temporary
// database, with one person in each standing a request can come from.
//
// Organization "alpha" has an owner, an admin, an operator and a viewer.
// "outsider" owns organization "beta" and is no part of alpha. "root" is an
// instance administrator who belongs to neither.
type panel struct {
	t      *testing.T
	srv    *Server
	h      http.Handler
	users  map[string]*store.User
	tokens map[string]string
}

var everyone = []string{"", "outsider", "viewer", "operator", "admin", "owner", "root"}

func newPanel(t *testing.T) *panel {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&avsysv1.MinecraftServer{}, router.HostnameIndex, router.IndexHostname).
		// As in a real cluster, status is written separately from the rest.
		WithStatusSubresource(&avsysv1.MinecraftServer{}).
		Build()

	p := &panel{
		t: t,
		srv: &Server{
			Config: Config{NamespacePrefix: "mc-", PublicURL: "http://panel.test", PackDir: t.TempDir()},
			Store:  db,
			Client: cluster,
			Events: NewHub(),
			Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			// No restic here: these tests are about who gets as far as being
			// told so. internal/backup tests the snapshots themselves.
			Backups: &backup.Manager{Dir: t.TempDir(), Restic: "restic-is-not-installed", Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		},
		users:  map[string]*store.User{},
		tokens: map[string]string{},
	}
	p.h = p.srv.Handler()
	// Nothing here reaches the internet: what the panel looks up elsewhere
	// is answered from offline, which tests fill in as they need.
	p.srv.catalog.http.Transport = offline{}

	for _, name := range []string{"root", "owner", "admin", "operator", "viewer", "outsider"} {
		u := &store.User{Username: name, IsAdmin: name == "root"}
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		secret, err := db.CreateAPIToken(ctx, &store.APIToken{UserID: u.ID, Name: "test"})
		if err != nil {
			t.Fatal(err)
		}
		p.users[name], p.tokens[name] = u, secret
	}

	alpha := &store.Org{Name: "Alpha", Slug: "alpha"}
	if err := db.CreateOrg(ctx, alpha, p.users["owner"].ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admin", "operator", "viewer"} {
		p.join(alpha, name, store.Role(name))
	}
	if err := db.CreateOrg(ctx, &store.Org{Name: "Beta", Slug: "beta"}, p.users["outsider"].ID); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"alpha", "beta"} {
		if err := p.srv.ensureNamespace(ctx, slug); err != nil {
			t.Fatal(err)
		}
	}
	p.addServer("alpha", "world", "play.alpha.example")
	return p
}

// join adds a user to an organization the way people join: through an invite.
func (p *panel) join(org *store.Org, user string, role store.Role) {
	p.t.Helper()
	ctx := context.Background()
	inv := &store.Invite{OrgID: org.ID, Role: role, CreatedBy: "test"}
	if err := p.srv.Store.CreateInvite(ctx, inv); err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.srv.Store.AcceptInvite(ctx, inv.Token, p.users[user].ID); err != nil {
		p.t.Fatal(err)
	}
}

func (p *panel) addServer(org, name, hostname string) {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace(org), Name: name},
		Spec: avsysv1.MinecraftServerSpec{
			EULA: true, Version: "1.21.4", Loader: avsysv1.LoaderVanilla, State: avsysv1.StateStopped,
			Expose: avsysv1.ExposeSpec{Type: avsysv1.ExposeRouter, Hostname: hostname},
		},
	}
	p.srv.applyDefaults(org, name, &mc.Spec)
	if err := p.srv.Client.Create(context.Background(), mc); err != nil {
		p.t.Fatal(err)
	}
	p.setPhase(org, name, avsysv1.PhaseStopped)
}

// setPhase stands in for the controller, which is not running here.
func (p *panel) setPhase(org, name string, phase avsysv1.Phase) {
	p.t.Helper()
	ctx := context.Background()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: p.srv.Namespace(org), Name: name}, mc); err != nil {
		p.t.Fatal(err)
	}
	mc.Status.Phase = phase
	if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
		p.t.Fatal(err)
	}
}

// do sends a request as the named person, or signed out for "". It returns
// the status and the error code of a failure response.
func (p *panel) do(who, method, path, body string) (int, string) {
	p.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	if who != "" {
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	} else {
		// A signed-out browser still sends the header the UI always adds, so
		// that what is tested is the sign-in check and not the CSRF one.
		req.Header.Set("X-Avsys", "1")
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	var e struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return rec.Code, e.Code
}

// TestOrganizationRoutes checks every route under an organization against
// every standing a caller can have. Signed out is 401. Someone outside the
// organization gets 404, never 403, so that they cannot tell it exists. A
// member below the required role gets 403. Everyone at or above it, and the
// instance administrator, gets through.
//
// A route registered under /orgs/{org} without an entry here fails the test,
// so one cannot be added without deciding who may call it.
func TestOrganizationRoutes(t *testing.T) {
	const server = "/orgs/{org}/servers/{server}"
	routes := []struct {
		// route is the pattern as registered; path overrides the request
		// path made from it.
		route, path, body string
		min               store.Role
		// ok is the status for a caller who is allowed. Zero skips those
		// callers, for routes that go on to reach into a running server.
		ok int
	}{
		{"GET /orgs/{org}", "", "", store.RoleViewer, 200},
		{"PATCH /orgs/{org}", "", `{"name":"Alpha"}`, store.RoleOwner, 200},
		// 409: allowed, and then refused because it still has a server.
		{"DELETE /orgs/{org}", "", "", store.RoleOwner, 409},
		{"GET /orgs/{org}/members", "", "", store.RoleViewer, 200},
		{"PATCH /orgs/{org}/members/{user}", "", `{"role":"viewer"}`, store.RoleAdmin, 204},
		{"DELETE /orgs/{org}/members/{user}", "/orgs/alpha/members/no-such-user", "", store.RoleAdmin, 404},
		{"GET /orgs/{org}/invites", "", "", store.RoleAdmin, 200},
		{"POST /orgs/{org}/invites", "", `{"role":"viewer"}`, store.RoleAdmin, 201},
		{"DELETE /orgs/{org}/invites/{token}", "", "", store.RoleAdmin, 204},

		{"GET /orgs/{org}/backup-policy", "", "", store.RoleViewer, 200},
		{"PUT /orgs/{org}/backup-policy", "", `{"enabled":true,"minute":540,"keep":{"daily":7,"weekly":4,"monthly":12,"yearly":2}}`, store.RoleAdmin, 200},
		{"POST /orgs/{org}/backup-policy/run", "", "", store.RoleAdmin, 503},
		// 404: allowed, and it has no picture. 400: allowed, and what was
		// sent is not one.
		{"GET /orgs/{org}/icon", "", "", store.RoleViewer, 404},
		{"PUT /orgs/{org}/icon", "", "not a picture", store.RoleOwner, 400},
		{"DELETE /orgs/{org}/icon", "", "", store.RoleOwner, 204},
		{"GET /orgs/{org}/offsite", "", "", store.RoleViewer, 200},
		{"POST /orgs/{org}/offsite/run", "", "", store.RoleAdmin, 503},
		{"GET /orgs/{org}/domains", "", "", store.RoleViewer, 200},
		// In rootOnly: domains are given by who runs the install and its DNS.
		{"PUT /orgs/{org}/domains", "", `{"domains":["alpha.example.org"]}`, store.RoleViewer, 200},
		// The two below are in rootOnly.
		{"GET /orgs/{org}/offsite/snapshots", "", "", store.RoleViewer, 503},
		{"POST /orgs/{org}/offsite/fetch", "", "", store.RoleViewer, 503},
		{"GET /orgs/{org}/snapshots", "", "", store.RoleViewer, 200},
		{"PUT /orgs/{org}/presence", "", `{"tab":"tab-of-{who}","server":"survival"}`, store.RoleViewer, 204},
		// 503 below: allowed, and then told that this panel has no restic.
		{"GET /orgs/{org}/snapshots/{id}", "", "", store.RoleViewer, 503},
		{"GET /orgs/{org}/snapshots/{id}/icon", "", "", store.RoleViewer, 503},
		{"DELETE /orgs/{org}/snapshots/{id}", "", "", store.RoleAdmin, 503},
		{"DELETE /orgs/{org}/snapshots", "", `{"ids":["aaaa1111"]}`, store.RoleAdmin, 503},

		{"GET /orgs/{org}/servers", "", "", store.RoleViewer, 200},
		{"POST /orgs/{org}/servers", "", `{"name":"by-{who}","spec":{"eula":true,"version":"1.21.4"}}`, store.RoleAdmin, 201},
		{"POST /orgs/{org}/servers", "", `{"name":"copy-{who}","fromSnapshot":"abcdef12"}`, store.RoleAdmin, 503},
		{"GET " + server, "", "", store.RoleViewer, 200},
		{"PATCH " + server, "", `{"spec":{"displayName":"World"}}`, store.RoleAdmin, 200},
		{"DELETE " + server, "/orgs/alpha/servers/doomed-{who}", "", store.RoleAdmin, 204},
		{"POST " + server + "/{action}", "/orgs/alpha/servers/world/stop", "", store.RoleOperator, 200},
		{"POST " + server + "/{action}", "/orgs/alpha/servers/world/start", "", store.RoleOperator, 200},
		{"POST " + server + "/{action}", "/orgs/alpha/servers/world/restart", "", store.RoleOperator, 200},
		{"POST " + server + "/{action}", "/orgs/alpha/servers/world/explode", "", store.RoleOperator, 404},
		// 409: allowed, and then refused because the server is not running.
		{"POST " + server + "/console", "", `{"command":"list"}`, store.RoleOperator, 409},
		{"GET " + server + "/console", "", "", store.RoleViewer, 0},
		{"GET " + server + "/activity", "", "", store.RoleViewer, 200},
		// 502: allowed, and this panel reaches no server's files. 400: allowed,
		// and that is no log.
		{"GET " + server + "/logs", "", "", store.RoleViewer, 502},
		{"GET " + server + "/logs/file", "/orgs/alpha/servers/world/logs/file?name=../server.properties", "", store.RoleViewer, 400},
		{"GET " + server + "/snapshots", "", "", store.RoleViewer, 200},
		{"POST " + server + "/snapshots", "", "", store.RoleOperator, 503},
		{"POST " + server + "/snapshots/{id}/restore", "", "", store.RoleAdmin, 503},
		{"GET " + server + "/operation", "", "", store.RoleViewer, 200},
		{"GET " + server + "/usage", "", "", store.RoleViewer, 200},
		{"GET " + server + "/placement", "", "", store.RoleAdmin, 200},
		// 400: allowed, and there is no such node.
		{"POST " + server + "/move", "", `{"node":"no-such-node"}`, store.RoleAdmin, 400},
		{"GET " + server + "/icon", "", "", store.RoleViewer, 0},
		{"PUT " + server + "/icon", "", "not a picture", store.RoleAdmin, 400},
		{"DELETE " + server + "/icon", "", "", store.RoleAdmin, 0},
		// 400: allowed, and that is no zip. 200: allowed, and it had none.
		{"PUT " + server + "/resource-pack", "", "not a zip", store.RoleAdmin, 400},
		{"DELETE " + server + "/resource-pack", "", "", store.RoleAdmin, 200},
		{"GET " + server + "/terminal", "", "", store.RoleOperator, 0},
		{"PUT " + server + "/content", "", "x", store.RoleAdmin, 0},
		// 409: allowed, and then refused because the server is running.
		{"PUT " + server + "/import", "", "x", store.RoleAdmin, 409},
		{"GET " + server + "/files", "", "", store.RoleOperator, 0},
		{"DELETE " + server + "/files", "", "", store.RoleOperator, 0},
		{"GET " + server + "/files/content", "", "", store.RoleOperator, 0},
		{"PUT " + server + "/files/content", "", "x", store.RoleOperator, 0},
		{"POST " + server + "/files/mkdir", "", "", store.RoleOperator, 0},
		{"POST " + server + "/files/rename", "", `{"to":"/b"}`, store.RoleOperator, 0},
	}

	// rootOnly are for those who run the install: members get 403 whatever
	// their role. What is offsite under an organization's name may be a
	// deleted organization's, so it is not its members' to list or take.
	rootOnly := map[string]bool{"GET /orgs/{org}/offsite/snapshots": true, "POST /orgs/{org}/offsite/fetch": true, "PUT /orgs/{org}/domains": true}

	p := newPanel(t)
	for _, who := range everyone {
		if who != "" {
			p.addServer("alpha", "doomed-"+who, "")
		}
	}
	fill := strings.NewReplacer("{org}", "alpha", "{server}", "world", "{user}", p.users["viewer"].ID, "{token}", "none", "{id}", "abcdef12")

	tested := map[string]bool{}
	for _, route := range routes {
		tested[route.route] = true
		method, path, _ := strings.Cut(route.route, " ")
		if route.path != "" {
			path = route.path
		}
		for _, who := range everyone {
			want := route.ok
			switch {
			case who == "":
				want = http.StatusUnauthorized
			case who == "outsider":
				want = http.StatusNotFound
			case who != "root" && (rootOnly[route.route] || !store.Role(who).AtLeast(route.min)):
				want = http.StatusForbidden
			}
			if want == 0 {
				continue
			}
			target := strings.ReplaceAll(fill.Replace(path), "{who}", who)
			if got, code := p.do(who, method, target, strings.ReplaceAll(route.body, "{who}", who)); got != want {
				name := who
				if name == "" {
					name = "signed out"
				}
				t.Errorf("%s %s as %s: got %d %s, want %d", method, path, name, got, code, want)
			}
		}
	}
	for _, registered := range p.srv.routes {
		if strings.Contains(registered, " /orgs/{org}") && !tested[registered] {
			t.Errorf("%s is served but has no entry in this table", registered)
		}
	}
	for route := range tested {
		if !slices.Contains(p.srv.routes, route) {
			t.Errorf("%s is in this table but is not served", route)
		}
	}
}

// publicRoutes are the only ones that answer a caller who is not signed in.
var publicRoutes = []string{
	"GET /meta",
	"POST /auth/register",
	"POST /auth/login",
	"POST /auth/logout",
	"GET /auth/{provider}/start",
	"GET /auth/{provider}/callback",
	"GET /invites/{token}",
}

func TestEveryOtherRouteNeedsASignIn(t *testing.T) {
	p := newPanel(t)
	placeholder := regexp.MustCompile(`\{[a-z]+\}`)
	for _, route := range p.srv.routes {
		if slices.Contains(publicRoutes, route) {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		if got, _ := p.do("", method, placeholder.ReplaceAllString(path, "x"), ""); got != http.StatusUnauthorized {
			t.Errorf("%s signed out: got %d, want 401", route, got)
		}
	}
	for _, route := range publicRoutes {
		if !slices.Contains(p.srv.routes, route) {
			t.Errorf("%s is listed as public but is not served", route)
		}
	}
	// Anything else under /api/ is a plain 404, not a page of the web UI.
	if got, code := p.do("", "GET", "/nothing/here", ""); got != http.StatusNotFound || code != "not_found" {
		t.Errorf("unknown API path: got %d %q, want 404 not_found", got, code)
	}
}

func TestOnlyInstanceAdminsCreateOrganizations(t *testing.T) {
	p := newPanel(t)
	for who, want := range map[string]int{"": 401, "viewer": 403, "owner": 403, "outsider": 403, "root": 201} {
		if got, _ := p.do(who, "POST", "/orgs", `{"name":"Gamma","slug":"gamma"}`); got != want {
			t.Errorf("POST /orgs as %q: got %d, want %d", who, got, want)
		}
	}
}

// The owner role is protected from admins, and an organization always keeps
// an owner.
func TestOwnerRules(t *testing.T) {
	p := newPanel(t)
	id := func(who string) string { return p.users[who].ID }
	steps := []struct {
		name, who, method, path, body string
		want                          int
		code                          string
	}{
		{"an admin cannot make an owner", "admin", "PATCH", "/orgs/alpha/members/" + id("viewer"), `{"role":"owner"}`, 403, "forbidden"},
		{"an admin cannot invite an owner", "admin", "POST", "/orgs/alpha/invites", `{"role":"owner"}`, 403, "forbidden"},
		{"an operator cannot invite at all", "operator", "POST", "/orgs/alpha/invites", `{"role":"viewer"}`, 403, "forbidden"},
		{"an admin cannot demote an owner", "admin", "PATCH", "/orgs/alpha/members/" + id("owner"), `{"role":"viewer"}`, 403, "forbidden"},
		{"an admin cannot remove an owner", "admin", "DELETE", "/orgs/alpha/members/" + id("owner"), "", 403, "forbidden"},
		{"a viewer cannot remove someone else", "viewer", "DELETE", "/orgs/alpha/members/" + id("operator"), "", 403, "forbidden"},
		{"the last owner cannot be demoted", "owner", "PATCH", "/orgs/alpha/members/" + id("owner"), `{"role":"admin"}`, 409, "last_owner"},
		{"the last owner cannot leave", "owner", "DELETE", "/orgs/alpha/members/" + id("owner"), "", 409, "last_owner"},
		{"an unknown role is refused", "owner", "PATCH", "/orgs/alpha/members/" + id("viewer"), `{"role":"god"}`, 400, "invalid"},
		{"an owner can make a second owner", "owner", "PATCH", "/orgs/alpha/members/" + id("admin"), `{"role":"owner"}`, 204, ""},
		{"and then step down", "owner", "PATCH", "/orgs/alpha/members/" + id("owner"), `{"role":"viewer"}`, 204, ""},
		{"anyone may leave", "viewer", "DELETE", "/orgs/alpha/members/" + id("viewer"), "", 204, ""},
		{"and is then outside", "viewer", "GET", "/orgs/alpha", "", 404, "not_found"},
	}
	for _, step := range steps {
		if got, code := p.do(step.who, step.method, step.path, step.body); got != step.want || code != step.code {
			t.Errorf("%s: got %d %q, want %d %q", step.name, got, code, step.want, step.code)
		}
	}
}

// Holding a role in one organization gives nothing in another, including
// through names that happen to exist there.
func TestOrganizationsAreSeparate(t *testing.T) {
	p := newPanel(t)
	p.addServer("beta", "mine", "play.beta.example")

	// The outsider owns beta, and alpha's server is not reachable through it.
	for _, path := range []string{"/orgs/beta/servers/world", "/orgs/alpha/servers/world", "/orgs/alpha/servers/mine"} {
		if got, _ := p.do("outsider", "GET", path, ""); got != http.StatusNotFound {
			t.Errorf("GET %s as beta's owner: got %d, want 404", path, got)
		}
	}
	if got, _ := p.do("outsider", "GET", "/orgs/beta/servers/mine", ""); got != http.StatusOK {
		t.Errorf("beta's owner cannot read beta's own server: %d", got)
	}
	// A hostname is one player-facing name across the whole install.
	body := `{"name":"copycat","spec":{"eula":true,"version":"1.21.4","expose":{"type":"Router","hostname":"play.alpha.example"}}}`
	if got, code := p.do("outsider", "POST", "/orgs/beta/servers", body); got != http.StatusConflict || code != "hostname_taken" {
		t.Errorf("claiming another organization's hostname: got %d %q, want 409 hostname_taken", got, code)
	}

	// /me lists only the caller's own organizations.
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["outsider"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	var me struct{ Orgs []struct{ Slug string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Orgs) != 1 || me.Orgs[0].Slug != "beta" {
		t.Errorf("/me for beta's owner lists %v, want only beta", me.Orgs)
	}
}

// The SSH gateway asks this before opening a shell or a file transfer.
func TestAuthorizeWorkspace(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	for who, allowed := range map[string]bool{"viewer": false, "operator": true, "admin": true, "owner": true, "outsider": false, "root": true} {
		ns, err := p.srv.AuthorizeWorkspace(ctx, p.users[who], "alpha", "world")
		if allowed && (err != nil || ns != "mc-alpha") {
			t.Errorf("%s: got %q, %v; want access to mc-alpha", who, ns, err)
		}
		if !allowed && err == nil {
			t.Errorf("%s was given a shell", who)
		}
	}
	// The refusal reads the same whether the server, the organization or the
	// permission is what is missing.
	_, noRole := p.srv.AuthorizeWorkspace(ctx, p.users["viewer"], "alpha", "world")
	_, noOrg := p.srv.AuthorizeWorkspace(ctx, p.users["outsider"], "alpha", "world")
	_, noServer := p.srv.AuthorizeWorkspace(ctx, p.users["owner"], "alpha", "ghost")
	if noRole == nil || noOrg == nil || noServer == nil {
		t.Fatal("expected all three to be refused")
	}
	if noRole.Error() != noOrg.Error() {
		t.Errorf("refusals differ: %q vs %q", noRole, noOrg)
	}
	if !strings.Contains(noServer.Error(), "ghost.alpha") {
		t.Errorf("refusal does not name the target: %q", noServer)
	}
}

func TestTokensAndSessions(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()

	// A token stops working the moment it is deleted.
	tokens, err := p.srv.Store.APITokens(ctx, p.users["viewer"].ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens: %v, %v", tokens, err)
	}
	if got, _ := p.do("viewer", "GET", "/me", ""); got != http.StatusOK {
		t.Fatalf("token before deletion: %d", got)
	}
	if err := p.srv.Store.DeleteAPIToken(ctx, p.users["viewer"].ID, tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.do("viewer", "GET", "/me", ""); got != http.StatusUnauthorized {
		t.Errorf("deleted token: got %d, want 401", got)
	}
	p.tokens["nobody"] = "avsys_not-a-real-token"
	if got, _ := p.do("nobody", "GET", "/me", ""); got != http.StatusUnauthorized {
		t.Errorf("made-up token: got %d, want 401", got)
	}

	// A cookie session can read, but cannot change anything without the
	// header a cross-site form cannot send.
	session, err := p.srv.Store.CreateSession(ctx, p.users["owner"].ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body string, header bool) (int, string) {
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		if header {
			req.Header.Set("X-Avsys", "1")
		}
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var e struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return rec.Code, e.Code
	}
	if got, _ := send("GET", "/orgs/alpha", "", false); got != http.StatusOK {
		t.Errorf("cookie read: got %d, want 200", got)
	}
	if got, code := send("POST", "/orgs/alpha/servers/world/stop", "", false); got != http.StatusForbidden || code != "csrf" {
		t.Errorf("cookie write without the header: got %d %q, want 403 csrf", got, code)
	}
	if got, _ := send("POST", "/orgs/alpha/servers/world/stop", "", true); got != http.StatusOK {
		t.Errorf("cookie write with the header: got %d, want 200", got)
	}
	if got, _ := send("POST", "/auth/logout", "", true); got >= 300 {
		t.Errorf("logout: got %d", got)
	}
	if got, _ := send("GET", "/orgs/alpha", "", false); got != http.StatusUnauthorized {
		t.Errorf("cookie after logout: got %d, want 401", got)
	}
}

func TestSignupNeedsAnInvite(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	register := func(body string) (int, string) { return p.do("", "POST", "/auth/register", body) }

	if got, code := register(`{"username":"walkin","password":"correct horse"}`); got != http.StatusForbidden || code != "signup_closed" {
		t.Errorf("signup without an invite: got %d %q, want 403 signup_closed", got, code)
	}
	if got, code := register(`{"username":"walkin","password":"correct horse","invite":"made-up"}`); got != http.StatusForbidden || code != "signup_closed" {
		t.Errorf("signup with a made-up invite: got %d %q, want 403 signup_closed", got, code)
	}

	alpha, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	inv := &store.Invite{OrgID: alpha.ID, Role: store.RoleOperator, CreatedBy: "test", MaxUses: 1}
	if err := p.srv.Store.CreateInvite(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if got, code := register(`{"username":"guest","password":"correct horse","invite":"` + inv.Token + `"}`); got != http.StatusCreated {
		t.Fatalf("signup with an invite: got %d %q, want 201", got, code)
	}
	guest, err := p.srv.Store.UserByUsername(ctx, "guest")
	if err != nil {
		t.Fatal(err)
	}
	if guest.IsAdmin {
		t.Error("an invited user became an instance administrator")
	}
	if role, err := p.srv.Store.RoleIn(ctx, alpha.ID, guest.ID); err != nil || role != store.RoleOperator {
		t.Errorf("invited user's role: %q, %v; want operator", role, err)
	}
	// The invite was for one person.
	if got, code := register(`{"username":"second","password":"correct horse","invite":"` + inv.Token + `"}`); got != http.StatusForbidden || code != "signup_closed" {
		t.Errorf("reusing a spent invite: got %d %q, want 403 signup_closed", got, code)
	}
	if got, code := p.do("outsider", "POST", "/invites/"+inv.Token+"/accept", ""); got != http.StatusGone || code != "invite_unusable" {
		t.Errorf("accepting a spent invite: got %d %q, want 410 invite_unusable", got, code)
	}
}

// A live event stream carries only the caller's own organizations.
func TestEventsStayInsideTheOrganization(t *testing.T) {
	p := newPanel(t)
	ts := httptest.NewServer(p.h)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("event stream: %d", res.StatusCode)
	}
	// The handler subscribes just before it writes its headers.
	subscribed := func() bool {
		p.srv.Events.mu.Lock()
		defer p.srv.Events.mu.Unlock()
		return len(p.srv.Events.subs) == 1
	}
	for !subscribed() {
		if ctx.Err() != nil {
			t.Fatal("the stream never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	p.srv.Events.Publish(Event{Type: "server.updated", Namespace: "mc-beta", Data: "from beta"})
	p.srv.Events.Publish(Event{Type: "server.updated", Namespace: "mc-gamma", Data: "from nowhere"})
	p.srv.Events.Publish(Event{Type: "server.updated", Namespace: "mc-alpha", Data: "from alpha"})

	lines := bufio.NewScanner(res.Body)
	for lines.Scan() {
		data, ok := strings.CutPrefix(lines.Text(), "data: ")
		// The first say whether the cluster can be reached, to everyone,
		// and who is on its own organization's servers.
		if !ok || strings.HasPrefix(data, `{"reachable"`) || strings.HasPrefix(data, `{"org":"alpha"`) {
			continue
		}
		if data != `"from alpha"` {
			t.Fatalf("alpha's viewer received %s", data)
		}
		return
	}
	t.Fatalf("no event arrived: %v", lines.Err())
}
