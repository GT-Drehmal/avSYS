package controller

import (
	"context"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// panel stands in for the panel's listeners: each port of each server it
// is asked for is given a port of its own.
type panel struct {
	ip   string
	next int32
	open map[types.NamespacedName]map[string]int32
}

func (p *panel) Open(server types.NamespacedName, ports []avsysv1.ExtraPort) (string, map[string]int32, error) {
	if p.open == nil {
		p.open = map[types.NamespacedName]map[string]int32{}
	}
	had := p.open[server]
	at := map[string]int32{}
	for _, port := range ports {
		if n, ok := had[port.Name]; ok {
			at[port.Name] = n
			continue
		}
		p.next++
		at[port.Name] = 40000 + p.next
	}
	p.open[server] = at
	return p.ip, at, nil
}

func (p *panel) Close(server types.NamespacedName) { delete(p.open, server) }

func asleep(mc *avsysv1.MinecraftServer) *avsysv1.MinecraftServer {
	mc.Annotations = map[string]string{avsysv1.AnnotationAsleep: "2026-11-14T00:00:00Z"}
	return mc
}

// podOf is a server's pod as it is made, its game ready if it has one.
func podOf(mc *avsysv1.MinecraftServer) *corev1.Pod {
	pod := buildPod(mc, Config{})
	if hasContainer(pod, MinecraftContainer) {
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: MinecraftContainer, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	}
	return pod
}

func portsService(t *testing.T, c client.Client, server string) *corev1.Service {
	t.Helper()
	svc := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName(server)}, svc); err != nil {
		t.Fatal(err)
	}
	return svc
}

// slice is a server's endpoints at the panel, or nil for none.
func slice(t *testing.T, c client.Client, server string) *discoveryv1.EndpointSlice {
	t.Helper()
	s := &discoveryv1.EndpointSlice{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: StandInSliceName(server)}, s); err != nil {
		if client.IgnoreNotFound(err) != nil {
			t.Fatal(err)
		}
		return nil
	}
	return s
}

func slicePorts(s *discoveryv1.EndpointSlice) []string {
	var out []string
	for _, p := range s.Ports {
		out = append(out, *p.Name+"/"+string(*p.Protocol))
	}
	sort.Strings(out)
	return out
}

// While a server sleeps, its extra ports reach the panel, which answers in
// the game's place; once its game runs they reach its pod again. Each of
// several servers asleep at once has its own.
func TestExtraPortsReachThePanelWhileAsleep(t *testing.T) {
	scheme := portsScheme(t)
	probe := asleep(withPorts("probe", avsysv1.ExposeRouter, bedrok, webmap))
	other := asleep(withPorts("other", avsysv1.ExposeRouter, voice))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(probe, other, podOf(probe), podOf(other)).Build()
	stand := &panel{ip: "10.42.0.7"}
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}, StandIn: stand}}
	ctx := context.Background()
	for _, mc := range []*avsysv1.MinecraftServer{probe, other} {
		if _, _, err := r.ensurePorts(ctx, mc, nil); err != nil {
			t.Fatal(err)
		}
	}

	svc := portsService(t, c, "probe")
	if svc.Spec.Selector != nil {
		t.Errorf("asleep, its ports select its pod: %v", svc.Spec.Selector)
	}
	s := slice(t, c, "probe")
	if s == nil {
		t.Fatal("asleep, its ports reach nothing")
	}
	at := stand.open[client.ObjectKeyFromObject(probe)]
	if s.AddressType != discoveryv1.AddressTypeIPv4 || len(s.Endpoints) != 1 || s.Endpoints[0].Addresses[0] != "10.42.0.7" || !*s.Endpoints[0].Conditions.Ready {
		t.Errorf("endpoints: %s %+v", s.AddressType, s.Endpoints)
	}
	if got := slicePorts(s); len(got) != 2 || got[0] != "geyser/UDP" || got[1] != "map/TCP" {
		t.Errorf("ports at the panel: %v", got)
	}
	for _, p := range s.Ports {
		if *p.Port != at[*p.Name] {
			t.Errorf("%s reaches the panel on %d, which listens on %d", *p.Name, *p.Port, at[*p.Name])
		}
	}
	if s.Labels[discoveryv1.LabelServiceName] != svc.Name || s.Labels[discoveryv1.LabelManagedBy] == "endpointslice-controller.k8s.io" {
		t.Errorf("labels: %v", s.Labels)
	}
	if owner := metav1.GetControllerOf(s); owner == nil || owner.Kind != "Service" || owner.Name != svc.Name {
		t.Errorf("owned by %+v, not its Service", owner)
	}
	if o := slice(t, c, "other"); o == nil || len(o.Ports) != 1 || *o.Ports[0].Port == at["geyser"] || *o.Ports[0].Port == at["map"] {
		t.Errorf("the other server's: %+v", o)
	}

	// Woken: its game starting, the panel still answers.
	woken := &avsysv1.MinecraftServer{}
	_ = c.Get(ctx, client.ObjectKeyFromObject(probe), woken)
	woken.Annotations = nil
	if err := c.Update(ctx, woken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ensurePorts(ctx, woken, nil); err != nil {
		t.Fatal(err)
	}
	if portsService(t, c, "probe").Spec.Selector != nil || slice(t, c, "probe") == nil {
		t.Error("starting, its ports no longer reach the panel")
	}

	// Its game running, they reach its pod again, and the panel lets go.
	running := podOf(woken)
	if err := c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "probe"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, running); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ensurePorts(ctx, woken, nil); err != nil {
		t.Fatal(err)
	}
	if got := portsService(t, c, "probe").Spec.Selector; got[avsysv1.LabelServer] != "probe" {
		t.Errorf("running, its ports select %v", got)
	}
	if slice(t, c, "probe") != nil {
		t.Error("running, its ports still reach the panel too")
	}
	if _, ok := stand.open[client.ObjectKeyFromObject(probe)]; ok {
		t.Error("the panel still listens for it")
	}

	// The other sleeps on, as it was.
	if portsService(t, c, "other").Spec.Selector != nil || slice(t, c, "other") == nil {
		t.Error("the other server's ports no longer reach the panel")
	}
	if _, ok := stand.open[client.ObjectKeyFromObject(other)]; !ok {
		t.Error("the panel stopped listening for the other server")
	}

	// Put to sleep again: as soon as its game is told to stop. The
	// endpoints the cluster made of its game's pod, which it does not take
	// away itself, go; another Service's stay.
	clusterMade := func(name, service string) *discoveryv1.EndpointSlice {
		return &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: name, Labels: map[string]string{
				discoveryv1.LabelServiceName: service, discoveryv1.LabelManagedBy: "endpointslice-controller.k8s.io",
			}},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.42.1.20"}}},
		}
	}
	gamePods, others := clusterMade("probe-ports-abcde", PortsServiceName("probe")), clusterMade("probe-xyz", "probe")
	for _, s := range []*discoveryv1.EndpointSlice{gamePods, others} {
		if err := c.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	again := &avsysv1.MinecraftServer{}
	_ = c.Get(ctx, client.ObjectKeyFromObject(probe), again)
	asleep(again)
	if err := c.Update(ctx, again); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ensurePorts(ctx, again, nil); err != nil {
		t.Fatal(err)
	}
	if portsService(t, c, "probe").Spec.Selector != nil || slice(t, c, "probe") == nil {
		t.Error("its game stopping, its ports do not reach the panel")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(gamePods), &discoveryv1.EndpointSlice{}); err == nil {
		t.Error("the endpoints of its game's pod are still there beside the panel's")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(others), &discoveryv1.EndpointSlice{}); err != nil {
		t.Errorf("another Service's endpoints were taken: %v", err)
	}

	// Its ports taken off, the panel lets go of them.
	again.Spec.Ports = nil
	if err := c.Update(ctx, again); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ensurePorts(ctx, again, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := stand.open[client.ObjectKeyFromObject(probe)]; ok {
		t.Error("with no ports, the panel still listens for it")
	}

	// Deleted, the same.
	if err := c.Delete(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(other)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stand.open[client.ObjectKeyFromObject(other)]; ok {
		t.Error("deleted, the panel still listens for it")
	}
}

// A stopped server's ports reach the panel too, which says it is off, and
// the address the panel has is taken as it is, of either kind.
func TestExtraPortsOfAStoppedServer(t *testing.T) {
	scheme := portsScheme(t)
	mc := withPorts("probe", avsysv1.ExposeRouter, bedrok)
	mc.Spec.State = avsysv1.StateStopped
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, podOf(mc)).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{StandIn: &panel{ip: "fd00::7"}}}
	if _, _, err := r.ensurePorts(context.Background(), mc, nil); err != nil {
		t.Fatal(err)
	}
	if s := slice(t, c, "probe"); s == nil || s.AddressType != discoveryv1.AddressTypeIPv6 || s.Endpoints[0].Addresses[0] != "fd00::7" {
		t.Errorf("endpoints: %+v", s)
	}
}

// Without the panel to answer, as when it runs outside the cluster, a
// sleeping server's ports select its pod as before.
func TestExtraPortsWithoutAStandIn(t *testing.T) {
	scheme := portsScheme(t)
	mc := asleep(withPorts("probe", avsysv1.ExposeRouter, bedrok))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, podOf(mc)).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme}
	if _, _, err := r.ensurePorts(context.Background(), mc, nil); err != nil {
		t.Fatal(err)
	}
	if got := portsService(t, c, "probe").Spec.Selector; got[avsysv1.LabelServer] != "probe" {
		t.Errorf("selector: %v", got)
	}
	if slice(t, c, "probe") != nil {
		t.Error("endpoints made with nothing to answer on them")
	}
}
