package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// The image downloads what Modrinth content needs unless the panel has
// listed it: then it is told to download nothing more. A modpack's mods are
// the pack's, and a server's own setting wins over either.
func TestDependencyEnv(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*avsysv1.MinecraftServer)
		want   string
	}{
		{"made before the panel listed them", nil, "required"},
		{"listed", func(mc *avsysv1.MinecraftServer) { mc.Spec.DependenciesListed = true }, "none"},
		{"a modpack", func(mc *avsysv1.MinecraftServer) {
			mc.Spec.DependenciesListed = true
			mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "pack"}
		}, "required"},
		{"its own setting", func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Env = []corev1.EnvVar{{Name: DependencyEnv, Value: "optional"}}
		}, "optional"},
	} {
		var last string
		for _, e := range minecraftEnv(server(c.change), Config{}) {
			if e.Name == DependencyEnv {
				last = e.Value
			}
		}
		if last != c.want {
			t.Errorf("%s: %q, want %q", c.name, last, c.want)
		}
	}
}

// A pod noted as running what its server's settings say is marked as made
// with them: a change after that leaves the pod as it is, as one to the
// panel's own notes on its content, is then no change to wait for a restart
// for, though the note is for the settings before. A change to the pod is.
func TestSettledPodIsMarked(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ready := corev1.ContainerStatus{Name: MinecraftContainer, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now().Add(-time.Hour))}}}
	kins := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "kinswathe", Version: "k1"}
	// Made before the panel listed what content needs.
	pod := buildPod(server(func(mc *avsysv1.MinecraftServer) { mc.Spec.Mods = []avsysv1.ContentRef{kins} }), Config{})
	pod.UID = "pod-1"
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{ready}
	// Brought over: listed, with what the image had downloaded for it.
	mc := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.DependenciesListed = true
		mc.Spec.Mods = []avsysv1.ContentRef{kins, {Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "FAPI", Version: "f1", Dependency: true, NeededBy: []string{"kinswathe"}}}
		mc.Generation = 4
		mc.Annotations = map[string]string{avsysv1.AnnotationSettled: "pod-1@4"}
	})
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, pod).Build()
	r := &Reconciler{Client: cl, Reader: cl, Scheme: scheme}
	ensure := func(what string, want bool) {
		t.Helper()
		got, pending, _, err := r.ensurePod(context.Background(), mc)
		if err != nil {
			t.Fatal(err)
		}
		if !got.DeletionTimestamp.IsZero() {
			t.Fatalf("%s: replaced", what)
		}
		if pending != want {
			t.Errorf("%s: pending %v, want %v", what, pending, want)
		}
	}
	ensure("settled", false)
	now := &corev1.Pod{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), now); err != nil {
		t.Fatal(err)
	}
	if now.Annotations[annotationSpecHash] != buildPod(mc, Config{}).Annotations[annotationSpecHash] {
		t.Error("the pod is not marked as made with the settings it runs")
	}
	// A note of the panel's changes: the next generation, the same pod.
	mc.Spec.Mods[1].NeededBy = []string{"kinswathe", "wathe_blood"}
	mc.Generation = 5
	ensure("a note changed", false)
	mc.Spec.Properties = map[string]string{"motd": "changed"}
	mc.Generation = 6
	ensure("a setting changed", true)
}
