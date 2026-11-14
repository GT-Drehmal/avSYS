package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// "Latest" set on a server is the newest version its software has a build
// for now, which the server keeps: in a change of settings as when it is
// made. When that cannot be found out it is refused, not saved as "Latest".
func TestLatestIsSettledWhenSet(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	if got, _ := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"picked","spec":{"eula":true,"version":"1.21.4"}}`); got != 201 {
		t.Fatalf("creating: %d", got)
	}
	patch := func(spec string) (int, string) {
		return p.do("owner", "PATCH", "/orgs/alpha/servers/picked", `{"spec":{`+spec+`}}`)
	}
	if got, code := patch(`"version":"LATEST"`); got != 200 || p.versionOf("picked") != "26.3" {
		t.Errorf("set to latest: %d %s, on %s, want 26.3", got, code, p.versionOf("picked"))
	}
	// The newest of the software it is changed to.
	if got, code := patch(`"loader":"folia","version":"LATEST"`); got != 200 || p.versionOf("picked") != "26.2" {
		t.Errorf("set to Folia's latest: %d %s, on %s, want 26.2", got, code, p.versionOf("picked"))
	}
	// A server still on "Latest" from before is left for PinLatest, unless
	// its version is set.
	legacy := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "old"},
		Spec: avsysv1.MinecraftServerSpec{
			EULA: true, State: avsysv1.StateStopped, Version: "LATEST", Loader: avsysv1.LoaderPaper,
			Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")}, Storage: avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
		},
	}
	if err := p.srv.Client.Create(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/servers/old", `{"spec":{"displayName":"Old"}}`); got != 200 || p.versionOf("old") != "LATEST" {
		t.Errorf("renamed: %d %s, on %s, want it left on LATEST", got, code, p.versionOf("old"))
	}
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/servers/old", `{"spec":{"version":"LATEST"}}`); got != 200 || p.versionOf("old") != "26.3" {
		t.Errorf("latest picked again: %d %s, on %s, want 26.3", got, code, p.versionOf("old"))
	}
	// Taken off a modpack, it gets a version of its own.
	pack := legacy.DeepCopy()
	pack.ObjectMeta = metav1.ObjectMeta{Namespace: legacy.Namespace, Name: "pack"}
	pack.Spec.Loader = avsysv1.LoaderVanilla
	pack.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "pack", Version: "v1", GameVersion: "1.20.1", Loader: "fabric"}
	if err := p.srv.Client.Create(context.Background(), pack); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/servers/pack", `{"spec":{"modpack":null}}`); got != 200 || p.versionOf("pack") != "26.3" {
		t.Errorf("taken off its modpack: %d %s, on %s, want 26.3", got, code, p.versionOf("pack"))
	}

	// With the lists down, "Latest" is refused, and nothing changes.
	clear(p.srv.catalog.entries)
	p.srv.catalog.http.Transport = offline{}
	_, out := p.get("owner", "PATCH", "/orgs/alpha/servers/picked", `{"spec":{"loader":"paper","version":"LATEST"}}`)
	if out["code"] != "invalid" || out["message"] != errNoLatest.Error() || p.versionOf("picked") != "26.2" {
		t.Errorf("latest with the lists down: %v, on %s, want it refused", out, p.versionOf("picked"))
	}
	if got, code := patch(`"displayName":"Still fine"`); got != 200 {
		t.Errorf("another change with the lists down: %d %s", got, code)
	}
}

// A copy, a server brought back or one made from a template's version whose
// settings say "Latest", as a snapshot from before this did, or a template
// keeps, gets the newest version there is then.
func TestLatestIsSettledFromASnapshot(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	org, err := p.srv.Store.OrgBySlug(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	a := &access{Org: org, Role: store.RoleOwner, Namespace: p.srv.Namespace("alpha")}
	recorded := &avsysv1.MinecraftServerSpec{
		EULA: true, Version: "LATEST", Loader: avsysv1.LoaderFolia,
		Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")}, Storage: avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
	}
	spec, err := p.srv.specFromSnapshot(context.Background(), a, "copy", &avsysv1.MinecraftServerSpec{}, recorded.DeepCopy(), nil)
	if err != nil || spec.Version != "26.2" {
		t.Errorf("from a snapshot on latest: %s %v, want 26.2", spec.Version, err)
	}
	// One that names a version keeps it.
	recorded.Version = "1.21.4"
	if spec, err := p.srv.specFromSnapshot(context.Background(), a, "copy", &avsysv1.MinecraftServerSpec{}, recorded.DeepCopy(), nil); err != nil || spec.Version != "1.21.4" {
		t.Errorf("from a snapshot on 1.21.4: %s %v", spec.Version, err)
	}
	// Asked for in the request.
	if spec, err := p.srv.specFromSnapshot(context.Background(), a, "copy", &avsysv1.MinecraftServerSpec{Version: "LATEST"}, recorded.DeepCopy(), map[string]json.RawMessage{"version": nil}); err != nil || spec.Version != "26.2" {
		t.Errorf("latest asked for a copy: %s %v, want 26.2", spec.Version, err)
	}

	clear(p.srv.catalog.entries)
	p.srv.catalog.http.Transport = offline{}
	recorded.Version = "LATEST"
	if _, err := p.srv.specFromSnapshot(context.Background(), a, "copy", &avsysv1.MinecraftServerSpec{}, recorded.DeepCopy(), nil); err != errNoLatest {
		t.Errorf("from a snapshot on latest, with the lists down: %v, want it refused", err)
	}
}

// Servers saved on "Latest" before are kept, once, on the version they run,
// or for one that never ran, the newest. One that cannot be told is left,
// and one that could not be settled for want of the lists is tried again.
func TestPinLatest(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	active := metav1.Now()
	type was struct {
		loader  avsysv1.Loader
		version string
		ran     string
		active  *metav1.Time
		modpack bool
	}
	servers := map[string]was{
		"paper":    {avsysv1.LoaderPaper, "LATEST", "Paper 1.21.4", &active, false},
		"vanilla":  {avsysv1.LoaderVanilla, "LATEST", "26.2", &active, false},
		"never":    {avsysv1.LoaderFolia, "LATEST", "", nil, false},
		"silent":   {avsysv1.LoaderPaper, "LATEST", "", &active, false},
		"proxied":  {avsysv1.LoaderPaper, "LATEST", "Velocity 3.4.0-SNAPSHOT", &active, false},
		"no-build": {avsysv1.LoaderFolia, "LATEST", "Paper 26.3", &active, false},
		"pack":     {avsysv1.LoaderVanilla, "LATEST", "1.20.1", &active, true},
		"fixed":    {avsysv1.LoaderPaper, "1.21.5", "Paper 1.21.4", &active, false},
	}
	for name, w := range servers {
		mc := &avsysv1.MinecraftServer{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: name},
			Spec: avsysv1.MinecraftServerSpec{
				EULA: true, State: avsysv1.StateRunning, Version: w.version, Loader: w.loader,
				Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")}, Storage: avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
			},
		}
		if w.modpack {
			mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "pack", GameVersion: "1.20.1", Loader: "fabric"}
		}
		if err := p.srv.Client.Create(ctx, mc); err != nil {
			t.Fatal(err)
		}
		mc.Status.Version, mc.Status.LastActive = w.ran, w.active
		if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
			t.Fatal(err)
		}
	}
	versions := func() map[string]string {
		out := map[string]string{}
		for name := range servers {
			out[name] = p.versionOf(name)
		}
		return out
	}

	// With the lists down nothing can be checked: tried again later.
	if again := p.srv.pinLatest(ctx); !again {
		t.Error("with the lists down, nothing is to be tried again")
	}
	for name, v := range versions() {
		if want := servers[name].version; v != want {
			t.Errorf("with the lists down, %s was put on %s", name, v)
		}
	}

	clear(p.srv.catalog.entries)
	p.srv.catalog.http.Transport = upstream
	if again := p.srv.pinLatest(ctx); again {
		t.Error("with the lists up, something is to be tried again")
	}
	want := map[string]string{
		"paper": "1.21.4", "vanilla": "26.2", "never": "26.2",
		"silent": "LATEST", "proxied": "LATEST", "no-build": "LATEST", "pack": "LATEST", "fixed": "1.21.5",
	}
	got := versions()
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s is on %s, want %s", name, got[name], v)
		}
	}
	// Once is enough: done again, it changes nothing.
	before := map[string]string{}
	for name := range servers {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: name}, mc); err != nil {
			t.Fatal(err)
		}
		before[name] = mc.ResourceVersion
	}
	p.srv.pinLatest(ctx)
	for name := range servers {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: name}, mc); err != nil {
			t.Fatal(err)
		}
		if mc.ResourceVersion != before[name] {
			t.Errorf("%s was changed again", name)
		}
	}

	// It gives up once there is nothing left to try.
	done := make(chan struct{})
	go func() {
		p.srv.PinLatest(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("PinLatest goes on with nothing left to try")
	}
}
