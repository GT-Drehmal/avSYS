package api

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

func TestCheckPorts(t *testing.T) {
	ok := []avsysv1.ExtraPort{{Name: "voice-chat", Port: 24454, Protocol: corev1.ProtocolUDP}, {Name: "map", Port: 8100}, {Name: "map-udp", Port: 8100, Protocol: corev1.ProtocolUDP}}
	if err := checkPorts(ok); err != nil {
		t.Fatalf("good ports: %v", err)
	}
	if ok[1].Protocol != corev1.ProtocolTCP {
		t.Errorf("a port with no protocol is TCP, got %q", ok[1].Protocol)
	}
	for want, ports := range map[string][]avsysv1.ExtraPort{
		"game's own":    {{Name: "game", Port: 25565}},
		"console":       {{Name: "rcon", Port: 25575}},
		"appears twice": {{Name: "a", Port: 1000}, {Name: "b", Port: 1000, Protocol: corev1.ProtocolTCP}},
		"up to 15":      {{Name: "Voice", Port: 1000}},
		"ending with":   {{Name: "voice-", Port: 1000}},
		"no two dashes": {{Name: "voice--chat", Port: 1000}},
		"15 lowercase":  {{Name: "a-very-long-name", Port: 1000}},
		"TCP or UDP":    {{Name: "a", Port: 1000, Protocol: corev1.ProtocolSCTP}},
		"is not a port": {{Name: "a", Port: 70000}},
		"up to 8 extra": {{Name: "a", Port: 1}, {Name: "b", Port: 2}, {Name: "c", Port: 3}, {Name: "d", Port: 4}, {Name: "e", Port: 5}, {Name: "f", Port: 6}, {Name: "g", Port: 7}, {Name: "h", Port: 8}, {Name: "i", Port: 9}},
	} {
		if err := checkPorts(ports); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

// A new server cannot take the name another's extra ports are opened under:
// its own Service would be that one.
func TestServiceNameIsFree(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	foo := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "foo", UID: "uid-foo"}}
	ports := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: controller.PortsServiceName("foo")}}
	if err := controllerutil.SetControllerReference(foo, ports, scheme); err != nil {
		t.Fatal(err)
	}
	// A server's own Service, left from one deleted a moment ago, is not
	// another's.
	own := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "bar"}}
	bar := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "bar", UID: "uid-bar"}}
	if err := controllerutil.SetControllerReference(bar, own, scheme); err != nil {
		t.Fatal(err)
	}
	s := &Server{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(foo, ports, own).Build()}
	if err := s.serviceNameIsFree(context.Background(), "mc-x", "foo-ports"); err == nil || !strings.Contains(err.Error(), "foo's extra ports") {
		t.Errorf("foo-ports: %v", err)
	}
	for _, name := range []string{"baz", "bar"} {
		if err := s.serviceNameIsFree(context.Background(), "mc-x", name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
