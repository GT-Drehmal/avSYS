package api

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A restart is the game stopping in place only when the controller has seen
// the server's latest settings and found nothing new in them.
func TestRestartsInPlace(t *testing.T) {
	server := func(phase avsysv1.Phase, pending bool, observed int64) *avsysv1.MinecraftServer {
		mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Generation: 3}}
		mc.Status.Phase, mc.Status.PendingRestart, mc.Status.ObservedGeneration = phase, pending, observed
		return mc
	}
	for _, c := range []struct {
		name string
		mc   *avsysv1.MinecraftServer
		want bool
	}{
		{"running, nothing new", server(avsysv1.PhaseRunning, false, 3), true},
		{"running, changes waiting", server(avsysv1.PhaseRunning, true, 3), false},
		// Changed a moment ago: the status is of the settings before.
		{"running, changed since it was seen", server(avsysv1.PhaseRunning, false, 2), false},
		{"starting", server(avsysv1.PhaseStarting, false, 3), false},
	} {
		if got := restartsInPlace(c.mc); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
