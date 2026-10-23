package api

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
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

// What a request says about the game itself is used too, and the rest of it
// stays the snapshot's: a copy on the next game version, open to everyone.
func TestFromSnapshotTakesWhatIsAsked(t *testing.T) {
	recorded := &avsysv1.MinecraftServerSpec{
		EULA: true, Version: "1.21.4", Loader: avsysv1.LoaderPaper, LoaderVersion: "232", GameJava: 21,
		Mods:          []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Version: "abc"}},
		ResourcePack:  &avsysv1.ResourcePackSpec{URL: "https://example.org/pack.zip"},
		Properties:    map[string]string{"motd": "hello", "difficulty": "hard", "white-list": "true"},
		Ops:           []string{"Steve"},
		Whitelist:     []string{"Alex"},
		AutoOp:        "off",
		AutoWhitelist: "viewer",
	}
	given := func(body string) (*avsysv1.MinecraftServerSpec, map[string]json.RawMessage) {
		t.Helper()
		spec, named := &avsysv1.MinecraftServerSpec{}, map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(body), spec); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(body), &named); err != nil {
			t.Fatal(err)
		}
		return spec, named
	}

	// Nothing said: who is let in and made operator by role comes along.
	got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil)
	if got.AutoOp != "off" || got.AutoWhitelist != "viewer" {
		t.Errorf("membership settings did not come from the snapshot: autoOp %q, autoWhitelist %q", got.AutoOp, got.AutoWhitelist)
	}
	if got.GameJava != 21 || got.ResourcePack == nil {
		t.Errorf("java or resource pack did not come from the snapshot: %+v", got)
	}

	asked, named := given(`{"version":"1.21.5","properties":{"white-list":"false","difficulty":null,"pvp":"false"},"autoWhitelist":"off","autoOp":"","whitelist":[],"resourcePack":null}`)
	got = fromSnapshot(asked, recorded, named)
	if got.Version != "1.21.5" || got.Loader != avsysv1.LoaderPaper {
		t.Errorf("the version asked for was not used, or the loader was lost: %s %s", got.Loader, got.Version)
	}
	// A build of the loader and the Java are those of the version left behind.
	if got.LoaderVersion != "" || got.GameJava != 0 {
		t.Errorf("the old version's loader build or Java was kept: %q %d", got.LoaderVersion, got.GameJava)
	}
	want := map[string]string{"motd": "hello", "white-list": "false", "pvp": "false"}
	if len(got.Properties) != len(want) {
		t.Errorf("properties: got %v, want %v", got.Properties, want)
	}
	for name, value := range want {
		if got.Properties[name] != value {
			t.Errorf("property %s: got %q, want %q", name, got.Properties[name], value)
		}
	}
	if got.AutoWhitelist != "off" || got.AutoOp != "" || len(got.Whitelist) != 0 || got.ResourcePack != nil {
		t.Errorf("what was asked for did not win: %+v", got)
	}
	// Not mentioned, so as recorded.
	if len(got.Ops) != 1 || len(got.Mods) != 1 || got.Mods[0].Version != "abc" {
		t.Errorf("what was not mentioned did not come from the snapshot: %+v", got)
	}
	if recorded.Properties["difficulty"] != "hard" || len(recorded.Properties) != 3 {
		t.Error("merging the properties changed the record")
	}

	// A loader build is kept when it is asked for by name.
	asked, named = given(`{"version":"1.21.5","loaderVersion":"7"}`)
	if got = fromSnapshot(asked, recorded, named); got.LoaderVersion != "7" {
		t.Errorf("loader build: got %q, want 7", got.LoaderVersion)
	}
}

// A copy can be given other mods, plugins and datapacks than its original,
// but not another modpack, nor a file that was never uploaded to it.
func TestCheckCopiedContent(t *testing.T) {
	recorded := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "lithium", Version: "abc"},
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindMod, Project: "ours.jar"},
	}}
	mods := func(body string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"mods": json.RawMessage(body)}
	}
	for name, c := range map[string]struct {
		spec  avsysv1.MinecraftServerSpec
		given map[string]json.RawMessage
		ok    bool
	}{
		"nothing about content": {avsysv1.MinecraftServerSpec{Properties: map[string]string{"motd": "x"}}, nil, true},
		"one taken away":        {avsysv1.MinecraftServerSpec{Mods: recorded.Mods[1:]}, mods("[]"), true},
		"all taken away":        {avsysv1.MinecraftServerSpec{}, mods("[]"), true},
		"one added": {avsysv1.MinecraftServerSpec{Mods: append(slices.Clone(recorded.Mods), avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Project: "carpet"})},
			mods("[]"), true},
		"another modpack":  {avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Project: "x"}}, nil, false},
		"a file not there": {avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{{Source: avsysv1.SourceUpload, Project: "theirs.jar"}}}, mods("[]"), false},
	} {
		if err := checkCopiedContent(&c.spec, recorded, c.given); (err == nil) != c.ok {
			t.Errorf("%s: got %v, want allowed %v", name, err, c.ok)
		}
	}

	// What is not mentioned stays as the original has it; what is, replaces it.
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil); len(got.Mods) != 2 {
		t.Errorf("mods not mentioned: %v", got.Mods)
	}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{Mods: recorded.Mods[:1]}, recorded, mods("[]")); len(got.Mods) != 1 || got.Mods[0].Project != "lithium" {
		t.Errorf("mods given: %v", got.Mods)
	}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, mods("[]")); len(got.Mods) != 0 {
		t.Errorf("all taken away: %v", got.Mods)
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
	if mc.Status.Snapshots == nil || !strings.Contains(mc.Status.Snapshots.Error, "Delete it and create it again") {
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

// From its last snapshot until it is gone, nothing else changes a server
// that is being deleted: a change made after the copy would be lost with it.
func TestAServerBeingDeletedIsHeld(t *testing.T) {
	p := newPanel(t)
	const world = "/orgs/alpha/servers/world"
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindDeleteServer, ""); !ok {
		t.Fatal("could not claim the server")
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", world + "/start", ""},
		{"PATCH", world, `{"spec":{"displayName":"World"}}`},
		{"PUT", world + "/files/content?path=/a.txt", "x"},
		{"DELETE", world + "/files?path=/a.txt", ""},
		{"POST", world + "/files/rename?path=/a&to=/b", ""},
		{"DELETE", world + "?snapshot=false", ""},
	} {
		if got, code := p.do("owner", c.method, c.path, c.body); got != 409 || code != "deleting" {
			t.Errorf("%s %s while the server is being deleted: got %d %q, want 409 deleting", c.method, c.path, got, code)
		}
	}
	if _, err := p.srv.AuthorizeWorkspace(context.Background(), p.users["operator"], "alpha", "world"); err == nil || !strings.Contains(err.Error(), "being deleted") {
		t.Errorf("a shell was let into a server that is being deleted: %v", err)
	}
	// A second delete does not start a second one.
	p.srv.Backups.Restic = "true"
	if got, code := p.do("owner", "DELETE", world, ""); got != 409 || code != "deleting" {
		t.Errorf("deleting a server that is being deleted: got %d %q, want 409 deleting", got, code)
	}
}

// A copy of a server is asked for through the same route, and everything
// that can be refused is refused before the snapshot it is made from.
func TestCreateFromServerRequests(t *testing.T) {
	p := newPanel(t)
	p.addServer("beta", "theirs", "")
	post := func(who, body string) (int, string) { return p.do(who, "POST", "/orgs/alpha/servers", body) }

	if got, code := post("owner", `{"name":"copy","fromServer":"world","fromSnapshot":"abcdef12"}`); got != 400 || code != "invalid" {
		t.Errorf("from a server and a snapshot at once: got %d %q, want 400 invalid", got, code)
	}
	if got, code := post("owner", `{"name":"copy","fromServer":"world"}`); got != 503 || code != "backups_unavailable" {
		t.Errorf("without restic: got %d %q, want 503 backups_unavailable", got, code)
	}
	// As any other way of making a server, it is for admins.
	if got, code := post("operator", `{"name":"copy","fromServer":"world"}`); got != 403 {
		t.Errorf("an operator copying a server: got %d %q, want 403", got, code)
	}

	p.srv.Backups.Restic = "true"
	for name, c := range map[string]struct {
		body string
		want int
		code string
	}{
		"a server that is not there":    {`{"name":"copy","fromServer":"nobody"}`, 404, "not_found"},
		"a name that is taken":          {`{"name":"world","fromServer":"world"}`, 409, "conflict"},
		"another modpack":               {`{"name":"copy","fromServer":"world","spec":{"modpack":{"source":"modrinth","project":"fabulously-optimized"}}}`, 400, "invalid"},
		"a file not uploaded to it":     {`{"name":"copy","fromServer":"world","spec":{"mods":[{"source":"upload","kind":"mod","project":"theirs.jar"}]}}`, 400, "invalid"},
		"a setting that is not one":     {`{"name":"copy","fromServer":"world","spec":{"autoWhitelist":"everyone"}}`, 400, "invalid"},
		"a game setting on two lines":   {`{"name":"copy","fromServer":"world","spec":{"properties":{"motd":"a\nb"}}}`, 400, "invalid"},
		"a state that is not one":       {`{"name":"copy","fromServer":"world","spec":{"state":"Bogus"}}`, 400, "invalid"},
		"another organization's server": {`{"name":"copy","fromServer":"theirs"}`, 404, "not_found"},
		"a pack given with a bad hash":  {`{"name":"copy","fromServer":"world","spec":{"resourcePack":{"url":"https://example.org/p.zip","sha1":"nope"}}}`, 400, "invalid"},
	} {
		if got, code := post("owner", c.body); got != c.want || code != c.code {
			t.Errorf("%s: got %d %q, want %d %s", name, got, code, c.want, c.code)
		}
	}
	// None of that took a snapshot of the original, or left anything behind.
	if p.srv.ops.current("mc-alpha", "world") != nil {
		t.Error("a refused copy left the original claimed")
	}
	if got, _ := p.do("owner", "GET", "/orgs/alpha/servers/copy", ""); got != 404 {
		t.Errorf("a server was left behind by a refused request: %d", got)
	}

	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	// Its pack, given a hash before hashes were checked, is the original's
	// own: made required on the copy, it is taken as it is.
	world.Spec.ResourcePack = &avsysv1.ResourcePackSpec{URL: "https://example.org/old.zip", SHA1: "typed-by-hand"}
	if err := p.srv.Client.Update(context.Background(), world); err != nil {
		t.Fatal(err)
	}
	// Stopped at a setting checked after it, so that what refuses it says.
	req := httptest.NewRequest("POST", "/api/v1/orgs/alpha/servers", strings.NewReader(`{"name":"copy","fromServer":"world","spec":{"state":"Bogus","resourcePack":{"url":"https://example.org/old.zip","sha1":"typed-by-hand","enforce":true}}}`))
	req.Header.Set("Authorization", "Bearer "+p.tokens["owner"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "SHA-1") {
		t.Errorf("the original's own pack, made required, was refused: %s", rec.Body)
	}

	// One that is in the middle of a restore is not copied as it is.
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	world.Annotations = map[string]string{avsysv1.AnnotationRestoring: "aaaa1111"}
	if err := p.srv.Client.Update(context.Background(), world); err != nil {
		t.Fatal(err)
	}
	if got, code := post("owner", `{"name":"copy","fromServer":"world"}`); got != 409 {
		t.Errorf("copying a server whose restore did not finish: got %d %q, want 409", got, code)
	}
}

// A copy of a server takes when it is snapshotted and the nodes it is kept
// to from the server itself, unless the request says otherwise.
func TestFromLive(t *testing.T) {
	live := &avsysv1.MinecraftServerSpec{
		Backups:      &avsysv1.BackupSpec{Enabled: new(false)},
		NodeSelector: map[string]string{"kubernetes.io/hostname": "pi"},
	}
	requested := &avsysv1.MinecraftServerSpec{}
	fromLive(requested, nil, live)
	if requested.Backups == nil || requested.NodeSelector["kubernetes.io/hostname"] != "pi" {
		t.Errorf("not taken from the server: %+v %v", requested.Backups, requested.NodeSelector)
	}
	requested.NodeSelector["kubernetes.io/hostname"] = "changed"
	if live.NodeSelector["kubernetes.io/hostname"] != "pi" {
		t.Error("the copy's nodes are the original's own map")
	}

	asked := &avsysv1.MinecraftServerSpec{NodeSelector: map[string]string{"kubernetes.io/hostname": "big"}}
	fromLive(asked, map[string]json.RawMessage{"nodeSelector": nil, "backups": nil}, live)
	if asked.Backups != nil || asked.NodeSelector["kubernetes.io/hostname"] != "big" {
		t.Errorf("the request did not win: %+v %v", asked.Backups, asked.NodeSelector)
	}
}

// A server made from a snapshot is put on the kind of storage the original
// was on unless the request names one: where a cluster has no default kind,
// a volume asked for without one is never provided.
func TestFromSnapshotKeepsTheStorageClass(t *testing.T) {
	fast, slow := "fast", "slow"
	recorded := &avsysv1.MinecraftServerSpec{Storage: avsysv1.StorageSpec{Size: resource.MustParse("20Gi"), StorageClassName: &fast}}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil); got.Storage.StorageClassName == nil || *got.Storage.StorageClassName != "fast" {
		t.Errorf("with none asked for: %v, want the original's", got.Storage.StorageClassName)
	}
	asked := &avsysv1.MinecraftServerSpec{Storage: avsysv1.StorageSpec{Size: resource.MustParse("30Gi"), StorageClassName: &slow}}
	if got := fromSnapshot(asked, recorded, nil); *got.Storage.StorageClassName != "slow" || got.Storage.Size.String() != "30Gi" {
		t.Errorf("with one asked for: %v %s, want slow and 30Gi", *got.Storage.StorageClassName, got.Storage.Size.String())
	}
	// And it is its own copy, not the record's to change through.
	got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil)
	*got.Storage.StorageClassName = "changed"
	if fast != "fast" {
		t.Error("the new server's storage class is the record's own variable")
	}
}

// A copy takes the original's resource pack unless another is asked for.
// Its uploaded one, only made required or given a prompt, is still the one
// in its files, which a request cannot say it has.
func TestFromSnapshotResourcePack(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	recorded := &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://panel.example/packs/" + sha + ".zip", SHA1: sha, Uploaded: &avsysv1.UploadedPack{Name: "p.zip", Size: 9}, Formats: &avsysv1.PackFormats{Min: 15, Max: 15}}}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil); got.ResourcePack == nil || got.ResourcePack.Uploaded == nil {
		t.Errorf("none asked: %+v, want the original's", got.ResourcePack)
	}
	said := map[string]json.RawMessage{"resourcePack": json.RawMessage(`{}`)}
	asked := &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: recorded.ResourcePack.URL, Enforce: true, Prompt: "Please"}}
	got := fromSnapshot(asked, recorded, said)
	if rp := got.ResourcePack; rp == nil || rp.Uploaded == nil || rp.SHA1 != sha || !rp.Enforce || rp.Prompt != "Please" || rp.Formats == nil || rp.Formats.Min != 15 {
		t.Errorf("made required: %+v, want the uploaded one, required, with its formats", rp)
	}
	// One joined from several is still joined from them, and from those the
	// original has, not any the request says.
	recorded.ResourcePack.Stack = []avsysv1.StackedPack{{SHA1: "aa"}, {SHA1: "bb"}}
	asked = &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: recorded.ResourcePack.URL, Enforce: true, Stack: []avsysv1.StackedPack{{SHA1: "made-up"}}}}
	if rp := fromSnapshot(asked, recorded, said).ResourcePack; rp == nil || len(rp.Stack) != 2 || rp.Stack[0].SHA1 != "aa" {
		t.Errorf("joined, made required: %+v, want the original's stack", rp)
	}
	if rp := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil).ResourcePack; rp == nil || len(rp.Stack) != 2 {
		t.Errorf("joined, none asked: %+v, want the original's stack", rp)
	}
	recorded.ResourcePack.Stack = nil
	asked = &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://cdn.modrinth.example/p.zip", SHA1: strings.Repeat("cd", 20), Project: &avsysv1.PackProject{Source: "modrinth", ID: "faithful"}}}
	if rp := fromSnapshot(asked, recorded, said).ResourcePack; rp == nil || rp.Uploaded != nil || rp.Project == nil {
		t.Errorf("another picked: %+v, want the picked one", rp)
	}
	if rp := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, map[string]json.RawMessage{"resourcePack": json.RawMessage(`null`)}).ResourcePack; rp != nil {
		t.Errorf("none asked for: %+v, want none", rp)
	}
}

// What a snapshot recorded of its server is shown to every member, but what
// decides which code runs, and may hold secrets, only to those who could
// make a server out of it.
func TestRecordFor(t *testing.T) {
	spec := avsysv1.MinecraftServerSpec{
		Version: "1.21.4", Image: "example.org/custom", Properties: map[string]string{"motd": "hello"},
		Env: []corev1.EnvVar{{Name: "SECRET_TOKEN", Value: "do-not-show"}},
		JVM: avsysv1.JVMSpec{ExtraFlags: "-Dpassword=do-not-show"},
	}
	for _, role := range []store.Role{store.RoleViewer, store.RoleOperator} {
		got := recordFor(role, spec)
		if got.Image != "" || got.Env != nil || got.JVM.ExtraFlags != "" {
			t.Errorf("a %s was shown the image, environment or extra JVM flags: %q %v %q", role, got.Image, got.Env, got.JVM.ExtraFlags)
		}
		if got.Version != "1.21.4" || got.Properties["motd"] != "hello" {
			t.Errorf("a %s was not shown the rest: %+v", role, got)
		}
	}
	for _, role := range []store.Role{store.RoleAdmin, store.RoleOwner} {
		if got := recordFor(role, spec); got.Image != "example.org/custom" || len(got.Env) != 1 || got.JVM.ExtraFlags == "" {
			t.Errorf("an %s was not shown the image and environment: %+v", role, got)
		}
	}
	if spec.Image == "" || len(spec.Env) != 1 {
		t.Error("showing the record to a viewer changed it")
	}
}

// notCopied names every setting a server made from another does not take
// from it, and why. Everything else is taken, which TestEverySettingIsDecided
// holds every setting to: one added later has to be put on one side or the
// other.
var notCopied = map[string]string{
	"State": "the request says whether the new server starts",
	// For the reason given at restoreSoftware: these decide what code runs,
	// and are read out of a repository the server itself could add to.
	"Image": "only when the request sends it back",
	"Env":   "only when the request sends it back",
	// A server's address is its own.
	"Expose.Hostname":           "an address is the original's own",
	"Expose.LoadBalancerIP":     "an address is the original's own",
	"Expose.NodePort":           "an address is the original's own",
	"Expose.ServiceAnnotations": "they often claim an address, as the others do",
	"Expose.Type":               "the request says how the new server is reached",
	// Not taken from a snapshot's record: createFromServer takes these from
	// the server itself when the request does not say.
	"Backups":        "when it is snapshotted is not recorded",
	"NodeSelector":   "a record, which the server could have written, does not say where a server runs",
	"JVM.ExtraFlags": "they run code; a record, which the server could have written, does not say what",
}

// TestEverySettingIsDecided fills in every setting of a server, makes a new
// one from it with nothing asked, and checks each setting was taken, or is
// one notCopied names.
func TestEverySettingIsDecided(t *testing.T) {
	recorded := &avsysv1.MinecraftServerSpec{}
	fill(reflect.ValueOf(recorded).Elem())
	spec := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil)

	seen := map[string]bool{}
	var walk func(path string, was, got reflect.Value)
	walk = func(path string, was, got reflect.Value) {
		for i := range was.NumField() {
			f := was.Type().Field(i)
			name := strings.TrimPrefix(path+"."+f.Name, ".")
			seen[name] = true
			// The groups of settings are looked into, setting by setting.
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[resource.Quantity]() {
				walk(name, was.Field(i), got.Field(i))
				continue
			}
			same := reflect.DeepEqual(was.Field(i).Interface(), got.Field(i).Interface())
			if _, mine := notCopied[name]; mine && same {
				t.Errorf("%s is said not to be copied, but was", name)
			}
			if _, mine := notCopied[name]; !mine && !same {
				t.Errorf("%s was not copied: it is %v and the copy has %v. Copy it, or say in notCopied why not", name, was.Field(i).Interface(), got.Field(i).Interface())
			}
		}
	}
	walk("", reflect.ValueOf(recorded).Elem(), reflect.ValueOf(&spec).Elem())
	for name := range notCopied {
		if !seen[name] {
			t.Errorf("notCopied names %s, which is not a setting", name)
		}
	}
}

// fill gives every field it can reach a value that is not its zero.
func fill(v reflect.Value) {
	if v.Type() == reflect.TypeFor[resource.Quantity]() {
		v.Set(reflect.ValueOf(resource.MustParse("3Gi")))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x" + v.Type().Name())
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(key)
		fill(value)
		v.SetMapIndex(key, value)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	}
}

// notRestored names every setting a restore leaves as it is now, and why.
// Everything else goes back to what the snapshot recorded.
var notRestored = map[string]string{
	"DisplayName":      "a restore puts back files, not what the server is called",
	"State":            "whether it runs is settled by the restore itself",
	"EULA":             "accepted for the server, not for its files",
	"Resources":        "what it is given to run on stays as it is now",
	"Storage":          "what it is given to run on stays as it is now",
	"Expose":           "its address stays as it is now",
	"Ports":            "how it is reached stays as it is now, as its address does",
	"Sleep":            "what it is given to run on stays as it is now",
	"Backups":          "when it is snapshotted is not recorded",
	"AutoOp":           "who is an operator by role is decided now, not by the files",
	"AutoWhitelist":    "who can join by role is decided now, not by the files",
	"OpMembers":        "as AutoOp: the members picked are decided now",
	"WhitelistMembers": "as AutoWhitelist: the members picked are decided now",
	// For the reason given at restoreSoftware.
	"Image":        "not taken from a record the server could have written",
	"Env":          "not taken from a record the server could have written",
	"NodeSelector": "not taken from a record the server could have written",
	"JVM":          "how Java is started is not what the files depend on",
}

// TestEverySettingIsDecidedForARestore is TestEverySettingIsDecided for
// putting a server back to one of its own snapshots.
func TestEverySettingIsDecidedForARestore(t *testing.T) {
	recorded := &avsysv1.MinecraftServerSpec{}
	fill(reflect.ValueOf(recorded).Elem())
	now := &avsysv1.MinecraftServerSpec{}
	restoreSoftware(now, recorded)
	was, got := reflect.ValueOf(recorded).Elem(), reflect.ValueOf(now).Elem()
	left := maps.Clone(notRestored)
	for i := range was.NumField() {
		name := was.Type().Field(i).Name
		same := reflect.DeepEqual(was.Field(i).Interface(), got.Field(i).Interface())
		_, kept := notRestored[name]
		if kept && same {
			t.Errorf("%s is said to stay as it is, but was restored", name)
		}
		if !kept && !same {
			t.Errorf("%s was not restored. Restore it in restoreSoftware, or say in notRestored why not", name)
		}
		delete(left, name)
	}
	for name := range left {
		t.Errorf("notRestored names %s, which is not a setting", name)
	}
}

// A name that a deleted server's snapshots are under is for bringing that
// server back. A new server, or a copy of another, cannot take it: it would
// have that history for its own.
func TestANameGoesWithItsHistory(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	dir := p.withOffsite("restic")
	repo, key := filepath.Join(dir, "alpha"), filepath.Join(dir, "alpha.key")
	if err := os.WriteFile(key, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restic := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("restic", append([]string{"--repo", repo, "--password-file", key, "-q"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	restic("", "init")
	// The final snapshot of a server named gone, which was deleted, and one
	// of world, which is still there.
	for _, server := range []string{"gone", "world"} {
		restic("a world", "backup", "--stdin", "--stdin-filename", "data/level.dat", "--host", server, "--tag", "avsys", "--tag", "trigger=final")
	}
	var list []struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal([]byte(restic("", "snapshots", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	gone := ""
	for _, s := range list {
		if s.Hostname == "gone" {
			gone = s.ID[:8]
		}
	}

	post := func(body string) (int, string) { return p.do("owner", "POST", "/orgs/alpha/servers", body) }
	for name, c := range map[string]struct {
		body string
		want int
		code string
	}{
		"a new server under it":  {`{"name":"gone","spec":{"eula":true,"version":"1.21.4"}}`, 409, "name_has_history"},
		"a copy under it":        {`{"name":"gone","fromServer":"world"}`, 409, "name_has_history"},
		"a server that is there": {`{"name":"world","spec":{"eula":true,"version":"1.21.4"}}`, 409, "conflict"},
		"a name with no history": {`{"name":"fresh","spec":{"eula":true,"version":"1.21.4"}}`, 201, ""},
	} {
		if got, code := post(c.body); got != c.want || code != c.code {
			t.Errorf("%s: got %d %q, want %d %q", name, got, code, c.want, c.code)
		}
	}
	// Brought back under its own name, it gets as far as reading what the
	// snapshot recorded, which this one has none of.
	if got, code := post(`{"name":"gone","fromSnapshot":"` + gone + `"}`); got != 422 || code != "no_record" {
		t.Errorf("bringing it back: got %d %q, want 422 no_record", got, code)
	}
	// Made from its snapshot under another name, it is a new server, and the
	// other name is free.
	if got, code := post(`{"name":"elsewhere","fromSnapshot":"` + gone + `"}`); got != 422 || code != "no_record" {
		t.Errorf("a new server from its snapshot: got %d %q, want 422 no_record", got, code)
	}
}

// A CPU limit asked to be none is none; one not mentioned is the original's.
func TestACopysCPULimit(t *testing.T) {
	two := resource.MustParse("2")
	recorded := &avsysv1.MinecraftServerSpec{Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi"), CPU: &two}}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil); got.Resources.CPU == nil || got.Resources.CPU.String() != "2" {
		t.Errorf("not mentioned: %v", got.Resources.CPU)
	}
	given := map[string]json.RawMessage{"resources": json.RawMessage(`{"memory":"4Gi","cpu":null}`)}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, given); got.Resources.CPU != nil {
		t.Errorf("asked for none: %v", got.Resources.CPU)
	}
	given = map[string]json.RawMessage{"resources": json.RawMessage(`{"memory":"8Gi"}`)}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, given); got.Resources.CPU == nil {
		t.Error("memory alone changed the CPU limit")
	}
}

// dialHeld is a pod dialer that is never answered, until it is called off.
type dialHeld struct{ dialed chan struct{} }

func (d dialHeld) DialPod(ctx context.Context, _, _ string, _ int) (net.Conn, error) {
	select {
	case d.dialed <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// A copy of a server as it is now is made and answered with at once, held
// while the snapshot its files come from is taken. Deleting it calls that
// off, and lets the original go.
func TestACopyIsAnsweredBeforeItsSnapshot(t *testing.T) {
	p := newPanel(t)
	p.srv.Backups.Restic = "true"
	held := dialHeld{dialed: make(chan struct{}, 1)}
	p.srv.Gateway = &sshgw.Gateway{Dialer: held}

	if got, code := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"copy","fromServer":"world","spec":{"state":"Running"}}`); got != 201 {
		t.Fatalf("copying a server: got %d %q, want 201 at once", got, code)
	}
	select {
	case <-held.dialed:
	case <-time.After(10 * time.Second):
		t.Fatal("the original's files were never asked for")
	}
	copied := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "copy"}, copied); err != nil {
		t.Fatal(err)
	}
	// Made stopped and held, whatever it is to be once its files are in.
	if copied.Spec.State != avsysv1.StateStopped || copied.Annotations[avsysv1.AnnotationCreating] != snapshotNotYetTaken || !restoreUnfinished(copied) {
		t.Errorf("the copy is not held while its snapshot is taken: %s %v", copied.Spec.State, copied.Annotations)
	}
	if op := p.srv.ops.current("mc-alpha", "copy"); op == nil || op.Kind != kindCreate || !strings.HasPrefix(op.Stage, "Taking a snapshot of") {
		t.Errorf("the copy's page does not say what it waits for: %+v", op)
	}
	if op := p.srv.ops.current("mc-alpha", "world"); op == nil || op.Kind != kindSnapshot {
		t.Errorf("the original is not held while it is snapshotted: %+v", op)
	}
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers/copy/start", ""); got != 409 {
		t.Errorf("starting a copy whose files are not in: got %d %q, want 409", got, code)
	}

	if got, code := p.do("owner", "DELETE", "/orgs/alpha/servers/copy", ""); got != 204 {
		t.Fatalf("deleting the copy: got %d %q", got, code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.srv.ops.current("mc-alpha", "world") != nil || p.srv.ops.current("mc-alpha", "copy") != nil {
		if time.Now().After(deadline) {
			t.Fatal("the original or the copy is still held after the copy was deleted")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
