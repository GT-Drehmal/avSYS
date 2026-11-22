package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// With enforce-whitelist on, vanilla up to 1.21.8 (not Paper, nor 1.21.9
// on) puts out everyone not on the whitelist at "whitelist remove" and
// "whitelist reload", even with the whitelist off. So while it is off the panel only puts players on it: who
// is to come off comes off once it is on again, by a change to the server
// or as it starts.
func TestNothingIsTakenOffAWhitelistThatIsOff(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	g := runningWithLists(t, p)
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "off", "admin"
		mc.Spec.Whitelist = []string{"Steve"}
		mc.Spec.Properties = map[string]string{"white-list": "false", "enforce-whitelist": "true"}
	})
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	look := func() string {
		t.Helper()
		p.srv.syncOrgOps(ctx, org)
		return g.commands()
	}
	// Put on, with the whitelist off: that puts nobody out.
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
	ran := g.since()
	time.Sleep(300 * time.Millisecond) // for the syncs those start by themselves
	if got := ran() + "|" + look(); got != "|" {
		t.Errorf("run with the whitelist off: %q, want nothing", got)
	}
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != "Ada,Olive,Steve" {
		t.Errorf("the panel's note: %q", got)
	}

	// Turned on: they come off at once, with nothing else to set it off.
	ran = g.since()
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"properties":{"white-list":"true"}}}`); code != http.StatusOK {
		t.Fatalf("turning the whitelist on: %d %s", code, msg)
	}
	waitFor(t, "those who left are taken off once the whitelist is on", func() bool { return ran() == "whitelist remove Ada; whitelist remove Steve" })
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != "Olive" {
		t.Errorf("the panel's note once on: %q", got)
	}

	// Off again, a change that puts no one on runs nothing.
	ran = g.since()
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"properties":{"white-list":"false"}}}`); code != http.StatusOK {
		t.Fatalf("turning it off: %d %s", code, msg)
	}
	time.Sleep(300 * time.Millisecond)
	if got := ran(); got != "" {
		t.Errorf("turning it off ran %q", got)
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
// then told to read again: that waits for the whitelist to be on.
func TestBedrockPlayersWaitForTheWhitelist(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	g := runningWithLists(t, p)
	p.srv.catalog.http.Transport = newGeyserAccounts(newMojangAccounts(), map[string]string{"ABCDEFGHIJKLMNO1": "1001"})
	_, event := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Event","bedrock":["ABCDEFGHIJKLMNO1"]}`)
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp = "off"
		mc.Spec.WhitelistLists = []string{event.ID}
		mc.Spec.Properties = map[string]string{"white-list": "false"}
	})
	ran := g.since()
	if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := ran(); got != "" {
		t.Errorf("run with the whitelist off: %q", got)
	}
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != "" {
		t.Errorf("noted as put on, with the whitelist off: %q", got)
	}
	ran = g.since()
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"properties":{"white-list":"true"}}}`); code != http.StatusOK {
		t.Fatalf("turning the whitelist on: %d %s", code, msg)
	}
	waitFor(t, "a Bedrock player put on once the whitelist is on", func() bool { return ran() == "whitelist reload" })
}

// Taking a player added in game off the whitelist by hand waits for the
// whitelist to be on, or the server to be stopped.
func TestRemovingByHandWaitsForTheWhitelist(t *testing.T) {
	p := newPanel(t)
	g := runningWithLists(t, p)
	g.whitelist = []string{"Herobrine"}
	g.saveWhitelist()
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "off", "off"
		mc.Spec.Properties = map[string]string{"white-list": "false"}
	})
	const herobrine = "/orgs/alpha/servers/world/lists/whitelist/Herobrine"
	if code, body := call(p, "admin", "DELETE", herobrine, ""); code != http.StatusConflict || !strings.Contains(body, `"code":"whitelist_off"`) {
		t.Errorf("taken off with the whitelist off: %d %s", code, body)
	}
	if got := g.commands(); got != "" {
		t.Errorf("run: %q", got)
	}
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.Properties["white-list"] = "true" })
	if code, body := call(p, "admin", "DELETE", herobrine, ""); code != http.StatusNoContent {
		t.Errorf("taken off with it on: %d %s", code, body)
	}
	if got := g.commands(); got != "whitelist remove Herobrine" {
		t.Errorf("run once on: %q", got)
	}
}
