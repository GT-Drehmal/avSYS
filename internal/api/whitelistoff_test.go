package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// With enforce-whitelist on, vanilla up to 1.21.8 (not Paper, nor 1.21.9
// on) puts out everyone not on the whitelist at "whitelist remove" and
// "whitelist reload", even with the whitelist off. So while the game has its
// whitelist off the panel only puts players on it: who is to come off comes
// off once the game has it on.

// gameProperties writes the server.properties the game keeps, as it does
// when it starts and when its whitelist is turned on or off at the console.
func gameProperties(t *testing.T, sc *sftp.Client, lines string) {
	t.Helper()
	f, err := sc.Create(dataRoot + "/server.properties")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("#Minecraft server properties\nmotd=A Minecraft Server\n" + lines)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// What the game has decides, not the panel's settings, which reach it only
// as it starts; and where it cannot be told, players are taken off as ever.
func TestTheGamesOwnWhitelistDecidesWhoIsTakenOff(t *testing.T) {
	for _, c := range []struct {
		name string
		spec map[string]string
		file string // "-" for none to be read
		held bool
	}{
		{"off in the settings and in game", map[string]string{"white-list": "false"}, "white-list=false\n", true},
		{"turned on in the settings, the game still off until it restarts", map[string]string{"white-list": "true"}, "white-list=false\n", true},
		{"off in the game, said in capitals", nil, "white-list=FALSE\r\n", true},
		{"not set in the settings, on in game", nil, "white-list=true\n", false},
		{"turned off in the settings, the game still on until it restarts", map[string]string{"white-list": "false"}, "white-list=true\n", false},
		{"on in game, said last", map[string]string{"white-list": "false"}, "white-list=false\nwhite-list=true\n", false},
		{"not said in the game's file", map[string]string{"white-list": "false"}, "", false},
		{"the game's file cannot be read", map[string]string{"white-list": "false"}, "-", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			p := newPanel(t)
			g := runningWithLists(t, p)
			changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
				mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "off", "admin"
				mc.Spec.Properties = c.spec
			})
			if c.file != "-" {
				gameProperties(t, g.sc, c.file)
			} else {
				// Its files out of reach, as while its workspace is down.
				p.srv.workspaces.mu.Lock()
				delete(p.srv.workspaces.conns, "mc-alpha/world")
				p.srv.workspaces.mu.Unlock()
			}
			if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
				t.Fatal(err)
			}
			if got := g.commands(); got != "whitelist add Ada; whitelist add Olive" {
				t.Errorf("put on: %q", got)
			}
			if err := p.srv.Store.RemoveMember(ctx, p.orgID("alpha"), p.users["admin"].ID); err != nil {
				t.Fatal(err)
			}
			if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
				t.Fatal(err)
			}
			want := "whitelist remove Ada"
			if c.held {
				want = ""
			}
			if got := g.commands(); got != want {
				t.Errorf("once Ada left: %q, want %q", got, want)
			}
		})
	}
}

// Held while the game has its whitelist off, through a change of settings
// that turns it on, and taken off as the game starts with it on, or has it
// turned on at the console.
func TestHeldRemovalsComeOffOnceTheGameHasItsWhitelistOn(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	g := runningWithLists(t, p)
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "off", "admin"
		mc.Spec.Whitelist = []string{"Steve"}
		mc.Spec.Properties = map[string]string{"white-list": "false", "enforce-whitelist": "true"}
	})
	gameProperties(t, g.sc, "white-list=false\nenforce-whitelist=true\n")
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	look := func() string {
		t.Helper()
		p.srv.syncOrgOps(ctx, org)
		return g.commands()
	}
	if got := look(); got != "whitelist add Ada; whitelist add Olive; whitelist add Steve" {
		t.Errorf("put on with the whitelist off: %q", got)
	}

	// Ada leaves, and Steve is taken out of the settings: neither comes
	// off, and the panel still has them as its own, to take off later.
	if code, msg := p.do("owner", "DELETE", "/orgs/alpha/members/"+p.users["admin"].ID, ""); code != http.StatusNoContent {
		t.Fatalf("removing Ada: %d %s", code, msg)
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"whitelist":[]}}`); code != http.StatusOK {
		t.Fatalf("taking Steve out: %d %s", code, msg)
	}
	// Turned on in the settings: the game has it off until it restarts.
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"properties":{"white-list":"true"}}}`); code != http.StatusOK {
		t.Fatalf("turning the whitelist on: %d %s", code, msg)
	}
	ran := g.since()
	time.Sleep(300 * time.Millisecond) // for the syncs those start by themselves
	if got := ran() + "|" + look(); got != "|" {
		t.Errorf("run with the game's whitelist off: %q, want nothing", got)
	}
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != "Ada,Olive,Steve" {
		t.Errorf("the panel's note: %q", got)
	}

	// The game starts again, with its whitelist on: they come off at once.
	stopped := world(t, p)
	stopped.Status.Phase = avsysv1.PhaseStarting
	gameProperties(t, g.sc, "white-list=true\nenforce-whitelist=true\n")
	ran = g.since()
	p.srv.started(stopped, world(t, p))
	waitFor(t, "those who left are taken off as the game starts with its whitelist on", func() bool { return ran() == "whitelist remove Ada; whitelist remove Steve" })
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != "Olive" {
		t.Errorf("the panel's note once on: %q", got)
	}

	// Off at the console, then on again there: taken off with the next look.
	gameProperties(t, g.sc, "white-list=false\n")
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.AutoWhitelist = "off" })
	if got := look(); got != "" {
		t.Errorf("with the game's whitelist off at the console: %q", got)
	}
	gameProperties(t, g.sc, "white-list=true\n")
	if got := look(); got != "whitelist remove Olive" {
		t.Errorf("with the game's whitelist on at the console: %q", got)
	}
}

// Operators are taken off as ever: that puts nobody out.
func TestOperatorsAreTakenOffWithTheWhitelistOff(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	g := runningWithLists(t, p)
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "admin", "off"
		mc.Spec.Properties = map[string]string{"white-list": "false"}
	})
	gameProperties(t, g.sc, "white-list=false\n")
	if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
		t.Fatal(err)
	}
	if got := g.commands(); got != "op Ada; op Olive" {
		t.Errorf("made operators: %q", got)
	}
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.AutoOp = "owner" })
	if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
		t.Fatal(err)
	}
	if got := g.commands(); got != "deop Ada" {
		t.Errorf("an operator no longer: %q", got)
	}
}

// Bedrock players go on and off by the whitelist's file, which the game is
// then told to read again: that waits for the game to have it on.
func TestBedrockPlayersWaitForTheWhitelist(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	g := runningWithLists(t, p)
	p.srv.catalog.http.Transport = newGeyserAccounts(newMojangAccounts(), map[string]string{"ABCDEFGHIJKLMNO1": "1001"})
	_, event := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Event","bedrock":["ABCDEFGHIJKLMNO1"]}`)
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp = "off"
		mc.Spec.WhitelistLists = []string{event.ID}
	})
	gameProperties(t, g.sc, "white-list=false\n")
	ran := g.since()
	if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := ran(); got != "" {
		t.Errorf("run with the game's whitelist off: %q", got)
	}
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != "" {
		t.Errorf("noted as put on, with the whitelist off: %q", got)
	}
	gameProperties(t, g.sc, "white-list=true\n")
	if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
		t.Fatal(err)
	}
	if got := g.commands(); got != "whitelist reload" {
		t.Errorf("put on once the game has its whitelist on: %q", got)
	}
}

// Taking a player added in game off the whitelist by hand waits for the
// game to have its whitelist on, or the server to be stopped.
func TestRemovingByHandWaitsForTheWhitelist(t *testing.T) {
	p := newPanel(t)
	g := runningWithLists(t, p)
	g.whitelist = []string{"Herobrine"}
	g.saveWhitelist()
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "off", "off" })
	// On in the settings, not yet in the game.
	gameProperties(t, g.sc, "white-list=false\n")
	const herobrine = "/orgs/alpha/servers/world/lists/whitelist/Herobrine"
	if code, body := call(p, "admin", "DELETE", herobrine, ""); code != http.StatusConflict || !strings.Contains(body, `"code":"whitelist_off"`) {
		t.Errorf("taken off with the game's whitelist off: %d %s", code, body)
	}
	if got := g.commands(); got != "" {
		t.Errorf("run: %q", got)
	}
	gameProperties(t, g.sc, "white-list=true\n")
	if code, body := call(p, "admin", "DELETE", herobrine, ""); code != http.StatusNoContent {
		t.Errorf("taken off with it on: %d %s", code, body)
	}
	if got := g.commands(); got != "whitelist remove Herobrine" {
		t.Errorf("run once on: %q", got)
	}
}
