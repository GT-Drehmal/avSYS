package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
	"github.com/GT-Drehmal/avSYS/internal/release"
)

// selfPanel is a test panel running current, installed as Deployment
// avsys/avsys, with GitHub saying latest is out.
func selfPanel(t *testing.T, current, latest string) (*panel, *kubefake.Clientset) {
	t.Helper()
	p := newPanel(t)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"` + latest + `","html_url":"https://github.com/GT-Drehmal/avSYS/releases/tag/` + latest + `"}`))
	}))
	t.Cleanup(github.Close)
	p.srv.Config.Version = current
	p.srv.Updates = &release.Checker{Current: current, URL: github.URL, Client: github.Client()}
	p.srv.Updates.Refresh(context.Background())
	kube := kubefake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "avsys", Name: "avsys"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "avsys",
			Image: "ghcr.io/gt-drehmal/avsys:" + current,
			Env: []corev1.EnvVar{
				{Name: "AVSYS_PUBLIC_URL", Value: "https://panel.example.com"},
				{Name: "AVSYS_WORKSPACE_IMAGE", Value: "registry.lan/avsys-workspace:" + current},
			},
		}}}}},
	})
	p.srv.Kube = kube
	p.srv.Config.Deployment = types.NamespacedName{Namespace: "avsys", Name: "avsys"}
	return p, kube
}

func deployed(t *testing.T, kube *kubefake.Clientset) corev1.Container {
	t.Helper()
	d, err := kube.AppsV1().Deployments("avsys").Get(context.Background(), "avsys", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d.Spec.Template.Spec.Containers[0]
}

func panelSays(t *testing.T, p *panel, who string) map[string]any {
	t.Helper()
	code, got := p.get(who, "GET", "/nodes", "")
	if code != 200 {
		t.Fatalf("nodes as %s: %d %v", who, code, got)
	}
	v, _ := got["panel"].(map[string]any)
	return v
}

// The instance administrator presses Update: the panel's own Deployment is
// moved to the newer release, and the panel says it is on its way there.
// Nobody else may, and only to the release the administrator was shown.
func TestUpdateFromThePanel(t *testing.T) {
	p, kube := selfPanel(t, "v0.1.0", "v0.2.0")

	if v := panelSays(t, p, "root"); v["version"] != "v0.1.0" || v["canUpdate"] != true || v["autoUpdate"] != false || v["updating"] != nil {
		t.Fatalf("before: %v", v)
	}
	for _, who := range []string{"owner", "admin", "viewer"} {
		if v := panelSays(t, p, who); v != nil {
			t.Errorf("%s is shown the panel: %v", who, v)
		}
		if code, _ := p.do(who, "POST", "/panel/update", `{"version":"v0.2.0"}`); code != 403 {
			t.Errorf("%s updating: %d, want 403", who, code)
		}
		if code, _ := p.do(who, "PATCH", "/panel", `{"autoUpdate":true}`); code != 403 {
			t.Errorf("%s setting updates: %d, want 403", who, code)
		}
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.1.0" {
		t.Fatalf("moved by someone who may not: %s", c.Image)
	}

	// Only to the release shown.
	if code, reason := p.do("root", "POST", "/panel/update", `{"version":"v0.3.0"}`); code != 409 || reason != "no_update" {
		t.Errorf("to a release that is not the newest: %d %s", code, reason)
	}

	code, got := p.get("root", "POST", "/panel/update", `{"version":"v0.2.0"}`)
	if code != 202 || got["updating"] != "v0.2.0" {
		t.Fatalf("updating: %d %v", code, got)
	}
	c := deployed(t, kube)
	if c.Image != "ghcr.io/gt-drehmal/avsys:v0.2.0" {
		t.Errorf("the panel's image: %s", c.Image)
	}
	want := []corev1.EnvVar{
		{Name: "AVSYS_PUBLIC_URL", Value: "https://panel.example.com"},
		{Name: "AVSYS_WORKSPACE_IMAGE", Value: "registry.lan/avsys-workspace:v0.2.0"},
	}
	if len(c.Env) != len(want) || c.Env[0] != want[0] || c.Env[1] != want[1] {
		t.Errorf("its settings: %v, want %v", c.Env, want)
	}
	if v := panelSays(t, p, "root"); v["updating"] != "v0.2.0" {
		t.Errorf("while the new version starts: %v", v)
	}

	// Pressed again, from another window: nothing more happens.
	patches := 0
	for _, a := range kube.Actions() {
		if a.GetVerb() == "patch" {
			patches++
		}
	}
	if code, _ := p.do("root", "POST", "/panel/update", `{"version":"v0.2.0"}`); code != 202 {
		t.Errorf("pressed twice: %d", code)
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() == "patch" {
			patches--
		}
	}
	if patches != 0 {
		t.Errorf("pressed twice: patched again")
	}
}

// A workspace image set by hand to something other than this version's is
// someone's choice, and is left as it is.
func TestUpdateLeavesAChosenWorkspaceImage(t *testing.T) {
	p, kube := selfPanel(t, "v0.1.0", "v0.2.0")
	ctx := context.Background()
	d, _ := kube.AppsV1().Deployments("avsys").Get(ctx, "avsys", metav1.GetOptions{})
	d.Spec.Template.Spec.Containers[0].Env[1].Value = "registry.lan/avsys-workspace:mine"
	if _, err := kube.AppsV1().Deployments("avsys").Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if code, _ := p.do("root", "POST", "/panel/update", `{}`); code != 202 {
		t.Fatalf("updating: %d", code)
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.2.0" || c.Env[1].Value != "registry.lan/avsys-workspace:mine" {
		t.Errorf("after: %s, %v", c.Image, c.Env)
	}
}

// Where the panel may not change its Deployment, or has none, or there is
// nothing newer, it does not offer to update itself, and refuses if asked.
func TestNoUpdateWhereThePanelCannot(t *testing.T) {
	forbidden := func() k8stesting.ReactionFunc {
		return func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "avsys", nil)
		}
	}

	t.Run("not allowed to read it", func(t *testing.T) {
		p, kube := selfPanel(t, "v0.1.0", "v0.2.0")
		kube.PrependReactor("get", "deployments", forbidden())
		if v := panelSays(t, p, "root"); v["canUpdate"] != false {
			t.Errorf("offered: %v", v)
		}
		if code, reason := p.do("root", "POST", "/panel/update", `{}`); code != 409 || reason != "cannot_update" {
			t.Errorf("asked anyway: %d %s", code, reason)
		}
	})
	t.Run("not allowed to change it", func(t *testing.T) {
		p, kube := selfPanel(t, "v0.1.0", "v0.2.0")
		kube.PrependReactor("patch", "deployments", forbidden())
		if code, reason := p.do("root", "POST", "/panel/update", `{}`); code != 409 || reason != "cannot_update" {
			t.Errorf("asked: %d %s", code, reason)
		}
	})
	t.Run("outside the cluster", func(t *testing.T) {
		p, _ := selfPanel(t, "v0.1.0", "v0.2.0")
		p.srv.Config.Deployment = types.NamespacedName{}
		if v := panelSays(t, p, "root"); v["canUpdate"] != false {
			t.Errorf("offered: %v", v)
		}
		if code, reason := p.do("root", "POST", "/panel/update", `{}`); code != 409 || reason != "cannot_update" {
			t.Errorf("asked anyway: %d %s", code, reason)
		}
	})
	t.Run("up to date", func(t *testing.T) {
		p, kube := selfPanel(t, "v0.2.0", "v0.2.0")
		if v := panelSays(t, p, "root"); v["canUpdate"] != true || v["updating"] != nil {
			t.Errorf("shown: %v", v)
		}
		if code, reason := p.do("root", "POST", "/panel/update", `{}`); code != 409 || reason != "no_update" {
			t.Errorf("asked anyway: %d %s", code, reason)
		}
		if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.2.0" {
			t.Errorf("moved: %s", c.Image)
		}
	})
	t.Run("a build of a commit", func(t *testing.T) {
		p, _ := selfPanel(t, "sha-1234567", "v0.2.0")
		if v := panelSays(t, p, "root"); v["canUpdate"] != false {
			t.Errorf("offered: %v", v)
		}
	})
}

// statusGame answers a server list ping with how many are on it, or does
// not answer at all.
type statusGame struct {
	online int
	down   bool
}

func (g *statusGame) DialPod(context.Context, string, string, int) (net.Conn, error) {
	if g.down {
		return nil, errors.New("not answering")
	}
	conn, game := net.Pipe()
	go func() {
		defer game.Close()
		r := bufio.NewReader(game)
		if _, err := mcproto.ReadHandshake(r); err != nil {
			return
		}
		_ = mcproto.ServeStatus(game, r, mcproto.Status{Players: mcproto.Players{Online: g.online, Max: 20}, Description: json.RawMessage(`"x"`)})
	}()
	return conn, nil
}

// Set to, the panel moves to a fix release by itself, and only while nobody
// is playing, as each running game says when asked then: a game that does
// not answer may have players, and what was last seen may be old. A release
// with more than fixes waits for the administrator.
func TestAutoUpdate(t *testing.T) {
	ctx := context.Background()
	p, kube := selfPanel(t, "v0.1.0", "v0.1.1")
	game := &statusGame{}
	p.srv.Dialer = game
	if p.srv.AutoUpdate(ctx) {
		t.Fatal("updated by itself without being set to")
	}
	code, got := p.get("root", "PATCH", "/panel", `{"autoUpdate":true}`)
	if code != 200 || got["autoUpdate"] != true {
		t.Fatalf("turning it on: %d %v", code, got)
	}
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	// The last look found nobody, but two are on it now.
	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	world.Status.Players = &avsysv1.PlayersStatus{Online: 0}
	if err := p.srv.Client.Status().Update(ctx, world); err != nil {
		t.Fatal(err)
	}
	game.online = 2
	if p.srv.AutoUpdate(ctx) {
		t.Error("updated by itself with people playing")
	}
	game.online, game.down = 0, true
	if p.srv.AutoUpdate(ctx) {
		t.Error("updated by itself with a game that does not say who is on it")
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.1.0" {
		t.Fatalf("moved: %s", c.Image)
	}
	// A stopped server has nobody on it, and is not asked.
	p.setPhase("alpha", "world", avsysv1.PhaseStopped)
	if !p.srv.AutoUpdate(ctx) {
		t.Error("did not update by itself with no game up")
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.1.1" {
		t.Errorf("after: %s", c.Image)
	}

	p, kube = selfPanel(t, "v0.1.0", "v0.1.1")
	p.srv.Dialer = &statusGame{}
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	if code, _ := p.do("root", "PATCH", "/panel", `{"autoUpdate":true}`); code != 200 {
		t.Fatal(code)
	}
	if !p.srv.AutoUpdate(ctx) {
		t.Error("did not update by itself with a running game nobody is on")
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.1.1" {
		t.Errorf("after: %s", c.Image)
	}

	p, kube = selfPanel(t, "v0.1.0", "v0.2.0")
	if code, _ := p.do("root", "PATCH", "/panel", `{"autoUpdate":true}`); code != 200 {
		t.Fatal(code)
	}
	if p.srv.AutoUpdate(ctx) {
		t.Error("moved to a new minor release by itself")
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.1.0" {
		t.Errorf("moved: %s", c.Image)
	}

	// Turned off again.
	p, kube = selfPanel(t, "v0.1.0", "v0.1.1")
	if code, _ := p.do("root", "PATCH", "/panel", `{"autoUpdate":true}`); code != 200 {
		t.Fatal(code)
	}
	if code, got := p.get("root", "PATCH", "/panel", `{"autoUpdate":false}`); code != 200 || got["autoUpdate"] != false {
		t.Fatalf("turning it off: %d %v", code, got)
	}
	if p.srv.AutoUpdate(ctx) {
		t.Error("updated by itself once turned off")
	}
	if c := deployed(t, kube); c.Image != "ghcr.io/gt-drehmal/avsys:v0.1.0" {
		t.Errorf("moved: %s", c.Image)
	}
	if code, _ := p.do("root", "PATCH", "/panel", `{}`); code != 400 {
		t.Errorf("saying nothing: %d", code)
	}
}
