package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GT-Drehmal/avSYS/internal/release"
)

// The instance administrator is told on the Nodes page of a newer release,
// with the command that upgrades the panel as it was installed. Nobody else
// is, and a panel that is up to date, or a build of a commit, says nothing.
func TestNewerReleaseIsForTheAdministrator(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://github.com/GT-Drehmal/avSYS/releases/tag/v0.2.0"}`))
	}))
	defer github.Close()
	running := func(version string) {
		p.srv.Updates = &release.Checker{Current: version, URL: github.URL, Client: github.Client()}
		p.srv.Updates.Refresh(ctx)
	}
	update := func(user string) map[string]any {
		t.Helper()
		code, got := p.get(user, "GET", "/nodes", "")
		if code != 200 {
			t.Fatalf("nodes as %s: %d %v", user, code, got)
		}
		u, _ := got["update"].(map[string]any)
		return u
	}

	// Installed with the chart, as release games in namespace games.
	p.srv.Config.RouterService = types.NamespacedName{Namespace: "games", Name: "games"}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "games", Name: "games",
		Labels:      map[string]string{"helm.sh/chart": "avsys-0.1.0"},
		Annotations: map[string]string{"meta.helm.sh/release-name": "games", "meta.helm.sh/release-namespace": "games"}}}
	if err := p.srv.Client.Create(ctx, svc); err != nil {
		t.Fatal(err)
	}
	running("v0.1.0")
	u := update("root")
	if u["version"] != "v0.2.0" || u["notes"] != "https://github.com/GT-Drehmal/avSYS/releases/tag/v0.2.0" {
		t.Fatalf("update: %v", u)
	}
	if want := "helm upgrade games oci://ghcr.io/gt-drehmal/charts/avsys -n games --reset-then-reuse-values --version 0.2.0"; u["command"] != want {
		t.Errorf("command: %v, want %q", u["command"], want)
	}
	if _, ok := u["manifest"]; ok {
		t.Errorf("a Helm install is pointed at install.yaml: %v", u)
	}
	for _, user := range []string{"owner", "viewer"} {
		if u := update(user); u != nil {
			t.Errorf("%s, not the instance administrator, is told of a release: %v", user, u)
		}
	}

	// Installed from install.yaml.
	svc.Annotations = nil
	if err := p.srv.Client.Update(ctx, svc); err != nil {
		t.Fatal(err)
	}
	u = update("root")
	if u["command"] != "kubectl apply -f install.yaml" || u["manifest"] != "https://github.com/GT-Drehmal/avSYS/releases/download/v0.2.0/install.yaml" {
		t.Errorf("installed from install.yaml: %v", u)
	}

	// Not known, as outside the cluster: the release, without a command.
	p.srv.Config.RouterService = types.NamespacedName{}
	u = update("root")
	if u["version"] != "v0.2.0" || u["command"] != nil {
		t.Errorf("installed some other way: %v", u)
	}

	for _, version := range []string{"v0.2.0", "sha-1234567", "dev"} {
		running(version)
		if u := update("root"); u != nil {
			t.Errorf("running %s: told of %v", version, u)
		}
	}
	p.srv.Updates = nil
	if u := update("root"); u != nil {
		t.Errorf("with no looking for releases: %v", u)
	}
}
