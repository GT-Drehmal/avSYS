// Command avsys runs the panel: the Kubernetes controller for MinecraftServer
// resources, the HTTP API and web UI, the SSH gateway and the player router.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/deploy/crds"
	"github.com/GT-Drehmal/avSYS/internal/api"
	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
	"github.com/GT-Drehmal/avSYS/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type options struct {
	dataDir   string
	backupDir string
	restic    string
	// offsiteRepository and offsitePasswordFile say where snapshots are
	// copied to and what opens them there.
	offsiteRepository   string
	offsitePasswordFile string
	httpAddr            string
	sshAddr             string
	routerAddr          string
	publicURL           string
	sshPublicHost       string
	sshPublicPort       int
	routerPort          int
	routerDomain        string
	namespacePrefix     string
	workspaceImage      string
	workspacePull       string
	webDir              string
	podDial             string
	allowSignups        bool
	installCRDs         bool
}

// env returns the value of AVSYS_<NAME> for a flag called name, or fallback.
func env(name, fallback string) string {
	if v, ok := os.LookupEnv("AVSYS_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); ok {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(env(name, "")); err == nil {
		return n
	}
	return fallback
}

func parseFlags() options {
	var o options
	str := func(p *string, name, fallback, usage string) {
		flag.StringVar(p, name, env(name, fallback), usage)
	}
	str(&o.dataDir, "data-dir", "./data", "directory for the panel's database")
	str(&o.backupDir, "backup-dir", "", "directory for the organizations' snapshot repositories (default: backups under data-dir)")
	str(&o.restic, "restic", "restic", "restic binary the panel runs to keep those repositories")
	str(&o.offsiteRepository, "offsite-repository", "", "restic repository location every organization's snapshots are copied to, e.g. s3:https://s3.example.com/bucket/avsys; each organization gets its own under it")
	str(&o.offsitePasswordFile, "offsite-password-file", "", "file holding the password of the offsite repositories (or set AVSYS_OFFSITE_PASSWORD)")
	str(&o.httpAddr, "http-addr", ":8080", "address the web UI and API listen on")
	str(&o.sshAddr, "ssh-addr", ":2222", "address the SSH gateway listens on (empty disables it)")
	str(&o.routerAddr, "router-addr", ":25565", "address the Minecraft router listens on (empty disables it)")
	str(&o.publicURL, "public-url", "http://localhost:8080", "URL people open the panel at")
	str(&o.sshPublicHost, "ssh-public-host", "", "hostname shown in SSH instructions (default: the host of public-url)")
	flag.IntVar(&o.sshPublicPort, "ssh-public-port", envInt("ssh-public-port", 0), "port shown in SSH instructions (default: the port of ssh-addr)")
	flag.IntVar(&o.routerPort, "router-public-port", envInt("router-public-port", 25565), "port players reach the router on")
	str(&o.routerDomain, "router-domain", "", "domain with a wildcard DNS record pointing at the router; new servers default to <server>.<org>.<domain>")
	str(&o.namespacePrefix, "namespace-prefix", "mc-", "prefix of the namespace created for each organization")
	str(&o.workspaceImage, "workspace-image", "ghcr.io/gt-drehmal/avsys-workspace:"+version, "image for the SSH workspace next to each server")
	str(&o.workspacePull, "workspace-image-pull-policy", "", "pull policy for the workspace image (default: Kubernetes' own)")
	str(&o.webDir, "web-dir", "", "serve the web UI from this directory instead of the built-in copy")
	str(&o.podDial, "pod-dial", "auto", "how to reach pods: direct, forward (through the API server), or auto")
	flag.BoolVar(&o.allowSignups, "allow-signups", env("allow-signups", "") == "true", "let anyone create an account without an invite")
	flag.BoolVar(&o.installCRDs, "install-crds", env("install-crds", "true") == "true", "install or upgrade the MinecraftServer CRD on startup")
	flag.Parse()
	o.publicURL = strings.TrimRight(o.publicURL, "/")
	return o
}

func main() {
	opts := parseFlags()
	handler := slog.NewTextHandler(os.Stderr, nil)
	log := slog.New(handler)
	ctrl.SetLogger(logr.FromSlogHandler(handler))
	if err := run(opts, log); err != nil {
		log.Error("avsys stopped", "error", err)
		os.Exit(1)
	}
}

func providers(publicURL string) (map[string]*auth.Provider, error) {
	out := map[string]*auth.Provider{}
	for _, id := range auth.ProviderIDs {
		prefix := "AVSYS_" + strings.ToUpper(id)
		clientID, secret := os.Getenv(prefix+"_CLIENT_ID"), os.Getenv(prefix+"_CLIENT_SECRET")
		if clientID == "" && secret == "" {
			continue
		}
		if clientID == "" || secret == "" {
			return nil, fmt.Errorf("%s sign-in needs both %s_CLIENT_ID and %s_CLIENT_SECRET", id, prefix, prefix)
		}
		p, err := auth.NewProvider(id, clientID, secret, publicURL)
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, nil
}

func run(opts options, log *slog.Logger) error {
	ctx := ctrl.SetupSignalHandler()

	if err := os.MkdirAll(opts.dataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(opts.dataDir, "avsys.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	hostKey, err := sshgw.LoadOrCreateKey(ctx, st, "host")
	if err != nil {
		return fmt.Errorf("ssh host key: %w", err)
	}
	workspaceKey, err := sshgw.LoadOrCreateKey(ctx, st, "workspace")
	if err != nil {
		return fmt.Errorf("workspace key: %w", err)
	}
	oauth, err := providers(opts.publicURL)
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(avsysv1.AddToScheme(scheme))

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	if opts.installCRDs {
		if err := installCRDs(ctx, restCfg); err != nil {
			return fmt.Errorf("installing CRDs: %w", err)
		}
	}
	// Only objects avSYS manages are cached, and the kinds it reads rarely are
	// not cached at all, so memory does not grow with the rest of the cluster.
	managed := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/managed-by": "avsys"})
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}:     {Label: managed},
			&corev1.Service{}: {Label: managed},
		}},
		Client: client.Options{Cache: &client.CacheOptions{
			DisableFor: []client.Object{&corev1.Secret{}, &corev1.PersistentVolumeClaim{}, &corev1.Namespace{}},
		}},
	})
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}

	inCluster := os.Getenv("KUBERNETES_SERVICE_HOST") != ""
	switch opts.podDial {
	case "direct":
		inCluster = true
	case "forward":
		inCluster = false
	case "auto":
	default:
		return fmt.Errorf("unknown --pod-dial %q", opts.podDial)
	}
	dialer := kube.NewPodDialer(restCfg, clientset, inCluster)

	if err := mgr.GetFieldIndexer().IndexField(ctx, &avsysv1.MinecraftServer{}, router.HostnameIndex, router.IndexHostname); err != nil {
		return err
	}
	if err := (&controller.Reconciler{
		Client: mgr.GetClient(),
		Reader: mgr.GetAPIReader(),
		Scheme: scheme,
		Config: controller.Config{
			WorkspaceImage:           opts.workspaceImage,
			WorkspaceImagePullPolicy: corev1.PullPolicy(opts.workspacePull),
			WorkspaceAuthorizedKeys:  string(ssh.MarshalAuthorizedKey(workspaceKey.PublicKey())),
			RouterPublicPort:         opts.routerPort,
			CurseForgeAPIKey:         os.Getenv("AVSYS_CURSEFORGE_API_KEY"),
		},
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.Add(&controller.Monitor{Client: mgr.GetClient(), Dialer: dialer, Interval: 10 * time.Second}); err != nil {
		return err
	}

	sshHost, sshPort := opts.sshPublicHost, opts.sshPublicPort
	if sshHost == "" {
		if u, err := url.Parse(opts.publicURL); err == nil {
			sshHost = u.Hostname()
		}
	}
	if sshPort == 0 {
		if _, port, err := net.SplitHostPort(opts.sshAddr); err == nil {
			sshPort, _ = strconv.Atoi(port)
		}
	}

	if opts.backupDir == "" {
		opts.backupDir = filepath.Join(opts.dataDir, "backups")
	}
	backups := &backup.Manager{Dir: opts.backupDir, Restic: opts.restic, Log: log.With("component", "backup")}
	if !backups.Available() {
		log.Warn("restic not found; snapshots are unavailable", "restic", opts.restic)
	}
	if opts.offsiteRepository != "" {
		// The password is the operator's to keep: it is what opens the copy
		// once everything the panel has is gone. It is never made up here.
		password := os.Getenv("AVSYS_OFFSITE_PASSWORD")
		if opts.offsitePasswordFile != "" {
			data, err := os.ReadFile(opts.offsitePasswordFile)
			if err != nil {
				return fmt.Errorf("reading the offsite password: %w", err)
			}
			password = strings.TrimSpace(string(data))
		}
		if password == "" {
			return errors.New("an offsite repository needs a password: set AVSYS_OFFSITE_PASSWORD or --offsite-password-file, and keep a copy of it somewhere else")
		}
		backups.Offsite = &backup.Offsite{Repository: opts.offsiteRepository, Password: password}
		log.Info("snapshots are copied offsite", "repository", opts.offsiteRepository)
	}
	gateway := &sshgw.Gateway{Store: st, Dialer: dialer, HostKey: hostKey, WorkspaceKey: workspaceKey, Log: log.With("component", "ssh")}
	server := &api.Server{
		Config: api.Config{
			PublicURL:       opts.publicURL,
			SSHHost:         sshHost,
			SSHPort:         sshPort,
			RouterDomain:    opts.routerDomain,
			NamespacePrefix: opts.namespacePrefix,
			AllowSignups:    opts.allowSignups,
			Providers:       oauth,
			Version:         version,
		},
		Store:   st,
		Client:  mgr.GetClient(),
		Kube:    clientset,
		Dialer:  dialer,
		Gateway: gateway,
		Backups: backups,
		Events:  api.NewHub(),
		Log:     log.With("component", "api"),
		Static:  staticFiles(opts.webDir, log),
	}
	gateway.Authz = server

	informer, err := mgr.GetCache().GetInformer(ctx, &avsysv1.MinecraftServer{})
	if err != nil {
		return err
	}
	if err := server.WatchServers(informer); err != nil {
		return err
	}

	// Runs once the cache has the servers in it.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		server.ResumeSaving(ctx)
		return nil
	})); err != nil {
		return err
	}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		server.RunSchedule(ctx)
		return nil
	})); err != nil {
		return err
	}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		server.RunOffsite(ctx)
		return nil
	})); err != nil {
		return err
	}

	httpServer := &http.Server{Addr: opts.httpAddr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		context.AfterFunc(ctx, func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdown)
		})
		log.Info("panel listening", "addr", opts.httpAddr, "url", opts.publicURL)
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})); err != nil {
		return err
	}
	if opts.sshAddr != "" {
		if err := mgr.Add(listener("ssh gateway", opts.sshAddr, log, gateway.Serve)); err != nil {
			return err
		}
	}
	if opts.routerAddr != "" {
		rt := &router.Router{Client: mgr.GetClient(), Dialer: dialer, Log: log.With("component", "router")}
		if err := mgr.Add(listener("minecraft router", opts.routerAddr, log, rt.Serve)); err != nil {
			return err
		}
	}

	go func() {
		// Sessions are checked for expiry when used; this only keeps the
		// table from accumulating dead rows.
		for range time.Tick(time.Hour) {
			_ = st.DeleteExpiredSessions(ctx)
		}
	}()
	return mgr.Start(ctx)
}

// installCRDs applies the bundled CRDs, so upgrading the panel also upgrades
// the schema it depends on.
func installCRDs(ctx context.Context, cfg *rest.Config) error {
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return err
	}
	entries, err := crds.Files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		raw, err := crds.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(raw, &obj.Object); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if err := c.Patch(ctx, obj, client.Apply, client.FieldOwner("avsys"), client.ForceOwnership); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if err := waitEstablished(ctx, c, obj.GetName()); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
	}
	return nil
}

// waitEstablished blocks until the API server serves a newly created CRD. On
// a fresh cluster that takes a moment, and starting the controller before
// then fails with "no matches for kind".
func waitEstablished(ctx context.Context, c client.Client, name string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		crd := &unstructured.Unstructured{}
		crd.SetAPIVersion("apiextensions.k8s.io/v1")
		crd.SetKind("CustomResourceDefinition")
		if err := c.Get(ctx, client.ObjectKey{Name: name}, crd); err == nil {
			conditions, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
			for _, raw := range conditions {
				if cond, ok := raw.(map[string]any); ok && cond["type"] == "Established" && cond["status"] == "True" {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("CRD %s was not served within a minute", name)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// listener adapts a Serve(ctx, net.Listener) function to a manager runnable.
func listener(name, addr string, log *slog.Logger, serve func(context.Context, net.Listener) error) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		log.Info(name+" listening", "addr", addr)
		return serve(ctx, ln)
	})
}

// staticFiles picks where the web UI is served from: a directory on disk when
// asked (for development), otherwise the copy compiled into the binary.
func staticFiles(dir string, log *slog.Logger) fs.FS {
	if dir != "" {
		return os.DirFS(dir)
	}
	if web.Dist == nil {
		log.Warn("built without the web UI; serving the API only (build with -tags embedweb, or pass --web-dir)")
		return nil
	}
	return web.Dist
}
