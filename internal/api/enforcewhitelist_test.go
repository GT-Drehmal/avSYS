package api

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

type props = map[string]string

// wantProperties fails the test unless the server's white-list and
// enforce-whitelist are as wanted, one not wanted being not set.
func wantProperties(t *testing.T, got, want props) {
	t.Helper()
	only := maps.Clone(got)
	maps.DeleteFunc(only, func(k, _ string) bool { return k != "white-list" && k != "enforce-whitelist" })
	if !maps.Equal(only, want) {
		t.Errorf("got %v, want %v", only, want)
	}
}

// A new server with its whitelist on enforces it, unless it was asked not
// to. One made to be imported into is made the same way, stopped.
func TestANewServerEnforcesItsWhitelist(t *testing.T) {
	on := props{"white-list": "true", "enforce-whitelist": "true"}
	for _, c := range []struct {
		name, asked string
		want        props
	}{
		{"on", `{"white-list":"true"}`, on},
		{"on, in capitals", `{"white-list":"TRUE"}`, props{"white-list": "TRUE", "enforce-whitelist": "true"}},
		{"on, in mixed case", `{"white-list":"True"}`, props{"white-list": "True", "enforce-whitelist": "true"}},
		{"on, enforce null", `{"white-list":"true","enforce-whitelist":null}`, on},
		{"on, enforce empty", `{"white-list":"true","enforce-whitelist":""}`, on},
		{"on, enforce true", `{"white-list":"true","enforce-whitelist":"true"}`, on},
		{"on, enforce false", `{"white-list":"true","enforce-whitelist":"false"}`, props{"white-list": "true", "enforce-whitelist": "false"}},
		{"on, enforce False", `{"white-list":"true","enforce-whitelist":"False"}`, props{"white-list": "true", "enforce-whitelist": "False"}},
		{"off", `{"white-list":"false"}`, props{"white-list": "false"}},
		{"not said", `{"motd":"hi"}`, props{}},
		{"none", `null`, props{}},
	} {
		for route, state := range map[string]string{"new": "Running", "import": "Stopped"} {
			t.Run(route+"/"+c.name, func(t *testing.T) {
				p := newPanel(t)
				body := `{"name":"made","spec":{"eula":true,"version":"1.21.4","state":"` + state + `","properties":` + c.asked + `}}`
				if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != 201 {
					t.Fatalf("made: %d %s", code, msg)
				}
				wantProperties(t, p.server("made").Spec.Properties, c.want)
			})
		}
	}
}

// So does a server made from another, by every route there is, whether the
// whitelist is the source's or the request's. A choice of either is kept.
func TestAServerMadeFromAnotherEnforcesItsWhitelist(t *testing.T) {
	on := props{"white-list": "true", "enforce-whitelist": "true"}
	notEnforced := props{"white-list": "true", "enforce-whitelist": "false"}
	cases := []struct {
		name   string
		source props
		asked  string // The request's spec.properties, if it has any.
		want   props
	}{
		{"on at the source", props{"white-list": "true"}, "", on},
		{"on at the source, in capitals", props{"white-list": "TRUE"}, "", props{"white-list": "TRUE", "enforce-whitelist": "true"}},
		{"on at the source, something else asked", props{"white-list": "true"}, `{"difficulty":"hard"}`, on},
		{"enforce true at the source", on, "", on},
		{"enforce false at the source", notEnforced, "", notEnforced},
		{"enforce false at the source, something else asked", notEnforced, `{"difficulty":"hard"}`, notEnforced},
		{"enforce false asked", props{"white-list": "true"}, `{"enforce-whitelist":"false"}`, notEnforced},
		{"enforce false asked over true", on, `{"enforce-whitelist":"false"}`, notEnforced},
		{"enforce taken out", notEnforced, `{"enforce-whitelist":null}`, on},
		{"turned on", props{"white-list": "false"}, `{"white-list":"true"}`, on},
		{"turned on, with nothing at the source", nil, `{"white-list":"true"}`, on},
		{"turned on, enforce false at the source", props{"enforce-whitelist": "false"}, `{"white-list":"true"}`, notEnforced},
		{"turned off", props{"white-list": "true"}, `{"white-list":"false"}`, props{"white-list": "false"}},
		{"turned off, enforce true at the source", on, `{"white-list":"false"}`, props{"white-list": "false", "enforce-whitelist": "true"}},
		{"off at the source", props{"white-list": "false"}, "", props{"white-list": "false"}},
		{"nothing at the source", nil, "", props{}},
	}
	for _, route := range []string{"fromServer", "fromSnapshot", "brought back", "fromTemplate", "fromTemplate with changes"} {
		for _, c := range cases {
			// A template's changes stand in for the request's, below.
			if route == "fromTemplate with changes" && c.asked == "" {
				continue
			}
			t.Run(route+"/"+c.name, func(t *testing.T) {
				ctx := context.Background()
				p := newPanel(t)
				spec := avsysv1.MinecraftServerSpec{
					EULA: true, Version: "1.21.4", Loader: avsysv1.LoaderVanilla, Properties: maps.Clone(c.source),
					Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")},
					Storage:   avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
					Expose:    avsysv1.ExposeSpec{Type: avsysv1.ExposeRouter},
				}
				name, from, asked := "made", "", c.asked
				if route == "fromServer" {
					p.srv.Backups.Restic = "true"
					p.srv.Gateway = &sshgw.Gateway{Dialer: dialHeld{dialed: make(chan struct{}, 4)}}
					source := p.server("world")
					source.Spec.Properties = maps.Clone(c.source)
					if err := p.srv.Client.Update(ctx, source); err != nil {
						t.Fatal(err)
					}
					from = `"fromServer":"world"`
				} else {
					id := strings.Repeat("ab", 32)
					host, trigger := "old", "final"
					from = `"fromSnapshot":"` + id + `"`
					if strings.HasPrefix(route, "fromTemplate") {
						tmpl := &store.Template{OrgID: p.orgID("alpha"), Name: "Kept", CreatedBy: "owner"}
						if err := p.srv.Store.CreateTemplate(ctx, tmpl); err != nil {
							t.Fatal(err)
						}
						if route == "fromTemplate with changes" {
							if err := p.srv.Store.SetTemplateCurrent(ctx, tmpl.OrgID, tmpl.ID, "", `{"properties":`+asked+`}`); err != nil {
								t.Fatal(err)
							}
							asked = ""
						}
						host, trigger = templateHost(tmpl.ID), "template"
						from = `"fromTemplate":"` + tmpl.ID + `"`
					} else if route == "brought back" {
						// Under its own name, which no server has now.
						name = host
					}
					p.withSnapshots(
						[]map[string]any{{"id": id, "time": time.Now().Add(-time.Hour), "hostname": host, "tags": []string{"avsys", "trigger=" + trigger}}},
						map[string]snapshotRecord{id: {Format: 1, Org: "alpha", Server: host, Spec: spec}})
				}
				body := `{"name":"` + name + `",` + from + `}`
				if asked != "" {
					body = `{"name":"` + name + `",` + from + `,"spec":{"properties":` + asked + `}}`
				}
				if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != 201 {
					t.Fatalf("made: %d %s", code, msg)
				}
				t.Cleanup(func() {
					// Calls off the copying of files, which never come here.
					if code, msg := p.do("owner", "DELETE", "/orgs/alpha/servers/"+name, ""); code != 204 {
						t.Errorf("deleting it: %d %s", code, msg)
					}
				})
				wantProperties(t, p.server(name).Spec.Properties, c.want)
				if route == "fromServer" {
					wantProperties(t, p.server("world").Spec.Properties, onlyWhitelist(c.source))
				}
			})
		}
	}
}

// onlyWhitelist is what wantProperties compares of some properties.
func onlyWhitelist(all props) props {
	out := props{}
	for _, k := range []string{"white-list", "enforce-whitelist"} {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Turning the whitelist on turns enforce-whitelist on. With it on already
// it is never put back, as someone may have taken it out, and turning the
// whitelist off leaves it alone.
func TestPatchEnforcesAWhitelistTurnedOn(t *testing.T) {
	on := props{"white-list": "true", "enforce-whitelist": "true"}
	notEnforced := props{"white-list": "true", "enforce-whitelist": "false"}
	bare := props{"white-list": "true"}
	for _, c := range []struct {
		name  string
		was   props
		patch string
		want  props
	}{
		{"from nothing", nil, `{"properties":{"white-list":"true"}}`, on},
		{"from false", props{"white-list": "false"}, `{"properties":{"white-list":"true"}}`, on},
		{"from False", props{"white-list": "False"}, `{"properties":{"white-list":"true"}}`, on},
		{"from empty", props{"white-list": ""}, `{"properties":{"white-list":"true"}}`, on},
		{"in capitals", nil, `{"properties":{"white-list":"TRUE"}}`, props{"white-list": "TRUE", "enforce-whitelist": "true"}},
		{"enforce null", props{"enforce-whitelist": "false"}, `{"properties":{"white-list":"true","enforce-whitelist":null}}`, on},
		{"enforce empty", nil, `{"properties":{"white-list":"true","enforce-whitelist":""}}`, on},
		{"enforce false with it", nil, `{"properties":{"white-list":"true","enforce-whitelist":"false"}}`, notEnforced},
		{"enforce false already", props{"enforce-whitelist": "false"}, `{"properties":{"white-list":"true"}}`, notEnforced},
		{"on already, sent again", bare, `{"properties":{"white-list":"true"}}`, bare},
		{"on already, in another case", props{"white-list": "TRUE"}, `{"properties":{"white-list":"true"}}`, bare},
		{"on already, enforce taken out", on, `{"properties":{"enforce-whitelist":null}}`, bare},
		{"on already, enforce set false", on, `{"properties":{"enforce-whitelist":"false"}}`, notEnforced},
		{"on already, another property", bare, `{"properties":{"motd":"hi"}}`, bare},
		{"on already, another setting", bare, `{"displayName":"World"}`, bare},
		{"on already, nothing changed", bare, `{}`, bare},
		{"off, with enforce", on, `{"properties":{"white-list":"false"}}`, props{"white-list": "false", "enforce-whitelist": "true"}},
		{"off, without", bare, `{"properties":{"white-list":"false"}}`, props{"white-list": "false"}},
		{"unset, with enforce", on, `{"properties":{"white-list":null}}`, props{"enforce-whitelist": "true"}},
		{"every property cleared", on, `{"properties":null}`, props{}},
		{"off, something else", props{"white-list": "false"}, `{"properties":{"motd":"hi"}}`, props{"white-list": "false"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPanel(t)
			mc := p.server("world")
			mc.Spec.Properties = maps.Clone(c.was)
			if err := p.srv.Client.Update(context.Background(), mc); err != nil {
				t.Fatal(err)
			}
			if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":`+c.patch+`}`); code != 200 {
				t.Fatalf("saved: %d %s", code, msg)
			}
			wantProperties(t, p.server("world").Spec.Properties, c.want)
		})
	}
}

// Nothing else that reads or writes a server's properties adds it: a server
// that has been without stays so, and a restore is as the snapshot was.
func TestOtherRoutesLeaveEnforceWhitelistAlone(t *testing.T) {
	bare := props{"white-list": "true"}
	const world = "/orgs/alpha/servers/world"
	for name, act := range map[string]func(t *testing.T, p *panel, key client.ObjectKey){
		"read": func(t *testing.T, p *panel, _ client.ObjectKey) {
			for _, path := range []string{world, "/orgs/alpha/servers"} {
				if code, msg := p.do("owner", "GET", path, ""); code != 200 {
					t.Fatalf("GET %s: %d %s", path, code, msg)
				}
			}
		},
		"notes": func(t *testing.T, p *panel, _ client.ObjectKey) {
			if code, msg := p.do("owner", "PATCH", world, `{"notes":"ours"}`); code != 200 {
				t.Fatalf("notes: %d %s", code, msg)
			}
		},
		"start": func(t *testing.T, p *panel, _ client.ObjectKey) {
			if code, msg := p.do("owner", "POST", world+"/start", ""); code != 200 {
				t.Fatalf("start: %d %s", code, msg)
			}
		},
		"a new world": func(t *testing.T, p *panel, key client.ObjectKey) {
			seed := "42"
			if err := p.srv.setWorldSettings(context.Background(), key, map[string]*string{"level-seed": &seed}); err != nil {
				t.Fatal(err)
			}
		},
		"a restore that turns the whitelist on": func(t *testing.T, p *panel, key client.ObjectKey) {
			ctx := context.Background()
			if err := p.srv.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) { spec.Properties = props{"white-list": "false"} }); err != nil {
				t.Fatal(err)
			}
			then := p.server("world").Spec.DeepCopy()
			then.Properties = maps.Clone(bare)
			if err := p.srv.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) { restoreSoftware(spec, then) }); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPanel(t)
			mc := p.server("world")
			mc.Spec.Properties = maps.Clone(bare)
			if err := p.srv.Client.Update(context.Background(), mc); err != nil {
				t.Fatal(err)
			}
			act(t, p, client.ObjectKeyFromObject(mc))
			wantProperties(t, p.server("world").Spec.Properties, bare)
		})
	}

	// A template keeps what it was given: a server gets it as it is made.
	p := newPanel(t)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/templates", `{"name":"Walled","spec":{"loader":"vanilla","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"properties":{"white-list":"true"}}}`); code != 201 {
		t.Fatalf("template: %d %s", code, msg)
	}
	all, err := p.srv.Store.Templates(context.Background(), p.orgID("alpha"))
	if err != nil || len(all) != 1 {
		t.Fatalf("templates: %v %v", all, err)
	}
	if strings.Contains(all[0].Settings, "enforce-whitelist") {
		t.Errorf("a template was given enforce-whitelist as it was saved: %s", all[0].Settings)
	}
}
