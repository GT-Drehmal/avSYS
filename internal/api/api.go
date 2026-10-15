// Package api is the panel's HTTP API and the server for its web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

type Config struct {
	// PublicURL is the address people open the panel at, without a trailing
	// slash. OAuth callbacks and invite links are built from it.
	PublicURL string
	// SSHHost and SSHPort are where the SSH gateway is reachable from outside.
	SSHHost string
	SSHPort int
	// RouterDomain, if set, has a wildcard DNS record pointing at the router,
	// and new servers default to <server>.<org>.<RouterDomain>.
	RouterDomain string
	// NamespacePrefix is prepended to an organization's slug to name its namespace.
	NamespacePrefix string
	// AllowSignups lets anyone create an account. Otherwise accounts are
	// created by the first-run setup and by accepting an invite.
	AllowSignups bool
	Providers    map[string]*auth.Provider
	Version      string
}

type Server struct {
	Config Config
	// contentLocks holds a lock per server, by namespace/name: see lockContent.
	contentLocks sync.Map
	// hostnames holds the panel to one change at a time of who may have
	// which hostname: a domain claimed, or a server given a name. A claim
	// and a server taking a name under it then cannot pass each other.
	hostnames sync.Mutex
	Store     *store.Store
	Client    client.Client
	// Reader reads the cluster as it is now, where Client may lag a change
	// by a moment. It is optional: Client is used where there is none.
	Reader  client.Reader
	Kube    kubernetes.Interface
	Dialer  kube.PodDialer
	Gateway *sshgw.Gateway
	// Backups keeps each organization's snapshot repository.
	Backups *backup.Manager
	Events  *Hub
	Log     *slog.Logger
	// Static is the built web UI. Nil serves the API only.
	Static fs.FS
	// Upstream is how the panel reaches the services it looks things up in:
	// Modrinth, Mojang, and the sites that list each kind of server's builds.
	// Nil is the internet. The browser tests answer from files instead, so
	// that they neither depend on those services nor load them.
	Upstream http.RoundTripper

	catalog    *catalog
	workspaces *workspacePool
	ops        operations
	schedule   schedule
	offsite    offsite
	autoOps    autoOps
	// serverIcons holds each server's icon for a short while.
	serverIcons serverIcons
	// snapshotIcons holds the icons read out of snapshots, which never change.
	snapshotIcons snapshotIcons
	// consoles holds one lock per server, taken to type at its console.
	consoles sync.Map
	// routes lists what Handler registered, as "METHOD /path". Tests use it
	// to check that no route is left without a decision on who may call it.
	routes []string
}

// Namespace returns the namespace that holds an organization's servers.
func (s *Server) Namespace(orgSlug string) string { return s.Config.NamespacePrefix + orgSlug }

// orgOfNamespace is the inverse of Namespace; ok is false for namespaces that
// do not belong to an organization.
func (s *Server) orgOfNamespace(ns string) (slug string, ok bool) {
	return strings.CutPrefix(ns, s.Config.NamespacePrefix)
}

// Error is a failure the client is meant to see.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func errBadRequest(format string, args ...any) error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid", Message: fmt.Sprintf(format, args...)}
}

var (
	errUnauthorized = &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "Sign in to continue."}
	errForbidden    = &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Your role does not allow this."}
	errNotFound     = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "Not found."}
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	switch {
	case errors.As(err, &e):
	case errors.Is(err, store.ErrNotFound), apierrors.IsNotFound(err):
		e = errNotFound
	case errors.Is(err, store.ErrConflict), apierrors.IsAlreadyExists(err):
		e = &Error{Status: http.StatusConflict, Code: "conflict", Message: "That name is already taken."}
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		e = &Error{Status: http.StatusBadRequest, Code: "invalid", Message: err.Error()}
	case errors.Is(err, context.Canceled):
		return
	default:
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		e = &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "Something went wrong on the server."}
	}
	writeJSON(w, e.Status, e)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errBadRequest("Malformed request body.")
	}
	return nil
}

type handler func(w http.ResponseWriter, r *http.Request) error
type userHandler func(w http.ResponseWriter, r *http.Request, u *store.User) error

const sessionCookie = "avsys_session"

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.Config.PublicURL, "https://") }

// bearerToken returns the API token a request carries, if any.
func bearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

func (s *Server) currentUser(r *http.Request) (*store.User, error) {
	if token := bearerToken(r); token != "" {
		u, err := s.Store.UserByAPIToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errUnauthorized
		}
		return u, err
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errUnauthorized
	}
	u, err := s.Store.SessionUser(r.Context(), c.Value)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnauthorized
	}
	return u, err
}

// public wraps a handler that needs no session.
func (s *Server) public(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browsers cannot attach a custom header to a cross-site request
		// without a CORS preflight, which this server never approves. Requiring
		// one on anything that changes state is the CSRF defence. Requests
		// carrying an API token are not using the cookie, so they are exempt.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Avsys") == "" && bearerToken(r) == "" {
			s.fail(w, r, &Error{Status: http.StatusForbidden, Code: "csrf", Message: "Missing X-Avsys header."})
			return
		}
		if err := h(w, r); err != nil {
			s.fail(w, r, err)
		}
	})
}

// authed wraps a handler that needs a signed-in user.
func (s *Server) authed(h userHandler) http.Handler {
	return s.public(func(w http.ResponseWriter, r *http.Request) error {
		u, err := s.currentUser(r)
		if err != nil {
			return err
		}
		return h(w, r, u)
	})
}

// access is a user's standing in the organization named by the request path.
type access struct {
	Org       *store.Org
	Role      store.Role
	Namespace string
}

// roleIn resolves a user's role in an organization. Instance admins act as
// owner everywhere.
func (s *Server) roleIn(ctx context.Context, u *store.User, org *store.Org) (store.Role, error) {
	role, err := s.Store.RoleIn(ctx, org.ID, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		if u.IsAdmin {
			return store.RoleOwner, nil
		}
		return "", errNotFound
	}
	return role, err
}

// org loads the organization in the path and checks the user holds at least
// min in it. Non-members get "not found" rather than "forbidden", so the
// existence of an organization is not revealed to outsiders.
func (s *Server) org(r *http.Request, u *store.User, min store.Role) (*access, error) {
	org, err := s.Store.OrgBySlug(r.Context(), r.PathValue("org"))
	if err != nil {
		return nil, err
	}
	role, err := s.roleIn(r.Context(), u, org)
	if err != nil {
		return nil, err
	}
	if !role.AtLeast(min) {
		return nil, errForbidden
	}
	return &access{Org: org, Role: role, Namespace: s.Namespace(org.Slug)}, nil
}

func (s *Server) audit(ctx context.Context, u *store.User, org *store.Org, action, target, detail string) {
	e := store.AuditEntry{UserID: u.ID, Action: action, Target: target, Detail: detail}
	if org != nil {
		e.OrgID = org.ID
	}
	if err := s.Store.Audit(context.WithoutCancel(ctx), e); err != nil {
		s.Log.Error("writing audit entry", "action", action, "error", err)
	}
}

// Handler builds the HTTP handler for the whole panel.
func (s *Server) Handler() http.Handler {
	s.catalog = newCatalog(s.Config.Version)
	if s.Upstream != nil {
		s.catalog.http.Transport = s.Upstream
	}
	s.offsite.since()
	s.workspaces = newWorkspacePool(s.Gateway)

	mux := http.NewServeMux()
	api := func(pattern string, h http.Handler) {
		method, path, _ := strings.Cut(pattern, " ")
		mux.Handle(method+" /api/v1"+path, h)
		s.routes = append(s.routes, pattern)
	}

	api("GET /meta", s.public(s.getMeta))
	api("POST /auth/register", s.public(s.register))
	api("POST /auth/login", s.public(s.login))
	api("POST /auth/logout", s.public(s.logout))
	api("GET /auth/{provider}/start", s.public(s.oauthStart))
	api("GET /auth/{provider}/callback", s.public(s.oauthCallback))

	api("GET /me", s.authed(s.getMe))
	api("GET /nodes", s.authed(s.listNodes))
	api("PATCH /me", s.authed(s.updateMe))
	api("POST /me/password", s.authed(s.setPassword))
	api("PUT /me/minecraft", s.authed(s.setMinecraftAccount))
	api("DELETE /me/minecraft", s.authed(s.clearMinecraftAccount))
	api("PUT /me/avatar", s.authed(s.putAvatar))
	api("DELETE /me/avatar", s.authed(s.deleteAvatar))
	api("GET /users/{user}/avatar", s.authed(s.getAvatar))
	api("GET /me/ssh-keys", s.authed(s.listSSHKeys))
	api("POST /me/ssh-keys", s.authed(s.addSSHKey))
	api("POST /me/ssh-keys/import", s.authed(s.importSSHKeys))
	api("DELETE /me/ssh-keys/{id}", s.authed(s.deleteSSHKey))

	api("GET /me/tokens", s.authed(s.listAPITokens))
	api("POST /me/tokens", s.authed(s.createAPIToken))
	api("DELETE /me/tokens/{id}", s.authed(s.deleteAPIToken))

	api("GET /invites/{token}", s.public(s.previewInvite))
	api("POST /invites/{token}/accept", s.authed(s.acceptInvite))

	api("POST /orgs", s.authed(s.createOrg))
	api("GET /orgs/{org}", s.authed(s.getOrg))
	api("PATCH /orgs/{org}", s.authed(s.updateOrg))
	api("DELETE /orgs/{org}", s.authed(s.deleteOrg))
	api("GET /orgs/{org}/members", s.authed(s.listMembers))
	api("PATCH /orgs/{org}/members/{user}", s.authed(s.updateMember))
	api("DELETE /orgs/{org}/members/{user}", s.authed(s.removeMember))
	api("GET /orgs/{org}/invites", s.authed(s.listInvites))
	api("POST /orgs/{org}/invites", s.authed(s.createInvite))
	api("DELETE /orgs/{org}/invites/{token}", s.authed(s.deleteInvite))

	api("GET /orgs/{org}/backup-policy", s.authed(s.getBackupPolicy))
	api("PUT /orgs/{org}/backup-policy", s.authed(s.putBackupPolicy))
	api("POST /orgs/{org}/backup-policy/run", s.authed(s.runBackupPolicy))
	api("GET /orgs/{org}/icon", s.authed(s.getOrgIcon))
	api("PUT /orgs/{org}/icon", s.authed(s.putOrgIcon))
	api("DELETE /orgs/{org}/icon", s.authed(s.deleteOrgIcon))
	api("GET /orgs/{org}/offsite", s.authed(s.getOffsite))
	api("POST /orgs/{org}/offsite/run", s.authed(s.runOffsite))
	api("GET /orgs/{org}/offsite/snapshots", s.authed(s.offsiteSnapshots))
	api("POST /orgs/{org}/offsite/fetch", s.authed(s.fetchOffsite))
	api("GET /orgs/{org}/snapshots", s.authed(s.listOrgSnapshots))
	api("GET /orgs/{org}/snapshots/{id}", s.authed(s.getSnapshot))
	api("GET /orgs/{org}/snapshots/{id}/icon", s.authed(s.getSnapshotIcon))
	api("DELETE /orgs/{org}/snapshots", s.authed(s.deleteSnapshots))
	api("DELETE /orgs/{org}/snapshots/{id}", s.authed(s.deleteSnapshot))

	api("GET /orgs/{org}/domains", s.authed(s.getDomains))
	api("PUT /orgs/{org}/domains", s.authed(s.putDomains))
	api("GET /orgs/{org}/servers", s.authed(s.listServers))
	api("POST /orgs/{org}/servers", s.authed(s.createServer))
	api("GET /orgs/{org}/servers/{server}", s.authed(s.getServer))
	api("PATCH /orgs/{org}/servers/{server}", s.authed(s.updateServer))
	api("DELETE /orgs/{org}/servers/{server}", s.authed(s.deleteServer))
	api("POST /orgs/{org}/servers/{server}/{action}", s.authed(s.serverAction))
	api("PUT /orgs/{org}/servers/{server}/content", s.authed(s.uploadContent))
	api("GET /orgs/{org}/servers/{server}/console", s.authed(s.streamConsole))
	api("POST /orgs/{org}/servers/{server}/console", s.authed(s.runCommand))
	api("GET /orgs/{org}/servers/{server}/logs", s.authed(s.listLogs))
	api("GET /orgs/{org}/servers/{server}/logs/file", s.authed(s.readLogFile))
	api("GET /orgs/{org}/servers/{server}/snapshots", s.authed(s.listServerSnapshots))
	api("POST /orgs/{org}/servers/{server}/snapshots", s.authed(s.createSnapshot))
	api("POST /orgs/{org}/servers/{server}/snapshots/{id}/restore", s.authed(s.restoreSnapshot))
	api("GET /orgs/{org}/servers/{server}/operation", s.authed(s.getOperation))
	api("GET /orgs/{org}/servers/{server}/terminal", s.authed(s.terminal))
	api("GET /orgs/{org}/servers/{server}/icon", s.authed(s.getServerIcon))
	api("PUT /orgs/{org}/servers/{server}/icon", s.authed(s.putServerIcon))
	api("DELETE /orgs/{org}/servers/{server}/icon", s.authed(s.deleteServerIcon))
	api("GET /orgs/{org}/servers/{server}/files", s.authed(s.listFiles))
	api("DELETE /orgs/{org}/servers/{server}/files", s.authed(s.deleteFile))
	api("GET /orgs/{org}/servers/{server}/files/content", s.authed(s.readFile))
	api("PUT /orgs/{org}/servers/{server}/files/content", s.authed(s.writeFile))
	api("POST /orgs/{org}/servers/{server}/files/mkdir", s.authed(s.makeDir))
	api("POST /orgs/{org}/servers/{server}/files/rename", s.authed(s.renameFile))

	api("GET /events", s.authed(s.streamEvents))

	api("GET /catalog/minecraft/versions", s.authed(s.minecraftVersions))
	api("GET /catalog/modrinth/search", s.authed(s.modrinthSearch))
	api("GET /catalog/modrinth/categories", s.authed(s.modrinthCategories))
	api("GET /catalog/modrinth/projects", s.authed(s.modrinthProjects))
	api("GET /catalog/modrinth/projects/{id}/versions", s.authed(s.modrinthVersions))
	api("POST /catalog/sha1", s.authed(s.hashURL))

	mux.Handle("/api/", s.public(func(http.ResponseWriter, *http.Request) error { return errNotFound }))
	if s.Static != nil {
		mux.Handle("/", s.staticHandler())
	}
	return mux
}

// staticHandler serves the web UI, falling back to index.html so that
// client-side routes load on a hard refresh.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServerFS(s.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(s.Static, path); err != nil {
			// A script or stylesheet that is not there belongs to another
			// build. The page is no answer to that: a browser asking for a
			// script would be handed HTML and told all was well.
			if strings.HasPrefix(path, "assets/") {
				// Not to be kept either: WebKit holds on to a 404 for a
				// script and answers the next request for it from memory.
				w.Header().Set("Cache-Control", "no-store")
				http.NotFound(w, r)
				return
			}
			path = "index.html"
		}
		if strings.HasPrefix(path, "assets/") {
			// Build output under assets/ is content-hashed.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if path == "index.html" {
			// Written directly: the file server would redirect any request
			// for index.html back to its directory.
			page, err := fs.ReadFile(s.Static, "index.html")
			if err != nil {
				http.Error(w, "The web UI is not built.", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + path
		files.ServeHTTP(w, r2)
	})
}

const sessionTTL = 30 * 24 * time.Hour

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID string) error {
	token, err := s.Store.CreateSession(r.Context(), userID, sessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return nil
}
