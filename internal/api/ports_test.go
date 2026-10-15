package api

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
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
		"TCP or UDP":    {{Name: "a", Port: 1000, Protocol: corev1.ProtocolSCTP}},
		"is not a port": {{Name: "a", Port: 70000}},
		"up to 8 extra": {{Name: "a", Port: 1}, {Name: "b", Port: 2}, {Name: "c", Port: 3}, {Name: "d", Port: 4}, {Name: "e", Port: 5}, {Name: "f", Port: 6}, {Name: "g", Port: 7}, {Name: "h", Port: 8}, {Name: "i", Port: 9}},
	} {
		if err := checkPorts(ports); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}
