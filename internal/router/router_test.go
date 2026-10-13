package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	// A limit set by hand in server.properties is in no setting, and is
	// known from what the game last said.
	mc := &avsysv1.MinecraftServer{}
	mc.Status.MaxPlayers = 64
	if got := maxPlayers(mc); got != 64 {
		t.Errorf("with only what the game last said: %d, want 64", got)
	}
	// A setting that has since been made is what it will take when it wakes.
	mc.Spec.Properties = map[string]string{"max-players": "10"}
	if got := maxPlayers(mc); got != 10 {
		t.Errorf("with a setting as well: %d, want 10", got)
	}
}

// A server's own icon is in its entry whether or not it can be joined, as
// long as it turns up in time.
func TestFavicon(t *testing.T) {
	mc := &avsysv1.MinecraftServer{}
	mc.Namespace, mc.Name = "mc-alpha", "world"
	png := []byte("\x89PNG not really")

	r := &Router{}
	if got := r.favicon(t.Context(), mc); got != "" {
		t.Errorf("with nowhere to ask: %q", got)
	}
	r.Icon = func(_ context.Context, namespace, name string) []byte {
		if namespace != "mc-alpha" || name != "world" {
			t.Errorf("asked for %s/%s", namespace, name)
		}
		return png
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if got := r.favicon(t.Context(), mc); got != want {
		t.Errorf("an icon: %q, want %q", got, want)
	}
	if st := (entry{title: "world", detail: "Offline.", closed: "Offline", icon: want}).status(767); st.Favicon != want {
		t.Errorf("a stopped server's entry carries %q", st.Favicon)
	}
	r.Icon = func(context.Context, string, string) []byte { return nil }
	if got := r.favicon(t.Context(), mc); got != "" {
		t.Errorf("a server with none: %q", got)
	}
	// One that is slow to read does not hold the list up.
	release := make(chan struct{})
	r.Icon = func(context.Context, string, string) []byte { <-release; return png }
	began := time.Now()
	if got := r.favicon(t.Context(), mc); got != "" || time.Since(began) > 3*iconWait {
		t.Errorf("a slow icon: %q after %v, want none and no long wait", got, time.Since(began))
	}
	close(release)
}
