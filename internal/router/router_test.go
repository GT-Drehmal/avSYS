package router

import (
	"encoding/json"
	"strings"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A server that cannot be joined is listed as one the game will not let
// anyone into, with the reason in a word. One that can be is listed as an
// ordinary server.
func TestEntryStatus(t *testing.T) {
	const client = 767
	off := entry{title: "survival", detail: "Offline.", closed: "Offline"}.status(client)
	if off.Version.Name != "Offline" || off.Version.Protocol == client || off.Version.Protocol >= 0 {
		t.Errorf("a stopped server's version: %+v, want one called Offline that no client speaks", off.Version)
	}
	var text struct {
		Text  string
		Extra []struct{ Text string }
	}
	if err := json.Unmarshal(off.Description, &text); err != nil || !strings.HasPrefix(text.Text, "survival") || len(text.Extra) != 1 || text.Extra[0].Text != "Offline." {
		t.Errorf("a stopped server's two lines: %s, %v", off.Description, err)
	}

	asleep := entry{title: "survival", detail: "Asleep. Join to wake it up.", max: 20}.status(client)
	if asleep.Version.Protocol != client || asleep.Players.Max != 20 || asleep.Players.Online != 0 {
		t.Errorf("a sleeping server: version %+v players %+v, want the client's own version and 0 of 20", asleep.Version, asleep.Players)
	}
}

func TestMaxPlayers(t *testing.T) {
	for set, want := range map[string]int{"": 20, "50": 50, "0": 20, "-3": 20, "many": 20} {
		mc := &avsysv1.MinecraftServer{}
		if set != "" {
			mc.Spec.Properties = map[string]string{"max-players": set}
		}
		if got := maxPlayers(mc); got != want {
			t.Errorf("max-players %q: %d, want %d", set, got, want)
		}
	}
}
