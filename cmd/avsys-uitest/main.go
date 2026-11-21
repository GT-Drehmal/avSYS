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
// The cluster has no metrics API unless --metrics is given, which also
// gives it two nodes (see metrics.go). With --fresh it is a panel just
// installed instead, with nobody and nothing in it, waiting to be set up.
//
// It prints one line of JSON when it is listening: the address, and an API
// token for each of the people it was seeded with. --listen with port 0
// takes any free port, which that line then says.
package main

import (
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/api"
	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/auth/fakeidp"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/release"
	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to serve on")
	webDir := flag.String("web-dir", "web/dist", "the built web UI")
	upstreamDir := flag.String("upstream", "web/tests/upstream", "answers for the services the panel looks things up in")
	readyFile := flag.String("ready", "", "also write the line printed when listening to this file")
	metrics := flag.Bool("metrics", false, "stand in a metrics API, and two nodes for it to answer for")
	offsite := flag.Bool("offsite", false, "copy snapshots offsite, with the older ones of the first organization there already")
	fresh := flag.Bool("fresh", false, "seed nothing: a panel just installed, waiting for its first account")
	curseForge := flag.String("curseforge-key", "", "a CurseForge API key the panel is installed with: CurseForge is offered, and answered from --upstream")
	flag.Parse()
	if err := run(*listen, *webDir, *upstreamDir, *readyFile, *metrics, *offsite, *fresh, *curseForge); err != nil {
		fmt.Fprintln(os.Stderr, "avsys-uitest:", err)
		os.Exit(1)
	}
}

// password is the one every seeded person signs in with.
const password = "correct horse battery staple"

func run(listen, webDir, upstreamDir, readyFile string, withMetrics, withOffsite, fresh bool, curseForgeKey string) error {
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
		Config: api.Config{NamespacePrefix: "mc-", SSHHost: "panel.test", SSHPort: 2222, RouterDomain: "play.test", Version: "v0.1.0",
			// Installed with the chart: see the Service below.
			RouterService: types.NamespacedName{Namespace: "avsys", Name: "avsys"},
			// Its own Deployment, which it can move to a newer release.
			Deployment:    types.NamespacedName{Namespace: "avsys", Name: "avsys"},
			CurseForgeKey: curseForgeKey},
		Store:  db,
		Client: cluster,
		Reader: cluster,
		Kube: kubefake.NewSimpleClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "avsys", Name: "avsys"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "avsys", Image: "ghcr.io/gt-drehmal/avsys:v0.1.0"}},
			}}},
		}),
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
	// Servers named bingo have files with content the panel did not add.
	srv.ContentFiles = looseFiles(dir)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv.Config.PublicURL = "http://" + ln.Addr().String()

	// An OpenID Connect provider of its own, which signs in whoever is typed
	// on its page. "outsider" there is the outsider here.
	idp, err := fakeidp.Start("avsys-uitest", "uitest-secret", srv.Config.PublicURL+"/api/v1/auth/oidc/callback")
	if err != nil {
		return err
	}
	defer idp.Close()
	provider, err := auth.NewOIDCProvider(auth.OIDCConfig{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret, Name: "Test ID"}, srv.Config.PublicURL)
	if err != nil {
		return err
	}
	srv.Config.Providers = map[string]*auth.Provider{provider.ID: provider}

	metrics := &metricsStandIn{on: withMetrics, cluster: cluster}
	srv.Metrics = metrics
	// The servers' games answer help as a server with Yet Another Bingo
	// did, recorded in --upstream: the console completes its commands.
	help, err := helpRecording(upstreamDir)
	if err != nil {
		return err
	}
	srv.GameCommand = func(ctx context.Context, mc *avsysv1.MinecraftServer, command string) (string, error) {
		if out, ok := help[command]; ok {
			return out, nil
		}
		return metrics.gameCommand(ctx, mc, command)
	}
	// The game's command trees are the ones recorded in --upstream; a
	// version without one has none, as one too old for them. That of
	// 1.20.1 cannot be made, as when Mojang's site does not answer: its
	// servers complete from their help alone.
	srv.CommandTree = func(_ context.Context, version string) ([]byte, error) {
		if version == "1.20.1" {
			return nil, errors.New("could not reach launchermeta.mojang.com")
		}
		return commandTree(upstreamDir, version)
	}
	tokens := map[string]string{}
	if !fresh {
		if tokens, err = populate(ctx, srv, cluster, dir, withOffsite, provider.ID, idp.URL); err != nil {
			return err
		}
		if withMetrics {
			if err := seedNodes(ctx, cluster); err != nil {
				return err
			}
		}
		backfill(ctx, srv, metrics)
	}
	go srv.RunUsage(ctx)

	// A newer release than this one (v0.1.0) is out, as upstream says, and
	// the panel was installed with the chart, as its Service says.
	panelService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "avsys", Name: "avsys",
		Labels:      map[string]string{"helm.sh/chart": "avsys-0.1.0"},
		Annotations: map[string]string{"meta.helm.sh/release-name": "avsys", "meta.helm.sh/release-namespace": "avsys"}}}
	if err := cluster.Create(ctx, panelService); err != nil {
		return err
	}
	srv.Updates = &release.Checker{Current: srv.Config.Version, Client: &http.Client{Transport: up}}
	srv.Updates.Refresh(ctx)

	ready, _ := json.Marshal(map[string]any{"url": srv.Config.PublicURL, "password": password, "tokens": tokens})
	fmt.Println(string(ready))
	if readyFile != "" {
		if err := os.WriteFile(readyFile, ready, 0o600); err != nil {
			return err
		}
	}
	return http.Serve(ln, srv.Handler())
}

// populate fills the panel with people, organizations, servers and
// snapshots, and returns an API token for each person. With offsite, the
// panel copies snapshots offsite, and the older ones are there already. The
// outsider is linked to "outsider" at the OpenID Connect provider (oidc,
// whose issuer is issuer).
func populate(ctx context.Context, srv *api.Server, cluster client.Client, dir string, offsite bool, oidc, issuer string) (map[string]string, error) {
	db := srv.Store
	// People, one for each role in the first organization, and one who is
	// in neither.
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	tokens := map[string]string{}
	users := map[string]*store.User{}
	for _, name := range []string{"owner", "admin", "operator", "viewer", "outsider"} {
		u := &store.User{Username: name, DisplayName: strings.ToUpper(name[:1]) + name[1:], PasswordHash: hash}
		if err := db.CreateUser(ctx, u); err != nil {
			return nil, err
		}
		secret, err := db.CreateAPIToken(ctx, &store.APIToken{UserID: u.ID, Name: "uitest"})
		if err != nil {
			return nil, err
		}
		users[name], tokens[name] = u, secret
	}
	if err := db.LinkIdentity(ctx, store.Identity{Provider: oidc, Subject: auth.OIDCSubject(issuer, "outsider"), UserID: users["outsider"].ID, Login: "outsider"}); err != nil {
		return nil, err
	}
	friends := &store.Org{Name: "Friends", Slug: "friends"}
	if err := db.CreateOrg(ctx, friends, users["owner"].ID); err != nil {
		return nil, err
	}
	for _, name := range []string{"admin", "operator", "viewer"} {
		inv := &store.Invite{OrgID: friends.ID, Role: store.Role(name), CreatedBy: "owner"}
		if err := db.CreateInvite(ctx, inv); err != nil {
			return nil, err
		}
		if _, err := db.AcceptInvite(ctx, inv.Token, users[name].ID); err != nil {
			return nil, err
		}
	}
	// An invite that has run out, for its link to be followed.
	expired := time.Now().Add(-time.Hour)
	if err := db.CreateInvite(ctx, &store.Invite{OrgID: friends.ID, Role: store.RoleViewer, CreatedBy: "owner", ExpiresAt: &expired}); err != nil {
		return nil, err
	}
	// A second organization of the same owner, with a server of the same
	// name as one in the first.
	club := &store.Org{Name: "Build Club", Slug: "club"}
	if err := db.CreateOrg(ctx, club, users["owner"].ID); err != nil {
		return nil, err
	}

	// Servers in each state a list has to draw.
	players := &avsysv1.PlayersStatus{Online: 2, Max: 20, Names: []string{"Steve", "Alex"}, Sample: []avsysv1.OnlinePlayer{{Name: "Steve", ID: "8667ba71-b85a-4004-af54-457a9734eed7"}, {Name: "Alex", ID: "ec561538-f3fd-461d-aff5-086b22154bce"}}}
	for _, s := range []seed{
		{org: "friends", name: "survival", display: "Survival", loader: avsysv1.LoaderPaper, version: "1.21.4", state: avsysv1.StateRunning, phase: avsysv1.PhaseRunning, players: players, lastBackup: 3 * time.Hour,
			mods: []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Title: "LuckPerms", Version: "lp1", VersionName: "v5.5.0"}}},
		{org: "friends", name: "creative", display: "Creative", loader: avsysv1.LoaderFabric, version: "1.21.4", state: avsysv1.StateStopped, phase: avsysv1.PhaseStopped, lastBackup: 50 * time.Hour},
		{org: "friends", name: "lobby", display: "Lobby", loader: avsysv1.LoaderVanilla, version: "1.21.4", state: avsysv1.StateRunning, phase: avsysv1.PhaseSleeping, sleep: true},
		{org: "friends", name: "broken", display: "Broken", loader: avsysv1.LoaderPaper, version: "1.21.4", state: avsysv1.StateRunning, phase: avsysv1.PhaseError, message: "The server exited with code 1."},
		{org: "club", name: "survival", display: "Survival", loader: avsysv1.LoaderVanilla, version: "1.21.4", state: avsysv1.StateStopped, phase: avsysv1.PhaseStopped},
		// On a version from before the game could say how fast it ticks.
		{org: "club", name: "classic", display: "Classic", loader: avsysv1.LoaderVanilla, version: "1.20.1", state: avsysv1.StateRunning, phase: avsysv1.PhaseRunning},
		// Moved in from elsewhere: its mods and datapacks are files in its
		// folders, which the panel did not add (see loose.go).
		{org: "club", name: "bingo", display: "Drehmal Bingo", loader: avsysv1.LoaderFabric, version: "1.20.1", state: avsysv1.StateStopped, phase: avsysv1.PhaseStopped},
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
			return nil, err
		}
		mc.Status = avsysv1.MinecraftServerStatus{Phase: s.phase, Message: s.message, Players: s.players, Address: mc.Spec.Expose.Hostname, MaxPlayers: 20}
		if s.phase == avsysv1.PhaseRunning {
			mc.Status.Version = s.version
			if s.loader == avsysv1.LoaderPaper {
				mc.Status.Version = "Paper " + s.version
			}
		}
		if s.lastBackup > 0 {
			at := metav1.NewTime(time.Now().Add(-s.lastBackup))
			mc.Status.Snapshots = &avsysv1.SnapshotStatus{LastRun: &at}
		}
		if err := cluster.Status().Update(ctx, mc); err != nil {
			return nil, err
		}
	}

	// A world in the first organization's library, with datapacks.
	world, err := seedLibrary(ctx, srv, dir, friends)
	if err != nil {
		return nil, err
	}
	if err := seedSnapshots(dir, offsite, world); err != nil {
		return nil, err
	}
	if offsite {
		// A directory standing in for the far end. The panel learns what
		// is there the way it does after any sync.
		srv.Backups.Offsite = &backup.Offsite{Repository: filepath.Join(dir, "offsite"), Password: password}
		if _, err := srv.Backups.SyncOffsite(ctx, "friends"); err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

// resticStandIn is what the panel runs as restic here. It answers the
// questions the panel asks of a repository out of files beside it, and
// agrees to everything else. Its arguments are as the panel gives them:
// --repo DIR, then --password-file FILE --no-cache --retry-lock TIME for its
// own repositories or --cache-dir DIR for the offsite ones, then COMMAND ...
const resticStandIn = `#!/bin/sh
repo=$2
shift 2
while :; do
  case "$1" in
    --password-file|--cache-dir|--retry-lock) shift 2 ;;
    --no-cache) shift ;;
    *) break ;;
  esac
done
case "$1" in
  snapshots) cat "$repo/snapshots.json" 2>/dev/null || echo '[]' ;;
  stats) echo '{"total_size": 734003200, "snapshots_count": 6}' ;;
  dump) cat "$repo/record-$3.json" 2>/dev/null || exit 1 ;;
  forget) cat "$repo/forget.json" 2>/dev/null ;;
esac
`

// seedSnapshots gives the first organization a repository with snapshots in
// it: several of a server that runs every day, one of another, the last
// one of a server that has since been deleted, and that of the library's
// world. With offsite, the offsite repository has copies of those more than
// a few hours old.
func seedSnapshots(dir string, offsite bool, world string) error {
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
		ID string `json:"id"`
		// Original is the snapshot a copy was made of.
		Original string    `json:"original,omitempty"`
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
	// The library's world, kept as its own files an hour ago.
	list = append(list, snapshot{ID: id(len(taken) + 1), Time: now.Add(-time.Hour), Hostname: "world-" + world,
		Tags: []string{"avsys", "trigger=template", "form=folders", "by=owner"}, Summary: summary{Bytes: 1_610_612_736, Files: 9_000, Added: 900 << 20}})
	if err := write("snapshots.json", list); err != nil {
		return err
	}
	if err := write("forget.json", []map[string]any{{"reasons": reasons}}); err != nil {
		return err
	}
	if !offsite {
		return nil
	}
	// A copy has an ID of its own.
	var copies []snapshot
	for _, s := range list {
		if now.Sub(s.Time) > 12*time.Hour {
			s.Original, s.ID = s.ID, "f"+s.ID[1:]
			copies = append(copies, s)
		}
	}
	far := filepath.Join(dir, "offsite", "friends")
	if err := os.MkdirAll(far, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(copies)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(far, "snapshots.json"), data, 0o600)
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
		// Status is what it answers with, where that is not 200 OK.
		Status int `json:"status"`
	}
}

// commandTree is a game version's commands.json, as recorded in
// commands/<version>.json.gz under dir.
func commandTree(dir, version string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, "commands", url.PathEscape(version)+".json.gz"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, api.ErrNoCommands
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(z)
}

// helpRecording is what a server with Yet Another Bingo answered to each
// help command the panel asked it, as recorded in commands/help/bingo.json
// under dir.
func helpRecording(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "commands", "help", "bingo.json"))
	if err != nil {
		return nil, err
	}
	var help map[string]string
	return help, json.Unmarshal(data, &help)
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
		status := cmp.Or(route.Status, http.StatusOK)
		return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: http.Header{"Content-Type": {"application/json"}}, Body: f, Request: r}, nil
	}
	fmt.Fprintln(os.Stderr, "avsys-uitest: nothing answers", address)
	return nil, errors.New("offline: " + r.URL.Host)
}
