package api

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A loader build is for the type and game version it was pinned with. A
// change of settings that moves the server to another of either, and does
// not name a build, drops the old one; one that names a build keeps it.
func TestLoaderBuildGoesWithItsVersion(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	if got, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"pinned","spec":{"eula":true,"loader":"fabric","version":"1.21.4","loaderVersion":"0.16.9"}}`); got != 201 {
		t.Fatalf("creating: %d %s", got, msg)
	}
	software := func() string {
		t.Helper()
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "pinned"}, mc); err != nil {
			t.Fatal(err)
		}
		return string(mc.Spec.Loader) + " " + mc.Spec.Version + " " + mc.Spec.LoaderVersion
	}
	for _, step := range []struct{ patch, want string }{
		// Nothing about its software changed.
		{`"displayName":"Pinned"`, "fabric 1.21.4 0.16.9"},
		{`"version":"1.21.4"`, "fabric 1.21.4 0.16.9"},
		// Another game version, with no build named.
		{`"version":"26.3"`, "fabric 26.3 "},
		// One named with it is kept.
		{`"version":"1.21.4","loaderVersion":"0.16.9"`, "fabric 1.21.4 0.16.9"},
		{`"loader":"forge","loaderVersion":"54.1.0"`, "forge 1.21.4 54.1.0"},
		// Another type, with no build named.
		{`"loader":"fabric"`, "fabric 1.21.4 "},
		// Named on its own, for the version it has.
		{`"loaderVersion":"0.16.10"`, "fabric 1.21.4 0.16.10"},
		{`"loaderVersion":null`, "fabric 1.21.4 "},
	} {
		if got, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/pinned", `{"spec":{`+step.patch+`}}`); got != 200 {
			t.Fatalf("%s: %d %s", step.patch, got, msg)
		}
		if got := software(); got != step.want {
			t.Errorf("after %s: %q, want %q", step.patch, got, step.want)
		}
	}
}
