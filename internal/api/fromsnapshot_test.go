package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A server made from a snapshot runs what the snapshot's files were made
// by. What it is given to run on can be chosen again, and what decides which
// code runs is never taken from the snapshot.
func TestFromSnapshot(t *testing.T) {
	recorded := &avsysv1.MinecraftServerSpec{
		DisplayName: "Survival", EULA: true, State: avsysv1.StateRunning,
		Version: "1.21.4", Loader: avsysv1.LoaderPaper, LoaderVersion: "232",
		Mods:       []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Version: "abc"}},
		Properties: map[string]string{"motd": "hello"}, Ops: []string{"Steve"}, Whitelist: []string{"Alex"},
		Resources:    avsysv1.ResourcesSpec{Memory: resource.MustParse("6Gi")},
		Storage:      avsysv1.StorageSpec{Size: resource.MustParse("20Gi")},
		Sleep:        avsysv1.SleepSpec{Enabled: true, IdleMinutes: 30},
		Expose:       avsysv1.ExposeSpec{Type: avsysv1.ExposeLoadBalancer, Hostname: "play.example.org", LoadBalancerIP: "192.0.2.7"},
		Image:        "example.org/not-the-usual-image",
		Env:          []corev1.EnvVar{{Name: "JVM_OPTS", Value: "-javaagent:/data/agent.jar"}},
		NodeSelector: map[string]string{"kubernetes.io/hostname": "elsewhere"},
	}

	got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil)
	if got.Version != "1.21.4" || got.Loader != avsysv1.LoaderPaper || got.LoaderVersion != "232" || len(got.Mods) != 1 || got.Mods[0].Version != "abc" ||
		got.Properties["motd"] != "hello" || len(got.Ops) != 1 || len(got.Whitelist) != 1 {
		t.Errorf("the software did not come from the snapshot: %+v", got)
	}
	if !got.EULA || got.DisplayName != "Survival" || got.Resources.Memory.String() != "6Gi" || got.Storage.Size.String() != "20Gi" || !got.Sleep.Enabled || got.Sleep.IdleMinutes != 30 {
		t.Errorf("what was left out did not default to the snapshot's: %+v", got)
	}
	if got.Image != "" || got.Env != nil || got.NodeSelector != nil {
		t.Errorf("the image, environment or node came from the snapshot: %q %v %v", got.Image, got.Env, got.NodeSelector)
	}
	if got.Expose.Hostname != "" || got.Expose.Type != "" || got.Expose.LoadBalancerIP != "" {
		t.Errorf("the address came from the snapshot: %+v", got.Expose)
	}
	if got.State != "" {
		t.Errorf("the state came from the snapshot: %q", got.State)
	}
	// The record is not shared with the server made from it.
	got.Properties["motd"] = "changed"
	if recorded.Properties["motd"] != "hello" {
		t.Error("changing the new server's settings changed the record")
	}

	// What the request does say wins, for the things it may say.
	asked := &avsysv1.MinecraftServerSpec{
		DisplayName: "Copy", State: avsysv1.StateStopped,
		Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("2Gi")},
		Storage:   avsysv1.StorageSpec{Size: resource.MustParse("5Gi")},
		Sleep:     avsysv1.SleepSpec{IdleMinutes: 5},
		Expose:    avsysv1.ExposeSpec{Hostname: "copy.example.org"},
		Image:     "example.org/asked-for",
		Env:       []corev1.EnvVar{{Name: "TZ", Value: "UTC"}},
	}
	got = fromSnapshot(asked, recorded, map[string]json.RawMessage{"sleep": nil})
	if got.DisplayName != "Copy" || got.State != avsysv1.StateStopped || got.Resources.Memory.String() != "2Gi" || got.Storage.Size.String() != "5Gi" ||
		got.Sleep.Enabled || got.Sleep.IdleMinutes != 5 || got.Expose.Hostname != "copy.example.org" || got.Image != "example.org/asked-for" || len(got.Env) != 1 || got.Env[0].Name != "TZ" {
		t.Errorf("the request did not win: %+v", got)
	}
	if got.Version != "1.21.4" {
		t.Errorf("the software did not come from the snapshot: %+v", got)
	}

	// Sleep switched off is a choice, and not the same as sleep not mentioned.
	off := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, map[string]json.RawMessage{"sleep": json.RawMessage(`{"enabled":false}`)})
	if off.Sleep.Enabled {
		t.Error("a request that switched sleep off got the snapshot's sleep setting")
	}
}

func TestSoftwareGiven(t *testing.T) {
	if softwareGiven(&avsysv1.MinecraftServerSpec{DisplayName: "x", EULA: true, Image: "y", Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("2Gi")}}) {
		t.Error("a request that names no software was refused")
	}
	for name, spec := range map[string]avsysv1.MinecraftServerSpec{
		"version":    {Version: "1.21.4"},
		"loader":     {Loader: avsysv1.LoaderFabric},
		"modpack":    {Modpack: &avsysv1.ModpackSpec{Project: "x"}},
		"mods":       {Mods: []avsysv1.ContentRef{{Project: "x"}}},
		"properties": {Properties: map[string]string{"motd": "x"}},
		"ops":        {Ops: []string{"Steve"}},
		"whitelist":  {Whitelist: []string{"Steve"}},
		"pack":       {ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://example.org/p.zip"}},
	} {
		if !softwareGiven(&spec) {
			t.Errorf("a request that sets the %s was let through", name)
		}
	}
}

// A server that never received all of its files is not run, snapshotted or
// restored. It can be looked at and deleted.
func TestAHalfMadeServerIsHeld(t *testing.T) {
	p := newPanel(t)
	p.srv.Backups.Restic = "true" // present, so requests get as far as these checks
	ctx := context.Background()
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "mc-alpha", Name: "copy",
			Annotations: map[string]string{avsysv1.AnnotationRestoring: "aaaa1111", avsysv1.AnnotationCreating: "aaaa1111"},
		},
		Spec: avsysv1.MinecraftServerSpec{EULA: true, Version: "1.21.4", Loader: avsysv1.LoaderVanilla, State: avsysv1.StateStopped},
	}
	p.srv.applyDefaults("alpha", "copy", &mc.Spec)
	if err := p.srv.Client.Create(ctx, mc); err != nil {
		t.Fatal(err)
	}
	p.setPhase("alpha", "copy", avsysv1.PhaseStopped)
	const copy = "/orgs/alpha/servers/copy"

	for _, refused := range []struct{ method, path, body string }{
		{"POST", copy + "/start", ""},
		{"POST", copy + "/restart", ""},
		{"PATCH", copy, `{"spec":{"state":"Running"}}`},
		{"POST", copy + "/snapshots", ""},
		{"POST", copy + "/snapshots/aaaa1111/restore", ""},
	} {
		if got, code := p.do("owner", refused.method, refused.path, refused.body); got != 409 || code != "create_incomplete" {
			t.Errorf("%s %s on a half-made server: got %d %q, want 409 create_incomplete", refused.method, refused.path, got, code)
		}
	}

	// The panel that finds it with nothing running says what to do about it.
	p.srv.resumeSaving(ctx)
	key := client.ObjectKeyFromObject(mc)
	if err := p.srv.Client.Get(ctx, key, mc); err != nil {
		t.Fatal(err)
	}
	if mc.Status.Snapshots == nil || !strings.Contains(mc.Status.Snapshots.Error, "Delete it and make it again") {
		t.Errorf("the server does not say it was never completed: %+v", mc.Status.Snapshots)
	}

	if got, _ := p.do("viewer", "GET", copy, ""); got != 200 {
		t.Errorf("reading a half-made server: got %d", got)
	}
	if got, code := p.do("owner", "DELETE", copy, ""); got != 204 {
		t.Errorf("deleting a half-made server: got %d %s, want 204", got, code)
	}
}

// While a new server's files are being copied in, it is held exactly as a
// server being restored is.
func TestANewServerIsHeldWhileItsFilesArrive(t *testing.T) {
	p := newPanel(t)
	const world = "/orgs/alpha/servers/world"
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindCreate, "aaaa1111"); !ok {
		t.Fatal("could not claim the server")
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", world + "/start", ""},
		{"POST", world + "/stop", ""},
		{"PATCH", world, `{"spec":{"displayName":"World"}}`},
		{"PUT", world + "/files/content?path=/a.txt", "x"},
	} {
		if got, code := p.do("owner", c.method, c.path, c.body); got != 409 || code != "creating" {
			t.Errorf("%s %s while files arrive: got %d %q, want 409 creating", c.method, c.path, got, code)
		}
	}
	if _, err := p.srv.AuthorizeWorkspace(context.Background(), p.users["operator"], "alpha", "world"); err == nil || !strings.Contains(err.Error(), "made from a snapshot") {
		t.Errorf("a shell was let in while files arrive: %v", err)
	}
	// The snapshot it is being made from stays until it is done.
	if _, ok := p.srv.ops.remove("mc-alpha", "aaaa1111"); ok {
		t.Error("the snapshot a server is being made from could be deleted")
	}
	// A second server of the same name cannot be claimed meanwhile.
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindCreate, "bbbb2222"); ok {
		t.Error("the same server was claimed twice")
	}
	p.srv.ops.end("mc-alpha", "world")
	if got, code := p.do("owner", "POST", world+"/stop", ""); got != 200 {
		t.Errorf("stop once the files are in: got %d %s, want 200", got, code)
	}
}

// Making a server from a snapshot is asked for through the same route as
// any other server, and held to the same rules about names.
func TestCreateFromSnapshotRequests(t *testing.T) {
	p := newPanel(t)
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"Not A Name","fromSnapshot":"abcdef12"}`); got != 400 || code != "invalid" {
		t.Errorf("a bad name: got %d %q, want 400 invalid", got, code)
	}
	// With no restic there is nothing to make it from, and nothing is made.
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"copy","fromSnapshot":"abcdef12"}`); got != 503 || code != "backups_unavailable" {
		t.Errorf("without restic: got %d %q, want 503 backups_unavailable", got, code)
	}
	if got, _ := p.do("owner", "GET", "/orgs/alpha/servers/copy", ""); got != 404 {
		t.Errorf("a server was left behind by a refused request: %d", got)
	}
	if p.srv.ops.current("mc-alpha", "copy") != nil {
		t.Error("a refused request left the name claimed")
	}

	// A state that is not one is refused now, not after the files are copied.
	p.srv.Backups.Restic = "true"
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"copy","fromSnapshot":"abcdef12","spec":{"state":"Bogus"}}`); got != 400 || code != "invalid" {
		t.Errorf("a state that is not one: got %d %q, want 400 invalid", got, code)
	}

	// A name that is taken is refused without touching the server that has it.
	p.srv.Backups.Restic = "true"
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"world","fromSnapshot":"abcdef12"}`); got != 409 || code != "conflict" {
		t.Errorf("a name that is taken: got %d %q, want 409 conflict", got, code)
	}
	if p.srv.ops.current("mc-alpha", "world") != nil {
		t.Error("the server that has the name was claimed")
	}
}

// Deleting a server that is still being made from a snapshot calls that off
// and deletes it, instead of making its owner wait for a copy nobody wants.
// A restore is not called off by anything.
func TestDeletingAServerCallsOffItsCreation(t *testing.T) {
	p := newPanel(t)
	const world = "/orgs/alpha/servers/world"

	op, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindCreate, "aaaa1111")
	if !ok {
		t.Fatal("could not claim the server")
	}
	working, stop := context.WithCancel(context.Background())
	p.srv.ops.stoppable(op, stop)
	// The copy, which winds down a moment after it is told to.
	go func() {
		<-working.Done()
		time.Sleep(150 * time.Millisecond)
		p.srv.finishOperation(context.Background(), "mc-alpha", "world", kindCreate, nil, nil, working.Err())
	}()
	if got, code := p.do("owner", "DELETE", world, ""); got != 204 {
		t.Fatalf("deleting a server that is being made: got %d %s, want 204", got, code)
	}
	if working.Err() == nil {
		t.Error("the copy was not told to stop")
	}
	if got, _ := p.do("owner", "GET", world, ""); got != 404 {
		t.Errorf("the server is still there: %d", got)
	}

	p.addServer("alpha", "other", "")
	restoring, ok := p.srv.ops.begin("mc-alpha", "alpha", "other", kindRestore, "aaaa1111")
	if !ok {
		t.Fatal("could not start a restore")
	}
	stopped := false
	p.srv.ops.stoppable(restoring, func() { stopped = true })
	if got, code := p.do("owner", "DELETE", "/orgs/alpha/servers/other", ""); got != 409 || code != "restoring" {
		t.Errorf("deleting a server that is being restored: got %d %q, want 409 restoring", got, code)
	}
	if stopped {
		t.Error("a restore was called off by a delete")
	}
}

// Files are only put into a pod that was made for the new server. One of the
// same name that belongs to a server deleted a moment ago has the old volume,
// and both are on their way out.
func TestOwnPod(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "back", UID: "new"}}
	yes := true
	pod := func(owner string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace: "mc-alpha", Name: "back",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "avsys.dev/v1alpha1", Kind: "MinecraftServer", Name: "back", UID: types.UID(owner), Controller: &yes}},
		}}
	}
	wait := func() error {
		limited, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		return p.srv.ownPod(limited, mc)
	}

	if err := wait(); err == nil {
		t.Error("a pod was found where there is none")
	}
	old := pod("old")
	if err := p.srv.Client.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil {
		t.Fatal("the pod of a deleted server of the same name was taken for the new server's")
	}
	if err := p.srv.Client.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := p.srv.Client.Create(ctx, pod("new")); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Errorf("the new server's own pod was not accepted: %v", err)
	}
}

// Deleting a server takes a last snapshot of it unless told not to. Told not
// to, it is simply deleted. While something else is running on the server,
// the last snapshot cannot be taken, and the server is not deleted.
func TestDeletingWithoutAFinalSnapshot(t *testing.T) {
	p := newPanel(t)
	p.srv.Backups.Restic = "true" // present, so that a final snapshot would be attempted
	p.addServer("alpha", "doomed", "")
	p.addServer("alpha", "busy", "")

	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "busy", kindSnapshot, ""); !ok {
		t.Fatal("could not start a snapshot")
	}
	if got, code := p.do("owner", "DELETE", "/orgs/alpha/servers/busy", ""); got != 409 || code != "busy" {
		t.Errorf("deleting a server that is being snapshotted: got %d %q, want 409 busy", got, code)
	}
	if got, _ := p.do("owner", "GET", "/orgs/alpha/servers/busy", ""); got != 200 {
		t.Errorf("a server whose last snapshot could not be taken was deleted all the same: %d", got)
	}

	if got, code := p.do("owner", "DELETE", "/orgs/alpha/servers/doomed?snapshot=false", ""); got != 204 {
		t.Fatalf("deleting with no final snapshot: got %d %s, want 204", got, code)
	}
	if got, _ := p.do("owner", "GET", "/orgs/alpha/servers/doomed", ""); got != 404 {
		t.Errorf("the server is still there: %d", got)
	}
	if p.srv.ops.current("mc-alpha", "doomed") != nil {
		t.Error("a claim was left on a deleted server")
	}
}
