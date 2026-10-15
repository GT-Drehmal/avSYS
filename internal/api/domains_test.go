package api

import (
	"testing"
)

// A domain the panel's administrator gives an organization is its alone: no
// other claims one overlapping it, and no other's server takes a hostname
// under it, though one that had it already keeps it.
func TestDomainsAreTheirOrganizations(t *testing.T) {
	p := newPanel(t)
	// Beta's server had its address before alpha claimed the domain it is under.
	p.addServer("beta", "early", "early.alpha.example.org")
	// And one made outside the panel, its name in another case, with a final dot.
	p.addServer("beta", "manual", "Manual.Alpha.Example.org.")

	code, got := p.get("root", "PUT", "/orgs/alpha/domains", `{"domains":["Alpha.Example.org.", "*.alpha.example.org"]}`)
	if code != 200 || len(got["domains"].([]any)) != 1 || got["domains"].([]any)[0] != "alpha.example.org" {
		t.Fatalf("claiming: %d %v", code, got)
	}
	inUse, _ := got["inUse"].([]any)
	if len(inUse) != 2 || inUse[0].(map[string]any)["server"] != "early" || inUse[0].(map[string]any)["org"] != "beta" || inUse[1].(map[string]any)["hostname"] != "manual.alpha.example.org" {
		t.Errorf("servers already under it: %v", inUse)
	}
	if code, name := p.do("admin", "PUT", "/orgs/alpha/domains", `{"domains":["mine.example.org"]}`); code != 403 {
		t.Errorf("an organization's own admin claiming: %d %s, want 403", code, name)
	}
	for _, d := range []string{"play.alpha.example.org", "example.org", "alpha.example.org"} {
		if code, name := p.do("root", "PUT", "/orgs/beta/domains", `{"domains":["`+d+`"]}`); code != 409 || name != "domain_claimed" {
			t.Errorf("beta claiming %s: %d %s, want 409 domain_claimed", d, code, name)
		}
	}
	for _, d := range []string{"not a domain", "*", "*.", "a.*.example.org", "*.com"} {
		if code, _ := p.do("root", "PUT", "/orgs/beta/domains", `{"domains":["`+d+`"]}`); code != 400 {
			t.Errorf("a claim of %q, which is no domain: %d, want 400", d, code)
		}
	}

	// Beta cannot make a server under alpha's domain; alpha can.
	if code, name := p.do("outsider", "POST", "/orgs/beta/servers", `{"name":"squat","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"play.alpha.example.org"}}}`); code != 409 || name != "domain_claimed" {
		t.Errorf("beta under alpha's domain: %d %s, want 409 domain_claimed", code, name)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"lobby","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"play.alpha.example.org"}}}`); code != 201 {
		t.Errorf("alpha under its own: %d %s", code, msg)
	}
	// Under no claimed domain, anyone may.
	if code, msg := p.do("outsider", "POST", "/orgs/beta/servers", `{"name":"free","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"play.unclaimed.example.net"}}}`); code != 201 {
		t.Errorf("beta under an unclaimed domain: %d %s", code, msg)
	}

	// The server that had it keeps it through other changes, but cannot
	// move to another name under alpha's domain.
	if code, msg := p.do("outsider", "PATCH", "/orgs/beta/servers/early", `{"spec":{"displayName":"Early"}}`); code != 200 {
		t.Errorf("changing something else on a server that had it already: %d %s", code, msg)
	}
	if code, msg := p.do("outsider", "PATCH", "/orgs/beta/servers/manual", `{"spec":{"displayName":"Manual"}}`); code != 200 {
		t.Errorf("changing something else on one whose name is written another way: %d %s", code, msg)
	}
	if code, name := p.do("outsider", "PATCH", "/orgs/beta/servers/early", `{"spec":{"expose":{"type":"Router","hostname":"later.alpha.example.org"}}}`); code != 409 || name != "domain_claimed" {
		t.Errorf("moving to another name under it: %d %s, want 409 domain_claimed", code, name)
	}

	// Members see their organization's domains, and that they cannot change them.
	if code, got := p.get("viewer", "GET", "/orgs/alpha/domains", ""); code != 200 || got["editable"] != false || len(got["domains"].([]any)) != 1 {
		t.Errorf("a member reading: %d %v", code, got)
	}
}

// The names the panel gives a server by itself, under its organization's own
// name in the router's domain, are that organization's, whatever is claimed.
func TestOwnZoneIsTheOrganizations(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.RouterDomain = "mc.example.org"
	// Even given a domain over it, beta takes no name in alpha's zone.
	if code, msg := p.do("root", "PUT", "/orgs/beta/domains", `{"domains":["alpha.mc.example.org"]}`); code != 200 {
		t.Fatalf("claiming: %d %s", code, msg)
	}
	if code, name := p.do("outsider", "POST", "/orgs/beta/servers", `{"name":"squat","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"lobby.alpha.mc.example.org"}}}`); code != 409 || name != "domain_claimed" {
		t.Errorf("beta in alpha's zone: %d %s, want 409 domain_claimed", code, name)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"lobby","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"lobby.alpha.mc.example.org"}}}`); code != 201 {
		t.Errorf("alpha in its own zone: %d %s", code, msg)
	}
	if code, msg := p.do("outsider", "POST", "/orgs/beta/servers", `{"name":"own","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"own.beta.mc.example.org"}}}`); code != 201 {
		t.Errorf("beta in its own zone: %d %s", code, msg)
	}
	// Nor in the zone of an organization not made yet.
	if code, name := p.do("outsider", "POST", "/orgs/beta/servers", `{"name":"early","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"lobby.gamma.mc.example.org"}}}`); code != 409 || name != "domain_claimed" {
		t.Errorf("beta in a zone of no organization yet: %d %s, want 409 domain_claimed", code, name)
	}
	// A name right under the router's domain is in no organization's zone.
	if code, msg := p.do("outsider", "POST", "/orgs/beta/servers", `{"name":"apex","spec":{"eula":true,"version":"1.21.4","expose":{"hostname":"apex.mc.example.org"}}}`); code != 201 {
		t.Errorf("beta right under the router's domain: %d %s", code, msg)
	}
	// The router's domain as configured may be written another way.
	p.srv.Config.RouterDomain = "MC.Example.org."
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"second","spec":{"eula":true,"version":"1.21.4"}}`); code != 201 {
		t.Errorf("alpha's own default name with the router's domain written another way: %d %s", code, msg)
	}
}
