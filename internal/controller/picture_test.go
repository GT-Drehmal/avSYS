package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A server's picture changed while its game was up waits for that game to
// start again, which reads it only as it starts: a restart in place, the
// game starting again in its container, is enough, as is a new pod. A
// server whose game is not up, stopped or asleep or still loading, has
// nothing waiting: it reads the new one as it starts. Neither is a change
// to its settings: the pod is not replaced for it.
func TestPictureChangedWaitsForTheGameToStartAgain(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	game := func(ready bool, restarts int32) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: MinecraftContainer, Ready: ready, RestartCount: restarts, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	}
	changedUnder := func(game string) func(*avsysv1.MinecraftServer) {
		return func(mc *avsysv1.MinecraftServer) {
			mc.Annotations = map[string]string{avsysv1.AnnotationPictureChanged: game}
		}
	}
	for _, c := range []struct {
		name    string
		server  func(*avsysv1.MinecraftServer)
		pod     string
		game    corev1.ContainerStatus
		waiting bool
	}{
		{"changed under the game that is up", changedUnder("pod-1/0"), "pod-1", game(true, 0), true},
		{"the game restarted in its container since", changedUnder("pod-1/0"), "pod-1", game(true, 1), false},
		{"a new pod since", changedUnder("pod-1/0"), "pod-2", game(true, 0), false},
		{"never changed", nil, "pod-1", game(true, 0), false},
		{"the game is loading", changedUnder("pod-1/0"), "pod-1", game(false, 0), false},
		{"stopped", func(mc *avsysv1.MinecraftServer) {
			changedUnder("pod-1/0")(mc)
			mc.Spec.State = avsysv1.StateStopped
		}, "pod-1", game(true, 0), false},
	} {
		mc := server(c.server)
		pod := buildPod(server(nil), Config{})
		pod.UID = "pod-1"
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{c.game}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&avsysv1.MinecraftServer{}).WithObjects(mc, pod).Build()
		r := &Reconciler{Client: cl, Reader: cl, Scheme: scheme}
		ctx := context.Background()
		got, pending, _, err := r.ensurePod(ctx, mc)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.server != nil && c.name != "stopped" && (!got.DeletionTimestamp.IsZero() || pending) {
			t.Errorf("%s: the pod was replaced (%v) or said to wait for new settings (%v)", c.name, !got.DeletionTimestamp.IsZero(), pending)
		}
		// The pod as the cluster has it now: another one, where the game
		// was given a new one.
		got.UID = "pod-1"
		if c.pod != "pod-1" {
			got.UID = "pod-2"
		}
		if err := r.updateStatus(ctx, mc, got, &corev1.Service{}, nil, pending); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		now := &avsysv1.MinecraftServer{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(mc), now); err != nil {
			t.Fatal(err)
		}
		if now.Status.PendingPicture != c.waiting {
			t.Errorf("%s: the picture waits: %v, want %v", c.name, now.Status.PendingPicture, c.waiting)
		}
		if now.Status.PendingRestart {
			t.Errorf("%s: said to wait for new settings", c.name)
		}
	}
}

// The game a pod runs is named by the pod and how often the game has
// started again in it, and only while it is up.
func TestGameUp(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1"}}
	if got := GameUp(pod); got != "" {
		t.Errorf("no game: %q", got)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: MinecraftContainer, Ready: true, RestartCount: 3}}
	if got := GameUp(pod); got != "pod-1/3" {
		t.Errorf("up: %q", got)
	}
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	if got := GameUp(pod); got != "" {
		t.Errorf("going away: %q", got)
	}
	if got := GameUp(nil); got != "" {
		t.Errorf("no pod: %q", got)
	}
}
