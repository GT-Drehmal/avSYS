package controller

import (
	"context"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// settle does to a Service's endpoints what the cluster's own controllers
// do, as a k3s cluster was seen to:
//
//   - With a selector, the Endpoints of the Service, and the endpoints the
//     cluster makes of it, are of the ready pods it selects; endpoints
//     copied from the Endpoints are taken away.
//   - Without one, the Endpoints and endpoints made while it had one are
//     left as they were, and the Endpoints are copied into endpoints of the
//     Service.
func settle(t *testing.T, c client.Client, key client.ObjectKey) {
	t.Helper()
	ctx := context.Background()
	svc := &corev1.Service{}
	if err := c.Get(ctx, key, svc); err != nil {
		t.Fatal(err)
	}
	endpoints := &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	made := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name + "-made"}}
	mirrored := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name + "-mirrored"}}
	put := func(obj client.Object) {
		t.Helper()
		obj.SetResourceVersion("")
		err := c.Create(ctx, obj)
		if apierrors.IsAlreadyExists(err) {
			had := obj.DeepCopyObject().(client.Object)
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), had); err != nil {
				t.Fatal(err)
			}
			obj.SetResourceVersion(had.GetResourceVersion())
			err = c.Update(ctx, obj)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	sliceOf := func(s *discoveryv1.EndpointSlice, manager string, ips []string) {
		ready := true
		s.Labels = map[string]string{discoveryv1.LabelServiceName: svc.Name, discoveryv1.LabelManagedBy: manager}
		s.AddressType = discoveryv1.AddressTypeIPv4
		s.Endpoints = nil
		for _, ip := range ips {
			s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{Addresses: []string{ip}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}})
		}
	}
	if svc.Spec.Selector != nil {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(key.Namespace), client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(svc.Spec.Selector)}); err != nil {
			t.Fatal(err)
		}
		var ips []string
		for _, p := range pods.Items {
			if p.Status.PodIP != "" {
				ips = append(ips, p.Status.PodIP)
			}
		}
		endpoints.Labels = map[string]string{"endpoints.kubernetes.io/managed-by": "endpoint-controller"}
		endpoints.Subsets = nil
		if len(ips) > 0 {
			subset := corev1.EndpointSubset{}
			for _, ip := range ips {
				subset.Addresses = append(subset.Addresses, corev1.EndpointAddress{IP: ip})
			}
			endpoints.Subsets = []corev1.EndpointSubset{subset}
		}
		put(endpoints)
		sliceOf(made, "endpointslice-controller.k8s.io", ips)
		put(made)
		if err := c.Delete(ctx, mirrored); client.IgnoreNotFound(err) != nil {
			t.Fatal(err)
		}
		return
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(endpoints), endpoints); apierrors.IsNotFound(err) {
		return
	} else if err != nil {
		t.Fatal(err)
	}
	var ips []string
	for _, subset := range endpoints.Subsets {
		for _, a := range subset.Addresses {
			ips = append(ips, a.IP)
		}
	}
	sliceOf(mirrored, "endpointslicemirroring-controller.k8s.io", ips)
	put(mirrored)
}

// reached is where traffic to a Service is sent: every ready address of
// every one of its endpoints, whoever made them.
func reached(t *testing.T, c client.Client, svc string) []string {
	t.Helper()
	var slices discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &slices, client.InNamespace("mc-x"), client.MatchingLabels{discoveryv1.LabelServiceName: svc}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready != nil && *e.Conditions.Ready && !seen[e.Addresses[0]] {
				seen[e.Addresses[0]] = true
				out = append(out, e.Addresses[0])
			}
		}
	}
	sort.Strings(out)
	return out
}

// Put to sleep while its game is still ready, a server's extra ports reach
// the panel alone, not the panel and its game's pod by turns: a Bedrock
// player joining was sent to where its game had been half the time, and
// never woke it.
func TestExtraPortsReachOnlyThePanelOnceAsleep(t *testing.T) {
	scheme := portsScheme(t)
	mc := withPorts("probe", avsysv1.ExposeRouter, bedrok, webmap)
	game := podOf(mc)
	game.Status.PodIP = "10.42.1.10"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, game).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}, StandIn: &panel{ip: "10.42.0.7"}}}
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}
	ensure := func(what string) {
		t.Helper()
		now := &avsysv1.MinecraftServer{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(mc), now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.ensurePorts(ctx, now, nil); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		settle(t, c, key)
	}

	ensure("running")
	if got := reached(t, c, key.Name); len(got) != 1 || got[0] != "10.42.1.10" {
		t.Fatalf("running, its ports reach %v, not its pod", got)
	}

	// Asleep: its game is told to stop, and is still ready a while.
	sleeping := &avsysv1.MinecraftServer{}
	_ = c.Get(ctx, client.ObjectKeyFromObject(mc), sleeping)
	asleep(sleeping)
	if err := c.Update(ctx, sleeping); err != nil {
		t.Fatal(err)
	}
	ensure("its game stopping")
	if got := reached(t, c, key.Name); len(got) != 1 || got[0] != "10.42.0.7" {
		t.Errorf("its game stopping, its ports reach %v, not the panel alone", got)
	}

	// The game's pod gone, the one with only the workspace in its place.
	if err := c.Delete(ctx, game); err != nil {
		t.Fatal(err)
	}
	workspace := podOf(sleeping)
	workspace.Status.PodIP = "10.42.1.12"
	if err := c.Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	ensure("asleep")
	if got := reached(t, c, key.Name); len(got) != 1 || got[0] != "10.42.0.7" {
		t.Errorf("asleep, its ports reach %v, not the panel alone", got)
	}

	// Woken and running again, its game's pod alone.
	running := &avsysv1.MinecraftServer{}
	_ = c.Get(ctx, client.ObjectKeyFromObject(mc), running)
	running.Annotations = nil
	if err := c.Update(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	back := podOf(running)
	back.Status.PodIP = "10.42.1.14"
	if err := c.Create(ctx, back); err != nil {
		t.Fatal(err)
	}
	ensure("running again")
	if got := reached(t, c, key.Name); len(got) != 1 || got[0] != "10.42.1.14" {
		t.Errorf("running again, its ports reach %v, not its pod", got)
	}
}
