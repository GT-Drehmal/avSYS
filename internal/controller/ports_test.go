package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func TestParsePortRange(t *testing.T) {
	if r, err := ParsePortRange("25600-25699"); err != nil || r != (PortRange{25600, 25699}) {
		t.Errorf("25600-25699: %v %v", r, err)
	}
	if r, err := ParsePortRange(""); err != nil || r.First != 0 {
		t.Errorf("empty: %v %v", r, err)
	}
	for _, bad := range []string{"25600", "25699-25600", "0-10", "a-b", "1-70000"} {
		if _, err := ParsePortRange(bad); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
}

func portsScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func withPorts(name string, expose avsysv1.ExposeType, ports ...avsysv1.ExtraPort) *avsysv1.MinecraftServer {
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: name, UID: types.UID("uid-" + name)}}
	mc.Spec.Expose = avsysv1.ExposeSpec{Type: expose, Hostname: name + ".example.org"}
	mc.Spec.Ports = ports
	return mc
}

var (
	voice  = avsysv1.ExtraPort{Name: "voice-chat", Port: 24454, Protocol: corev1.ProtocolUDP}
	bedrok = avsysv1.ExtraPort{Name: "geyser", Port: 19132, Protocol: corev1.ProtocolUDP}
	webmap = avsysv1.ExtraPort{Name: "map", Port: 8100}
)

// Servers sharing the router's address get public ports of their own from
// the range, none given twice, kept while they have the port, and a
// LoadBalancer Service that opens them.
func TestSharedAddressPorts(t *testing.T) {
	scheme := portsScheme(t)
	other := withPorts("other", avsysv1.ExposeRouter, voice)
	other.Annotations = map[string]string{avsysv1.AnnotationPublicPorts: "voice-chat=25600"}
	mc := withPorts("probe", avsysv1.ExposeRouter, voice, bedrok)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(other, mc).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}}}
	ports, _, err := r.ensurePorts(context.Background(), mc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 || ports[0].PublicPort != 25601 || ports[1].PublicPort != 25602 || ports[0].Address != "probe.example.org:25601" || ports[1].Protocol != corev1.ProtocolUDP {
		t.Fatalf("ports: %+v", ports)
	}
	svc := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}, svc); err != nil {
		t.Fatal(err)
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || len(svc.Spec.Ports) != 2 || svc.Spec.Ports[0].Port != 25601 || svc.Spec.Ports[0].TargetPort.IntVal != 24454 || svc.Spec.Ports[0].Protocol != corev1.ProtocolUDP {
		t.Errorf("service: %s %+v", svc.Spec.Type, svc.Spec.Ports)
	}

	// The voice port taken off: its public port is given back, the other kept.
	now := &avsysv1.MinecraftServer{}
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(mc), now)
	now.Spec.Ports = []avsysv1.ExtraPort{bedrok}
	if err := c.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if ports, _, err = r.ensurePorts(context.Background(), now, nil); err != nil || len(ports) != 1 || ports[0].PublicPort != 25602 {
		t.Errorf("after one is taken off: %+v %v", ports, err)
	}
	if got := now.Annotations[avsysv1.AnnotationPublicPorts]; got != "geyser=25602" {
		t.Errorf("recorded %q", got)
	}

	// None left: its Service goes.
	now.Spec.Ports = nil
	if err := c.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ensurePorts(context.Background(), now, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}, &corev1.Service{}); err == nil {
		t.Error("its ports' Service is still there with no ports")
	}
	if _, ok := now.Annotations[avsysv1.AnnotationPublicPorts]; ok {
		t.Error("its public ports are still recorded")
	}
}

// With no range, each extra port of a server sharing the address is a node
// port the cluster gives.
func TestSharedAddressPortsWithoutRange(t *testing.T) {
	scheme := portsScheme(t)
	mc := withPorts("probe", avsysv1.ExposeRouter, voice)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme}
	if _, _, err := r.ensurePorts(context.Background(), mc, nil); err != nil {
		t.Fatal(err)
	}
	svc := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}, svc); err != nil {
		t.Fatal(err)
	}
	if svc.Spec.Type != corev1.ServiceTypeNodePort || svc.Spec.Ports[0].Port != 24454 {
		t.Errorf("service: %s %+v", svc.Spec.Type, svc.Spec.Ports)
	}
	if _, ok := mc.Annotations[avsysv1.AnnotationPublicPorts]; ok {
		t.Error("public ports given out with no range")
	}
}

// A server with an address of its own has its extra ports beside the game's
// on its own Service, at their own numbers.
func TestOwnAddressPorts(t *testing.T) {
	mc := withPorts("probe", avsysv1.ExposeLoadBalancer, voice, webmap)
	mc.Spec.Expose.Hostname = ""
	svc := &corev1.Service{}
	applyService(svc, mc)
	if len(svc.Spec.Ports) != 3 || svc.Spec.Ports[0].Name != "minecraft" || svc.Spec.Ports[1].Port != 24454 || svc.Spec.Ports[1].Protocol != corev1.ProtocolUDP || svc.Spec.Ports[2].Protocol != corev1.ProtocolTCP {
		t.Fatalf("ports: %+v", svc.Spec.Ports)
	}
	// The node ports the cluster gives a load balancer's ports are kept, or
	// each look would give them up for new ones.
	for i := range svc.Spec.Ports {
		svc.Spec.Ports[i].NodePort = int32(30000 + i)
	}
	applyService(svc, mc)
	for i, p := range svc.Spec.Ports {
		if p.NodePort != int32(30000+i) {
			t.Errorf("%s: node port %d, want %d kept", p.Name, p.NodePort, 30000+i)
		}
	}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.7"}}
	st := portStatus(mc, svc, nil, Config{})
	if st[0].PublicPort != 24454 || st[0].Address != "203.0.113.7:24454" || st[1].Address != "203.0.113.7:8100" {
		t.Errorf("status: %+v", st)
	}
	// Not exposed at all: none.
	none := withPorts("hidden", avsysv1.ExposeNone, voice)
	svc = &corev1.Service{}
	applyService(svc, none)
	if len(svc.Spec.Ports) != 1 {
		t.Errorf("a server not exposed opened %+v", svc.Spec.Ports)
	}
}

// Where the load balancer gives a server's ports an address of their own,
// not the router's, that address is shown; on the router's, the hostname.
func TestPortsOwnAddress(t *testing.T) {
	scheme := portsScheme(t)
	router := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "avsys", Name: "avsys"}}
	router.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.1"}}
	mc := withPorts("probe", avsysv1.ExposeRouter, voice)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(router, mc).WithStatusSubresource(&corev1.Service{}).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}, RouterService: types.NamespacedName{Namespace: "avsys", Name: "avsys"}, PortsServiceAnnotations: map[string]string{"metallb.universe.tf/allow-shared-ip": "avsys"}}}
	if _, _, err := r.ensurePorts(context.Background(), mc, nil); err != nil {
		t.Fatal(err)
	}
	own := &corev1.Service{}
	key := client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}
	if err := c.Get(context.Background(), key, own); err != nil {
		t.Fatal(err)
	}
	if own.Annotations["metallb.universe.tf/allow-shared-ip"] != "avsys" {
		t.Errorf("annotations: %v", own.Annotations)
	}
	for ip, want := range map[string]string{"203.0.113.1": "probe.example.org:25600", "203.0.113.9": "203.0.113.9:25600"} {
		own.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip}}
		if err := c.Status().Update(context.Background(), own); err != nil {
			t.Fatal(err)
		}
		ports, _, err := r.ensurePorts(context.Background(), mc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ports[0].Address != want {
			t.Errorf("given %s: shown %s, want %s", ip, ports[0].Address, want)
		}
	}
}

// A panel that does not know the router's Service, as one run outside the
// cluster, shows the address the load balancer gave the ports.
func TestPortsOwnAddressWithoutRouter(t *testing.T) {
	scheme := portsScheme(t)
	mc := withPorts("probe", avsysv1.ExposeRouter, voice)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc).WithStatusSubresource(&corev1.Service{}).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}}}
	if _, _, err := r.ensurePorts(context.Background(), mc, nil); err != nil {
		t.Fatal(err)
	}
	own := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}, own); err != nil {
		t.Fatal(err)
	}
	ports, _, err := r.ensurePorts(context.Background(), mc, nil)
	if err != nil || ports[0].Address != "probe.example.org:25600" {
		t.Errorf("with no address given yet: %v %+v, want the hostname", err, ports)
	}
	own.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.9"}}
	if err := c.Status().Update(context.Background(), own); err != nil {
		t.Fatal(err)
	}
	if ports, _, err = r.ensurePorts(context.Background(), mc, nil); err != nil || ports[0].Address != "203.0.113.9:25600" {
		t.Errorf("given one: %v %+v, want 203.0.113.9:25600", err, ports)
	}
}

// With every public port given out, a server still runs: the ports left
// over are not opened, say why, and are looked at again.
func TestPortRangeFull(t *testing.T) {
	scheme := portsScheme(t)
	other := withPorts("other", avsysv1.ExposeRouter, voice)
	other.Annotations = map[string]string{avsysv1.AnnotationPublicPorts: "voice-chat=25600"}
	mc := withPorts("probe", avsysv1.ExposeRouter, voice, bedrok)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(other, mc).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25601}}}
	ports, retry, err := r.ensurePorts(context.Background(), mc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !retry || len(ports) != 2 || ports[0].PublicPort != 25601 || ports[0].Message != "" || ports[1].PublicPort != 0 || !strings.Contains(ports[1].Message, "every public port from 25600 to 25601") {
		t.Fatalf("retry %v, ports %+v", retry, ports)
	}
	svc := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("probe")}, svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Name != "voice-chat" {
		t.Errorf("service ports: %+v", svc.Spec.Ports)
	}
	// None free at all: no Service, which cannot have no ports, and the
	// server is not held up.
	lone := withPorts("lone", avsysv1.ExposeRouter, webmap)
	if err := c.Create(context.Background(), lone); err != nil {
		t.Fatal(err)
	}
	ports, retry, err = r.ensurePorts(context.Background(), lone, nil)
	if err != nil || !retry || len(ports) != 1 || ports[0].Message == "" {
		t.Fatalf("lone: %v %v %+v", err, retry, ports)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: PortsServiceName("lone")}, &corev1.Service{}); err == nil {
		t.Error("a Service with no ports was made")
	}
}

// A server named as another's Service of extra ports has its own Service by
// that name: it is never changed or removed for the other's ports.
func TestPortsServiceOfAnotherServer(t *testing.T) {
	scheme := portsScheme(t)
	mc := withPorts("foo", avsysv1.ExposeRouter, voice)
	// Given before the name was taken.
	mc.Annotations = map[string]string{avsysv1.AnnotationPublicPorts: "voice-chat=25600"}
	named := withPorts("foo-ports", avsysv1.ExposeRouter)
	theirs := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "foo-ports"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Name: "minecraft", Port: 25565}}}}
	if err := controllerutil.SetControllerReference(named, theirs, scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, named, theirs).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}}}
	ports, retry, err := r.ensurePorts(context.Background(), mc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || !strings.Contains(ports[0].Message, "another server's") {
		t.Errorf("ports: %+v", ports)
	}
	// No public port is held for them meanwhile.
	if got := mc.Annotations[avsysv1.AnnotationPublicPorts]; got != "" {
		t.Errorf("public ports held: %q", got)
	}
	// They are looked at again: that Service going away says nothing to
	// this server, whose ports are then opened.
	if !retry {
		t.Error("ports kept closed by another server's Service are not looked at again")
	}
	// Taking the ports away does not remove it either.
	mc.Spec.Ports = nil
	if _, _, err := r.ensurePorts(context.Background(), mc, nil); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Service{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "mc-x", Name: "foo-ports"}, got); err != nil {
		t.Fatalf("the other server's Service: %v", err)
	}
	if got.Spec.Type != corev1.ServiceTypeClusterIP || len(got.Spec.Ports) != 1 || got.Spec.Ports[0].Name != "minecraft" {
		t.Errorf("the other server's Service was changed: %s %+v", got.Spec.Type, got.Spec.Ports)
	}

	// Once that Service is gone, as with its server, the ports are opened.
	mc.Spec.Ports = []avsysv1.ExtraPort{voice}
	if err := c.Delete(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	ports, _, err = r.ensurePorts(context.Background(), mc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].Message != "" || ports[0].PublicPort == 0 {
		t.Errorf("once that Service is gone: %+v", ports)
	}
}

// The router's own ports on the address shared are never given out: the
// game's, and any other it has.
func TestRouterPortsAreNotGivenOut(t *testing.T) {
	scheme := portsScheme(t)
	router := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "avsys", Name: "avsys"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "minecraft", Port: 25565}, {Name: "http", Port: 25600}, {Name: "ssh", Port: 25601}}}}
	mc := withPorts("probe", avsysv1.ExposeRouter, voice)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(router, mc).Build()
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme, Config: Config{PortRange: PortRange{25600, 25699}, RouterService: types.NamespacedName{Namespace: "avsys", Name: "avsys"}}}
	ports, _, err := r.ensurePorts(context.Background(), mc, nil)
	if err != nil || len(ports) != 1 || ports[0].PublicPort != 25602 {
		t.Errorf("%v %+v, want public port 25602", err, ports)
	}
}
