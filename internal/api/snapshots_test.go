package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	first, ok := ops.begin("mc-alpha", "alpha", "world", kindSnapshot, "")
	if !ok || first.Org != "alpha" || first.Server != "world" {
		t.Fatalf("first operation: %+v, %v", first, ok)
	}
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore, ""); ok {
		t.Error("a second operation started on a busy server")
	}
	// Another server, and a server of the same name elsewhere, are free.
	if _, ok := ops.begin("mc-alpha", "alpha", "creative", kindSnapshot, ""); !ok {
		t.Error("another server in the organization was blocked")
	}
	if _, ok := ops.begin("mc-beta", "beta", "world", kindSnapshot, ""); !ok {
		t.Error("a server of the same name in another organization was blocked")
	}
	if got := ops.inNamespace("mc-alpha"); len(got) != 2 {
		t.Errorf("operations in alpha: %+v", got)
	}
	// "mc-alpha" must not match a namespace that merely starts with it.
	if _, ok := ops.begin("mc-alpha-two", "alpha-two", "world", kindSnapshot, ""); !ok {
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
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore, ""); !ok {
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
		mc.Status.Phase = phase
		if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
			t.Fatal(err)
		}
	}
	add("stopped-since", avsysv1.PhaseStopped, true)
	add("still-running", avsysv1.PhaseRunning, true)
	add("untouched", avsysv1.PhaseRunning, false)
	add("on-its-way-down", avsysv1.PhaseStopping, true)

	// A snapshot running right now paused saving on purpose.
	add("mid-snapshot", avsysv1.PhaseStopped, true)
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "mid-snapshot", kindSnapshot, ""); !ok {
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
	// The game that was told to hold its saves may still be there.
	if _, ok := note("on-its-way-down"); !ok {
		t.Error("the note was cleared while the game was still shutting down")
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

// While a server's files are being put back, nothing else may change it,
// through any route. A snapshot only keeps the game from being started or
// restarted under it.
func TestWhatAnOperationHoldsOff(t *testing.T) {
	p := newPanel(t)
	const world = "/orgs/alpha/servers/world"
	changes := []struct{ method, path, body string }{
		{"POST", world + "/start", ""},
		{"POST", world + "/stop", ""},
		{"POST", world + "/restart", ""},
		{"PATCH", world, `{"spec":{"displayName":"World"}}`},
		{"PUT", world + "/content?name=a.jar&kind=mod", "x"},
		{"PUT", world + "/files/content?path=/a.txt", "x"},
		{"DELETE", world + "/files?path=/a.txt", ""},
		{"POST", world + "/files/mkdir?path=/a", ""},
		{"POST", world + "/files/rename?path=/a&to=/b", ""},
		{"DELETE", world, ""},
	}

	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindSnapshot, ""); !ok {
		t.Fatal("could not start a snapshot")
	}
	for _, action := range []string{"start", "restart"} {
		if got, code := p.do("owner", "POST", world+"/"+action, ""); got != 409 || code != "busy" {
			t.Errorf("%s during a snapshot: got %d %q, want 409 busy", action, got, code)
		}
	}
	// Starting by setting the state is starting all the same.
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"state":"Running"}}`); got != 409 || code != "busy" {
		t.Errorf("setting the state to Running during a snapshot: got %d %q, want 409 busy", got, code)
	}
	// Stopping and other settings are not held up by a snapshot.
	if got, code := p.do("owner", "POST", world+"/stop", ""); got != 200 {
		t.Errorf("stop during a snapshot: got %d %s, want 200", got, code)
	}
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"displayName":"World"}}`); got != 200 {
		t.Errorf("renaming during a snapshot: got %d %s, want 200", got, code)
	}
	p.srv.ops.end("mc-alpha", "world")

	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindRestore, "aaaa1111"); !ok {
		t.Fatal("could not start a restore")
	}
	for _, c := range changes {
		if got, code := p.do("owner", c.method, c.path, c.body); got != 409 || code != "restoring" {
			t.Errorf("%s %s during a restore: got %d %q, want 409 restoring", c.method, c.path, got, code)
		}
	}
	// Looking is still fine.
	if got, _ := p.do("viewer", "GET", world, ""); got != 200 {
		t.Errorf("reading a server during its restore: got %d", got)
	}
	// A second snapshot or restore is refused as busy.
	p.srv.Backups.Restic = "true"
	if got, code := p.do("owner", "POST", world+"/snapshots", ""); got != 409 || code != "busy" {
		t.Errorf("snapshot during a restore: got %d %q, want 409 busy", got, code)
	}

	p.srv.ops.end("mc-alpha", "world")
	if got, code := p.do("owner", "POST", world+"/stop", ""); got != 200 {
		t.Errorf("stop after the restore ended: got %d %s, want 200", got, code)
	}
}

// Claims on a server and on a snapshot are decided under one lock, so that
// neither side can slip in between the other's check and its act.
func TestClaims(t *testing.T) {
	var ops operations

	// A change being applied holds off the start of an operation.
	release, blocker := ops.change("mc-alpha", "world", kindRestore)
	if blocker != nil {
		t.Fatalf("a change was refused with nothing running: %+v", blocker)
	}
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore, "aaaa1111"); ok {
		t.Error("a restore started while a change was being applied")
	}
	// Changes do not hold each other off.
	second, blocker := ops.change("mc-alpha", "world", kindRestore)
	if blocker != nil {
		t.Errorf("two changes at once: second refused by %+v", blocker)
	}
	second()
	release()
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore, "aaaa1111"); !ok {
		t.Fatal("a restore could not start after the changes finished")
	}

	// Now the restore holds off changes that ask about restores, and tells
	// them what is in the way.
	if _, blocker := ops.change("mc-alpha", "world", kindRestore); blocker == nil || blocker.Kind != kindRestore {
		t.Errorf("a change was admitted during a restore: %+v", blocker)
	}
	if release, blocker := ops.change("mc-alpha", "world", kindSnapshot); blocker != nil {
		t.Errorf("a change that only minds snapshots was refused by a restore: %+v", blocker)
	} else {
		release()
	}

	// The snapshot being restored to cannot be deleted; another can.
	if _, ok := ops.remove("mc-alpha", "aaaa1111"); ok {
		t.Error("the snapshot a restore is using was allowed to be deleted")
	}
	if _, ok := ops.remove("mc-beta", "aaaa1111"); !ok {
		t.Error("a snapshot of the same ID in another organization was held")
	}
	done, ok := ops.remove("mc-alpha", "bbbb2222")
	if !ok {
		t.Fatal("an unrelated snapshot could not be deleted")
	}
	// And while that one is being deleted, no restore to it can start.
	ops.end("mc-alpha", "world")
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore, "bbbb2222"); ok {
		t.Error("a restore started to a snapshot that is being deleted")
	}
	if _, ok := ops.remove("mc-alpha", "bbbb2222"); ok {
		t.Error("the same snapshot was allowed to be deleted twice at once")
	}
	done()
	if _, ok := ops.begin("mc-alpha", "alpha", "world", kindRestore, "bbbb2222"); !ok {
		t.Error("a restore could not start once the deletion was over")
	}
}

// A restore that was cut short leaves its mark on the server. Until a
// restore completes, the game is not started on those files by any route.
func TestAnUnfinishedRestoreKeepsTheGameOff(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	const world = "/orgs/alpha/servers/world"

	if err := p.srv.markRestoring(ctx, key, "aaaa1111"); err != nil {
		t.Fatal(err)
	}
	for _, start := range []struct{ method, path, body string }{
		{"POST", world + "/start", ""},
		{"POST", world + "/restart", ""},
		{"PATCH", world, `{"spec":{"state":"Running"}}`},
	} {
		if got, code := p.do("owner", start.method, start.path, start.body); got != 409 || code != "restore_incomplete" {
			t.Errorf("%s %s after an unfinished restore: got %d %q, want 409 restore_incomplete", start.method, start.path, got, code)
		}
	}
	// Everything short of running the game still works.
	if got, code := p.do("owner", "PATCH", world, `{"spec":{"displayName":"World"}}`); got != 200 {
		t.Errorf("renaming after an unfinished restore: got %d %s", got, code)
	}

	// The panel that finds the mark, with nothing running, says so on the server.
	p.srv.resumeSaving(ctx)
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, key, mc); err != nil {
		t.Fatal(err)
	}
	if mc.Status.Snapshots == nil || !strings.Contains(mc.Status.Snapshots.Error, "interrupted") {
		t.Errorf("the server does not say its restore was interrupted: %+v", mc.Status.Snapshots)
	}
	if !restoreUnfinished(mc) {
		t.Error("reporting the interruption cleared the mark")
	}

	// A restore that completes clears it.
	if err := p.srv.markRestoring(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("owner", "POST", world+"/start", ""); got != 200 {
		t.Errorf("start once a restore has completed: got %d %s, want 200", got, code)
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

// A snapshot is only taken of a server whose files hold still: one that is
// running, where the game can be told to hold its saves, or one with no game.
func TestSnapshotsWaitForASettledServer(t *testing.T) {
	p := newPanel(t)
	p.srv.Backups.Restic = "true" // present, so requests get as far as these checks
	const path = "/orgs/alpha/servers/world/snapshots"

	for _, phase := range []avsysv1.Phase{avsysv1.PhaseStarting, avsysv1.PhaseStopping, avsysv1.PhaseError, ""} {
		p.setPhase("alpha", "world", phase)
		if got, code := p.do("owner", "POST", path, ""); got != 409 || code != "unsettled" {
			t.Errorf("snapshot of a server that is %q: got %d %q, want 409 unsettled", phase, got, code)
		}
	}
	if p.srv.ops.current("mc-alpha", "world") != nil {
		t.Error("a refused snapshot left the server claimed")
	}

	// What an interrupted restore left is not kept as a state of the server.
	p.setPhase("alpha", "world", avsysv1.PhaseStopped)
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	if err := p.srv.markRestoring(context.Background(), key, "aaaa1111"); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("owner", "POST", path, ""); got != 409 || code != "restore_incomplete" {
		t.Errorf("snapshot after an unfinished restore: got %d %q, want 409 restore_incomplete", got, code)
	}
}

// No new shell or file transfer is opened into a server while its files are
// being put back.
func TestNoNewShellsDuringARestore(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	if _, err := p.srv.AuthorizeWorkspace(ctx, p.users["operator"], "alpha", "world"); err != nil {
		t.Fatalf("before the restore: %v", err)
	}
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindRestore, "aaaa1111"); !ok {
		t.Fatal("could not start a restore")
	}
	_, err := p.srv.AuthorizeWorkspace(ctx, p.users["owner"], "alpha", "world")
	if err == nil || !strings.Contains(err.Error(), "being restored") {
		t.Errorf("during the restore: %v", err)
	}
	// Someone with no access learns nothing new from it.
	_, outside := p.srv.AuthorizeWorkspace(ctx, p.users["outsider"], "alpha", "world")
	if outside == nil || strings.Contains(outside.Error(), "restored") {
		t.Errorf("an outsider during the restore: %v", outside)
	}
	// A snapshot does not keep people out.
	p.srv.ops.end("mc-alpha", "world")
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindSnapshot, ""); !ok {
		t.Fatal("could not start a snapshot")
	}
	if _, err := p.srv.AuthorizeWorkspace(ctx, p.users["operator"], "alpha", "world"); err != nil {
		t.Errorf("during a snapshot: %v", err)
	}
}
