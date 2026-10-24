// Command avsys-uitest serves the panel with nothing behind it, for the
// browser tests: no cluster, no restic, no internet.
//
// The API and the web UI are the real ones. What stands in is everything
// they would reach out to. Servers are kept by a client that only
// remembers them, so they can be made, changed and deleted but nothing
// ever runs: each stays in the state it was seeded with. What the panel
// would look up at Modrinth, Mojang and the build sites is answered from
// the files in --upstream. Pages that need a running server (console,
// files, terminal, snapshots) are there to be opened and to fail politely.
//
// It prints one line of JSON when it is listening: the address, and an API
// token for each of the people it was seeded with.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/api"
	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to serve on")
	webDir := flag.String("web-dir", "web/dist", "the built web UI")
	upstreamDir := flag.String("upstream", "web/tests/upstream", "answers for the services the panel looks things up in")
	readyFile := flag.String("ready", "", "also write the line printed when listening to this file")
	flag.Parse()
	if err := run(*listen, *webDir, *upstreamDir, *readyFile); err != nil {
		fmt.Fprintln(os.Stderr, "avsys-uitest:", err)
		os.Exit(1)
	}
}

// password is the one every seeded person signs in with.
const password = "correct horse battery staple"

func run(listen, webDir, upstreamDir, readyFile string) error {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	dir, err := os.MkdirTemp("", "avsys-uitest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		return err
	}
	cluster := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&avsysv1.MinecraftServer{}, router.HostnameIndex, router.IndexHostname).
		WithStatusSubresource(&avsysv1.MinecraftServer{}).
		Build()

	up, err := loadUpstream(upstreamDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(webDir, "index.html")); err != nil {
		return fmt.Errorf("the web UI is not built in %s: run `bun run build` in web/", webDir)
	}
	// Nothing runs, so nothing can be reached inside a server: asking is
	// answered with an error, as a server whose pod is gone would be.
	// Nothing runs here to wait for: a server's files are not there at once.
	gateway := &sshgw.Gateway{Store: db, Dialer: noPods{}, Log: log, WorkspaceTries: 1}
	srv := &api.Server{
		Config:  api.Config{NamespacePrefix: "mc-", PublicURL: "http://" + listen, SSHHost: "panel.test", SSHPort: 2222, RouterDomain: "play.test", Version: "uitest"},
		Store:   db,
		Client:  cluster,
		Reader:  cluster,
		Kube:    kubefake.NewSimpleClientset(),
		Dialer:  noPods{},
		Gateway: gateway,
		Events:  api.NewHub(),
		Log:     log,
		// A restic that answers from files (see seedSnapshots): enough for
		// the pages about snapshots to be drawn and looked through.
		Backups:  &backup.Manager{Dir: filepath.Join(dir, "backups"), Restic: filepath.Join(dir, "restic"), Log: log},
		Static:   os.DirFS(webDir),
		Upstream: up,
	}
	gateway.Authz = srv

	// People, one for each role in the first organization, and one who is
	// in neither.
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	tokens := map[string]string{}
	users := map[string]*store.User{}
	for _, name := range []string{"owner", "admin", "operator", "viewer", "outsider"} {
		u := &store.User{Username: name, DisplayName: strings.ToUpper(name[:1]) + name[1:], PasswordHash: hash}
		if err := db.CreateUser(ctx, u); err != nil {
			return err
		}
		secret, err := db.CreateAPIToken(ctx, &store.APIToken{UserID: u.ID, Name: "uitest"})
		if err != nil {
			return err
		}
		users[name], tokens[name] = u, secret
	}
	friends := &store.Org{Name: "Friends", Slug: "friends"}
	if err := db.CreateOrg(ctx, friends, users["owner"].ID); err != nil {
		return err
	}
	for _, name := range []string{"admin", "operator", "viewer"} {
		inv := &store.Invite{OrgID: friends.ID, Role: store.Role(name), CreatedBy: users["owner"].ID}
		if err := db.CreateInvite(ctx, inv); err != nil {
			return err
		}
		if _, err := db.AcceptInvite(ctx, inv.Token, users[name].ID); err != nil {
			return err
		}
	}
	// A second organization of the same owner, with a server of the same
	// name as one in the first.
	club := &store.Org{Name: "Build Club", Slug: "club"}
	if err := db.CreateOrg(ctx, club, users["owner"].ID); err != nil {
		return err
	}

	// Servers in each state a list has to draw.
	players := &avsysv1.PlayersStatus{Online: 2, Max: 20, Names: []string{"Steve", "Alex"}}
	for _, s := range []seed{
		{org: "friends", name: "survival", display: "Survival", loader: avsysv1.LoaderPaper, version: "1.21.4", state: avsysv1.StateRunning, phase: avsysv1.PhaseRunning, players: players, lastBackup: 3 * time.Hour,
			mods: []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Title: "LuckPerms", Version: "lp1", VersionName: "v5.5.0"}}},
		{org: "friends", name: "creative", display: "Creative", loader: avsysv1.LoaderFabric, version: "1.21.4", state: avsysv1.StateStopped, phase: avsysv1.PhaseStopped, lastBackup: 50 * time.Hour},
		{org: "friends", name: "lobby", display: "Lobby", loader: avsysv1.LoaderVanilla, version: "1.21.4", state: avsysv1.StateRunning, phase: avsysv1.PhaseSleeping, sleep: true},
		{org: "friends", name: "broken", display: "Broken", loader: avsysv1.LoaderPaper, version: "1.21.4", state: avsysv1.StateRunning, phase: avsysv1.PhaseError, message: "The server exited with code 1."},
		{org: "club", name: "survival", display: "Survival", loader: avsysv1.LoaderVanilla, version: "1.21.4", state: avsysv1.StateStopped, phase: avsysv1.PhaseStopped},
	} {
		mc := &avsysv1.MinecraftServer{
			ObjectMeta: metav1.ObjectMeta{Namespace: srv.Namespace(s.org), Name: s.name, CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour))},
			Spec: avsysv1.MinecraftServerSpec{
				EULA: true, DisplayName: s.display, Version: s.version, Loader: s.loader, State: s.state, Mods: s.mods,
				Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")},
				Storage:   avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
				Expose:    avsysv1.ExposeSpec{Type: avsysv1.ExposeRouter, Hostname: s.name + "." + s.org + ".play.test"},
				Sleep:     avsysv1.SleepSpec{Enabled: s.sleep, IdleMinutes: 15},
			},
		}
		if err := cluster.Create(ctx, mc); err != nil {
			return err
		}
		mc.Status = avsysv1.MinecraftServerStatus{Phase: s.phase, Message: s.message, Players: s.players, Address: mc.Spec.Expose.Hostname, MaxPlayers: 20}
		if s.phase == avsysv1.PhaseRunning {
			mc.Status.Version = "Paper " + s.version
		}
		if s.lastBackup > 0 {
			at := metav1.NewTime(time.Now().Add(-s.lastBackup))
			mc.Status.Snapshots = &avsysv1.SnapshotStatus{LastRun: &at}
		}
		if err := cluster.Status().Update(ctx, mc); err != nil {
			return err
		}
	}

	if err := seedSnapshots(dir); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	ready, _ := json.Marshal(map[string]any{"url": "http://" + ln.Addr().String(), "password": password, "tokens": tokens})
	fmt.Println(string(ready))
	if readyFile != "" {
		if err := os.WriteFile(readyFile, ready, 0o600); err != nil {
			return err
		}
	}
	return http.Serve(ln, srv.Handler())
}

// resticStandIn is what the panel runs as restic here. It answers the
// questions the panel asks of a repository out of files beside it, and
// agrees to everything else. Its arguments are as the panel gives them:
// --repo DIR --password-file FILE --no-cache COMMAND ...
const resticStandIn = `#!/bin/sh
repo=$2
case "$6" in
  snapshots) cat "$repo/snapshots.json" 2>/dev/null || echo '[]' ;;
  stats) echo '{"total_size": 734003200, "snapshots_count": 6}' ;;
  dump) cat "$repo/record-$8.json" 2>/dev/null || exit 1 ;;
  forget) cat "$repo/forget.json" 2>/dev/null ;;
esac
`

// seedSnapshots gives the first organization a repository with snapshots in
// it: several of a server that runs every day, one of another, and the last
// one of a server that has since been deleted.
func seedSnapshots(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "restic"), []byte(resticStandIn), 0o755); err != nil {
		return err
	}
	repo := filepath.Join(dir, "backups", "friends")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	// What makes a directory a repository, as far as the panel looks.
	if err := os.WriteFile(filepath.Join(repo, "config"), nil, 0o600); err != nil {
		return err
	}
	type summary struct {
		Bytes int64 `json:"total_bytes_processed"`
		Files int64 `json:"total_files_processed"`
		Added int64 `json:"data_added_packed"`
	}
	type snapshot struct {
		ID       string    `json:"id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Tags     []string  `json:"tags"`
		Summary  summary   `json:"summary"`
	}
	type reason struct {
		Snapshot snapshot `json:"snapshot"`
		Matches  []string `json:"matches"`
	}
	now := time.Now().UTC()
	id := func(n int) string { return fmt.Sprintf("%02x", n) + strings.Repeat("a1b2c3d4", 8)[2:] }
	taken := []struct {
		server, trigger, by string
		ago                 time.Duration
	}{
		{"survival", "scheduled", "", 3 * time.Hour},
		{"survival", "manual", "owner", 20 * time.Hour},
		{"survival", "scheduled", "", 27 * time.Hour},
		{"survival", "scheduled", "", 51 * time.Hour},
		{"creative", "scheduled", "", 3 * time.Hour},
		{"oldworld", "final", "owner", 240 * time.Hour},
	}
	var list []snapshot
	var reasons []reason
	for i, t := range taken {
		tags := []string{"avsys", "trigger=" + t.trigger, "loader=paper", "version=1.21.4"}
		if t.by != "" {
			tags = append(tags, "by="+t.by)
		}
		snap := snapshot{ID: id(i + 1), Time: now.Add(-t.ago), Hostname: t.server, Tags: tags, Summary: summary{Bytes: 350 << 20, Files: 625, Added: int64(4+i) << 20}}
		list = append(list, snap)
		// The schedule keeps the newest of each day, so all three of these.
		if t.server == "survival" && t.trigger == "scheduled" {
			reasons = append(reasons, reason{Snapshot: snap, Matches: []string{"daily snapshot"}})
		}
		record, err := json.Marshal(map[string]any{"format": 1, "org": "friends", "server": t.server, "spec": avsysv1.MinecraftServerSpec{
			EULA: true, DisplayName: t.server, Version: "1.21.4", Loader: avsysv1.LoaderPaper,
			Resources:  avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")},
			Storage:    avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
			Properties: map[string]string{"difficulty": "hard"},
			Mods:       []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Title: "LuckPerms", Version: "lp1", VersionName: "v5.5.0"}},
		}})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo, "record-"+snap.ID+".json"), record, 0o600); err != nil {
			return err
		}
	}
	write := func(name string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(repo, name), data, 0o600)
	}
	if err := write("snapshots.json", list); err != nil {
		return err
	}
	return write("forget.json", []map[string]any{{"reasons": reasons}})
}

// noPods is the way into servers' pods where there are none.
type noPods struct{}

func (noPods) DialPod(context.Context, string, string, int) (net.Conn, error) {
	return nil, errors.New("nothing runs here: this panel has no cluster behind it")
}

type seed struct {
	org, name, display string
	loader             avsysv1.Loader
	version            string
	state              avsysv1.DesiredState
	phase              avsysv1.Phase
	message            string
	players            *avsysv1.PlayersStatus
	mods               []avsysv1.ContentRef
	sleep              bool
	// lastBackup is how long ago its scheduled snapshot last ran, if it has.
	lastBackup time.Duration
}

// upstream answers what the panel would ask other services, from files.
// routes.json lists them: the first whose "url" the request's address
// starts with, and whose "has" (if any) it contains, is the answer. An
// address nothing answers is an error, as being offline would be, and is
// said on stderr so that a test asking for something new shows up.
type upstream struct {
	dir    string
	routes []struct {
		URL  string `json:"url"`
		Has  string `json:"has"`
		File string `json:"file"`
	}
}

func loadUpstream(dir string) (*upstream, error) {
	u := &upstream{dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, "routes.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &u.routes); err != nil {
		return nil, fmt.Errorf("%s/routes.json: %w", dir, err)
	}
	for _, r := range u.routes {
		if _, err := os.Stat(filepath.Join(dir, r.File)); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func (u *upstream) RoundTrip(r *http.Request) (*http.Response, error) {
	address, _ := url.QueryUnescape(r.URL.String())
	for _, route := range u.routes {
		if !strings.HasPrefix(address, route.URL) || !strings.Contains(address, route.Has) {
			continue
		}
		f, err := os.Open(filepath.Join(u.dir, route.File))
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: f, Request: r}, nil
	}
	fmt.Fprintln(os.Stderr, "avsys-uitest: nothing answers", address)
	return nil, errors.New("offline: " + r.URL.Host)
}
