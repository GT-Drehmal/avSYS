package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A restore brings back what the files depend on and leaves alone what the
// server is given to run on, and what code it runs.
func TestRestoreSoftware(t *testing.T) {
	now := avsysv1.MinecraftServerSpec{
		DisplayName: "Now", State: avsysv1.StateRunning, EULA: true,
		Version: "1.21.5", Loader: avsysv1.LoaderPaper,
		Mods:       []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Project: "luckperms"}},
		Properties: map[string]string{"difficulty": "hard"},
		Resources:  avsysv1.ResourcesSpec{Memory: resource.MustParse("8Gi")},
		Storage:    avsysv1.StorageSpec{Size: resource.MustParse("20Gi")},
		Expose:     avsysv1.ExposeSpec{Type: avsysv1.ExposeRouter, Hostname: "now.example.org"},
		Sleep:      avsysv1.SleepSpec{Enabled: true, IdleMinutes: 30},
		Image:      "itzg/minecraft-server:java21",
		Env:        []corev1.EnvVar{{Name: "USE_AIKAR_FLAGS", Value: "true"}},
	}
	then := avsysv1.MinecraftServerSpec{
		DisplayName: "Then", State: avsysv1.StateStopped,
		Version: "1.21.4", Loader: avsysv1.LoaderFabric, LoaderVersion: "0.16.9",
		Mods:         []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Project: "lithium"}},
		ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://example.org/pack.zip"},
		Properties:   map[string]string{"difficulty": "easy"},
		Ops:          []string{"Steve"},
		Whitelist:    []string{"Alex"},
		Resources:    avsysv1.ResourcesSpec{Memory: resource.MustParse("2Gi")},
		Storage:      avsysv1.StorageSpec{Size: resource.MustParse("5Gi")},
		Expose:       avsysv1.ExposeSpec{Type: avsysv1.ExposeNone},
		Image:        "example.org/not-what-the-admin-chose",
		Env:          []corev1.EnvVar{{Name: "TYPE", Value: "CUSTOM"}},
	}
	got := now.DeepCopy()
	restoreSoftware(got, &then)

	if got.Version != "1.21.4" || got.Loader != avsysv1.LoaderFabric || got.LoaderVersion != "0.16.9" {
		t.Errorf("software: %s %s %s", got.Loader, got.Version, got.LoaderVersion)
	}
	if len(got.Mods) != 1 || got.Mods[0].Project != "lithium" || got.ResourcePack == nil {
		t.Errorf("content: %+v %+v", got.Mods, got.ResourcePack)
	}
	if got.Properties["difficulty"] != "easy" || len(got.Ops) != 1 || len(got.Whitelist) != 1 {
		t.Errorf("gameplay: %v %v %v", got.Properties, got.Ops, got.Whitelist)
	}
	if got.DisplayName != "Now" || got.State != avsysv1.StateRunning || !got.EULA {
		t.Errorf("identity and state changed: %q %s %v", got.DisplayName, got.State, got.EULA)
	}
	if got.Resources.Memory.String() != "8Gi" || got.Storage.Size.String() != "20Gi" || got.Expose.Hostname != "now.example.org" || !got.Sleep.Enabled {
		t.Errorf("what the server runs on changed: %+v %+v %+v %+v", got.Resources, got.Storage, got.Expose, got.Sleep)
	}
	if got.Image != "itzg/minecraft-server:java21" || len(got.Env) != 1 || got.Env[0].Name != "USE_AIKAR_FLAGS" {
		t.Errorf("image or environment was taken from the snapshot: %q %+v", got.Image, got.Env)
	}
}

func TestSoftwareOf(t *testing.T) {
	plain := avsysv1.MinecraftServerSpec{Version: "1.21.4", Loader: avsysv1.LoaderPaper}
	if v, l := softwareOf(&plain); v != "1.21.4" || l != "paper" {
		t.Errorf("plain server: %q %q", v, l)
	}
	// A modpack decides both, whatever the fields beside it say.
	pack := avsysv1.MinecraftServerSpec{Version: "LATEST", Loader: avsysv1.LoaderVanilla, Modpack: &avsysv1.ModpackSpec{GameVersion: "1.20.1", Loader: "fabric"}}
	if v, l := softwareOf(&pack); v != "1.20.1" || l != "fabric" {
		t.Errorf("modpack: %q %q", v, l)
	}
}

func TestOneOperationPerServer(t *testing.T) {
	var ops operations
	first, ok := ops.begin("mc-alpha", "alpha", "world", kindSnapshot)
	if !ok || first.Org != "alpha" || first.Server != "world" {
		t.Fatalf("first operation: %+v, %v", first, ok)
	}
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore); ok {
		t.Error("a second operation started on a busy server")
	}
	// Another server, and a server of the same name elsewhere, are free.
	if _, ok := ops.begin("mc-alpha", "alpha", "creative", kindSnapshot); !ok {
		t.Error("another server in the organization was blocked")
	}
	if _, ok := ops.begin("mc-beta", "beta", "world", kindSnapshot); !ok {
		t.Error("a server of the same name in another organization was blocked")
	}
	if got := ops.inNamespace("mc-alpha"); len(got) != 2 {
		t.Errorf("operations in alpha: %+v", got)
	}
	// "mc-alpha" must not match a namespace that merely starts with it.
	if _, ok := ops.begin("mc-alpha-two", "alpha-two", "world", kindSnapshot); !ok {
		t.Fatal("could not start in a third namespace")
	}
	if got := ops.inNamespace("mc-alpha"); len(got) != 2 {
		t.Errorf("operations in alpha after one started in alpha-two: %+v", got)
	}
	// What callers are handed is a copy.
	current := ops.current("mc-alpha", "world")
	current.Stage = "changed by the caller"
	if ops.current("mc-alpha", "world").Stage != "Starting" {
		t.Error("a caller could change a running operation")
	}
	ops.end("mc-alpha", "world")
	if ops.current("mc-alpha", "world") != nil {
		t.Error("the operation is still there after it ended")
	}
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore); !ok {
		t.Error("a finished server stayed busy")
	}
}

// A panel that was stopped in the middle of a snapshot leaves the game with
// saving off and a note on the server. The next panel acts on the note.
func TestResumeSaving(t *testing.T) {
	p := newPanel(t)
	p.srv.Kube = kubefake.NewSimpleClientset()
	ctx := context.Background()
	note := func(name string) (string, bool) {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: name}, mc); err != nil {
			t.Fatal(err)
		}
		v, ok := mc.Annotations[avsysv1.AnnotationSavingPaused]
		return v, ok
	}
	add := func(name string, phase avsysv1.Phase, paused bool) {
		mc := &avsysv1.MinecraftServer{
			ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: name},
			Spec:       avsysv1.MinecraftServerSpec{EULA: true, Version: "1.21.4"},
			Status:     avsysv1.MinecraftServerStatus{Phase: phase},
		}
		if paused {
			mc.Annotations = map[string]string{avsysv1.AnnotationSavingPaused: "2026-10-12T00:00:00Z"}
		}
		if err := p.srv.Client.Create(ctx, mc); err != nil {
			t.Fatal(err)
		}
	}
	add("stopped-since", avsysv1.PhaseStopped, true)
	add("still-running", avsysv1.PhaseRunning, true)
	add("untouched", avsysv1.PhaseRunning, false)

	// A snapshot running right now paused saving on purpose.
	add("mid-snapshot", avsysv1.PhaseStopped, true)
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "mid-snapshot", kindSnapshot); !ok {
		t.Fatal("could not start an operation")
	}

	p.srv.resumeSaving(ctx)

	if _, ok := note("mid-snapshot"); !ok {
		t.Error("the note was cleared under a snapshot that is still running")
	}

	// A game that has stopped since starts with saving on; nothing to send.
	if _, ok := note("stopped-since"); ok {
		t.Error("the note stayed on a server that is no longer running")
	}
	// This game's console cannot be reached here, so saving was not switched
	// back on. The note has to stay, or nobody would try again.
	if _, ok := note("still-running"); !ok {
		t.Error("the note was cleared although saving could not be switched back on")
	}
	if _, ok := note("untouched"); ok {
		t.Error("a server that was never paused got a note")
	}

	// Setting and clearing the note leaves the rest of the server alone.
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	if err := p.srv.markSavingPaused(ctx, key, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := note("world"); !ok {
		t.Error("the note was not set")
	}
	if err := p.srv.markSavingPaused(ctx, key, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := note("world"); ok {
		t.Error("the note was not cleared")
	}
}

// While a server's files are being put back, nothing else may change it.
func TestNothingChangesAServerBeingRestored(t *testing.T) {
	p := newPanel(t)
	changes := []struct{ method, path, body string }{
		{"POST", "/orgs/alpha/servers/world/start", ""},
		{"POST", "/orgs/alpha/servers/world/stop", ""},
		{"POST", "/orgs/alpha/servers/world/restart", ""},
		{"PATCH", "/orgs/alpha/servers/world", `{"spec":{"displayName":"World"}}`},
		{"PUT", "/orgs/alpha/servers/world/content?name=a.jar&kind=mod", "x"},
		{"DELETE", "/orgs/alpha/servers/world", ""},
	}

	// A snapshot does not get in the way of running the server.
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindSnapshot); !ok {
		t.Fatal("could not start a snapshot")
	}
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers/world/stop", ""); got != 200 {
		t.Errorf("stop during a snapshot: got %d %s, want 200", got, code)
	}
	p.srv.ops.end("mc-alpha", "world")

	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindRestore); !ok {
		t.Fatal("could not start a restore")
	}
	for _, c := range changes {
		if got, code := p.do("owner", c.method, c.path, c.body); got != 409 || code != "restoring" {
			t.Errorf("%s %s during a restore: got %d %q, want 409 restoring", c.method, c.path, got, code)
		}
	}
	// Looking is still fine, and so is another server.
	if got, _ := p.do("viewer", "GET", "/orgs/alpha/servers/world", ""); got != 200 {
		t.Errorf("reading a server during its restore: got %d", got)
	}
	// A second snapshot or restore is refused as busy.
	p.srv.Backups.Restic = "true"
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers/world/snapshots", ""); got != 409 || code != "busy" {
		t.Errorf("snapshot during a restore: got %d %q, want 409 busy", got, code)
	}

	p.srv.ops.end("mc-alpha", "world")
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers/world/stop", ""); got != 200 {
		t.Errorf("stop after the restore ended: got %d %s, want 200", got, code)
	}
}

// An organization whose repository cannot be read is not deleted on the
// assumption that it is empty.
func TestDeletingAnOrganizationWithAnUnreadableRepository(t *testing.T) {
	p := newPanel(t)
	// Beta has no servers. Give it a repository that this panel, having no
	// restic, cannot look inside.
	repository := filepath.Join(p.srv.Backups.Dir, "beta")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "config"), []byte("not a real repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("outsider", "DELETE", "/orgs/beta", ""); got != 409 || code != "has_snapshots" {
		t.Fatalf("plain delete: got %d %q, want 409 has_snapshots", got, code)
	}
	if got, _ := p.do("outsider", "GET", "/orgs/beta", ""); got != 200 {
		t.Fatalf("the organization is gone after a refused delete: %d", got)
	}

	// Asked for by name, it goes, repository and all, and the name is free
	// with nothing left under it.
	if got, code := p.do("outsider", "DELETE", "/orgs/beta?snapshots=delete", ""); got != 204 {
		t.Fatalf("confirmed delete: got %d %q, want 204", got, code)
	}
	if _, err := os.Stat(repository); !os.IsNotExist(err) {
		t.Errorf("the repository is still there: %v", err)
	}
}

// If the repository cannot be removed, the organization keeps its name, so
// that nobody else can come to own what is left under it.
func TestAnOrganizationOutlivesARepositoryThatCannotBeRemoved(t *testing.T) {
	p := newPanel(t)
	// A file where the directory of repositories should be makes removal fail.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.srv.Backups.Dir = filepath.Join(blocked, "under-a-file")

	if got, _ := p.do("outsider", "DELETE", "/orgs/beta?snapshots=delete", ""); got != 500 {
		t.Fatalf("delete with an unremovable repository: got %d, want 500", got)
	}
	if got, _ := p.do("outsider", "GET", "/orgs/beta", ""); got != 200 {
		t.Errorf("the organization was deleted although its repository could not be: %d", got)
	}
}
