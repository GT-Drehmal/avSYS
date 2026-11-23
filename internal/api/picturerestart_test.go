package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// pictureFiles gives server "world" of alpha files kept in memory, with a
// /data folder, in place of its workspace.
func pictureFiles(t *testing.T, p *panel) {
	t.Helper()
	sc := memFiles(t)
	if err := sc.Mkdir(dataRoot); err != nil {
		t.Fatal(err)
	}
	used := &atomic.Int64{}
	used.Store(time.Now().UnixNano())
	p.srv.workspaces.mu.Lock()
	p.srv.workspaces.conns[p.srv.Namespace("alpha")+"/world"] = &pooledSFTP{sftp: sc, lastUsed: used}
	p.srv.workspaces.mu.Unlock()
}

// A running game reads its picture only as it starts. A picture set or
// taken off while it is up is noted against that game, which is what the
// server then says waits for a restart; once that game has started again,
// in place or in a new pod, nothing does. A server whose game is not up
// reads the new one as it starts, and nothing is noted.
func TestAChangedPictureWaitsForARestart(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	pictureFiles(t, p)
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: "pod-1"}}
	setGame := func(ready bool, restarts int32) {
		t.Helper()
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: controller.MinecraftContainer, Ready: ready, RestartCount: restarts}}
		if err := p.srv.Client.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.srv.Client.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	noted := func() string {
		t.Helper()
		return p.server("world").Annotations[avsysv1.AnnotationPictureChanged]
	}
	setState := func(state avsysv1.DesiredState) {
		t.Helper()
		mc := p.server("world")
		mc.Spec.State = state
		if err := p.srv.Client.Update(ctx, mc); err != nil {
			t.Fatal(err)
		}
	}
	change := func(how string) {
		t.Helper()
		body := []byte(nil)
		method := "DELETE"
		if how == "set" {
			body, method = asPNG(t, picture(64, 64)), "PUT"
		}
		if rec := p.send("admin", method, "/orgs/alpha/servers/world/icon", body); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", how, rec.Code, rec.Body)
		}
	}

	// Stopped: nothing waits.
	setState(avsysv1.StateStopped)
	setGame(true, 0)
	change("set")
	if got := noted(); got != "" {
		t.Errorf("set on a stopped server: noted %q", got)
	}
	// Starting, its game still loading: it reads the new one once loaded.
	setState(avsysv1.StateRunning)
	setGame(false, 0)
	change("set")
	if got := noted(); got != "" {
		t.Errorf("set while the game loads: noted %q", got)
	}

	// Up: set under this game.
	setGame(true, 0)
	change("set")
	if got := noted(); got != "pod-1/0" {
		t.Fatalf("set while up: noted %q, want pod-1/0", got)
	}
	// What the controller makes of that, and how the server is shown.
	mc := p.server("world")
	mc.Status.Phase, mc.Status.PendingPicture, mc.Status.ObservedGeneration = avsysv1.PhaseRunning, true, mc.Generation
	if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
		t.Fatal(err)
	}
	_, shown := p.get("viewer", "GET", "/orgs/alpha/servers/world", "")
	if status, _ := shown["status"].(map[string]any); status["pendingRestart"] != true || status["pendingPicture"] != true {
		t.Errorf("shown as %v, want it to wait for a restart", shown["status"])
	}
	// A restart in place is enough for it.
	if !restartsInPlace(p.server("world")) {
		t.Error("a restart for a new picture is not made in place")
	}

	// Restarted in its container, then taken off: under that game.
	setGame(true, 1)
	change("remove")
	if got := noted(); got != "pod-1/1" {
		t.Errorf("taken off after a restart in place: noted %q, want pod-1/1", got)
	}
	// Asleep, its pod not yet told: nothing new is noted.
	asleep := p.server("world")
	asleep.Annotations[avsysv1.AnnotationAsleep] = "true"
	if err := p.srv.Client.Update(ctx, asleep); err != nil {
		t.Fatal(err)
	}
	setGame(true, 2)
	change("set")
	if got := noted(); got != "pod-1/1" {
		t.Errorf("set while asleep: noted %q, want what was noted before", got)
	}
}
