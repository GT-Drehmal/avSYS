package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

var (
	geyserPort = avsysv1.ExtraPort{Name: "geyser", Port: 19132, Protocol: corev1.ProtocolUDP, For: "geyser"}
	geyserTCP  = avsysv1.ExtraPort{Name: "geyser-tcp", Port: 19132, Protocol: corev1.ProtocolTCP, For: "geyser"}
	ownPort    = avsysv1.ExtraPort{Name: "votifier", Port: 8192, Protocol: corev1.ProtocolTCP}
)

// Adding a known mod opens its port in the same save, taking it off closes
// that port, and the server's own ports stay as they are. A request that
// says the ports has them as it says.
func TestModPortsFollowContent(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = offline{
		"https://api.modrinth.com/v2/version/g1": `{"id":"g1","version_number":"2.12.1","files":[]}`,
	}
	const geyser = `{"source":"modrinth","kind":"mod","project":"geyser","version":"g1","title":"Geyser","iconUrl":"https://cdn.example/geyser.png"}`
	ports := func(name string) []avsysv1.ExtraPort {
		t.Helper()
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: name}, mc); err != nil {
			t.Fatal(err)
		}
		return mc.Spec.Ports
	}
	patch := func(body string) {
		t.Helper()
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", body); code != 200 {
			t.Fatalf("%s: %d %s", body, code, msg)
		}
	}
	patch(`{"spec":{"ports":[{"name":"votifier","port":8192}]}}`)

	patch(`{"spec":{"mods":[` + geyser + `]}}`)
	if got, want := ports("world"), []avsysv1.ExtraPort{ownPort, geyserPort, geyserTCP}; !reflect.DeepEqual(got, want) {
		t.Errorf("Geyser added: %+v, want %+v", got, want)
	}
	// Saved again as it is, nothing changes.
	patch(`{"spec":{"mods":[` + geyser + `]}}`)
	if got := ports("world"); len(got) != 3 {
		t.Errorf("Geyser kept: %+v", got)
	}
	patch(`{"spec":{"mods":[]}}`)
	if got, want := ports("world"), []avsysv1.ExtraPort{ownPort}; !reflect.DeepEqual(got, want) {
		t.Errorf("Geyser taken off: %+v, want %+v", got, want)
	}

	// The ports said with the mods are the ones it has.
	patch(`{"spec":{"mods":[` + geyser + `],"ports":[]}}`)
	if got := ports("world"); len(got) != 0 {
		t.Errorf("ports said: %+v", got)
	}
	patch(`{"spec":{"mods":[]}}`)

	// A port of its own by Geyser's name is not taken for Geyser's, and
	// stays when Geyser goes.
	patch(`{"spec":{"ports":[{"name":"geyser","port":19133,"protocol":"UDP"}]}}`)
	patch(`{"spec":{"mods":[` + geyser + `]}}`)
	patch(`{"spec":{"mods":[]}}`)
	if got, want := ports("world"), []avsysv1.ExtraPort{{Name: "geyser", Port: 19133, Protocol: corev1.ProtocolUDP}}; !reflect.DeepEqual(got, want) {
		t.Errorf("own port by its name: %+v, want %+v", got, want)
	}

	// A new server with Geyser has its port, unless the request says the ports.
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"bedrock","spec":{"eula":true,"version":"1.21.4","mods":[`+geyser+`]}}`); code != 201 {
		t.Fatalf("create: %d %s", code, msg)
	}
	if got, want := ports("bedrock"), []avsysv1.ExtraPort{geyserPort, geyserTCP}; !reflect.DeepEqual(got, want) {
		t.Errorf("new server: %+v, want %+v", got, want)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"closed","spec":{"eula":true,"version":"1.21.4","mods":[`+geyser+`],"ports":[]}}`); code != 201 {
		t.Fatalf("create: %d %s", code, msg)
	}
	if got := ports("closed"); len(got) != 0 {
		t.Errorf("new server with the ports said: %+v", got)
	}

	// A port says it is for a mod the panel knows.
	if code, _ := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"ports":[{"name":"thing","port":1234,"for":"thing"}]}}`); code != 400 {
		t.Errorf("a port for an unknown mod: %d, want 400", code)
	}
	if err := checkPorts([]avsysv1.ExtraPort{{Name: "thing", Port: 1234, For: "thing"}}); err == nil || !strings.Contains(err.Error(), "not a mod") {
		t.Errorf("unknown mod: %v", err)
	}
}

// A copy, or a server from a snapshot or template, with its mods chosen
// again opens and closes their ports as the original's change, and keeps
// the original's ports when the mods are left as they were.
func TestModPortsOfACopy(t *testing.T) {
	geyser := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "geyser"}
	chat := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "simple-voice-chat"}
	chatPort := avsysv1.ExtraPort{Name: "voice-chat", Port: 24454, Protocol: corev1.ProtocolUDP, For: "simple-voice-chat"}
	recorded := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{geyser}, Ports: []avsysv1.ExtraPort{ownPort, geyserPort}}

	got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil)
	if !reflect.DeepEqual(got.Ports, recorded.Ports) {
		t.Errorf("as it was: %+v", got.Ports)
	}
	asked := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{chat}}
	got = fromSnapshot(asked, recorded, map[string]json.RawMessage{"mods": nil})
	if want := []avsysv1.ExtraPort{ownPort, chatPort}; !reflect.DeepEqual(got.Ports, want) {
		t.Errorf("mods chosen again: %+v, want %+v", got.Ports, want)
	}
	asked = &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{chat}, Ports: []avsysv1.ExtraPort{ownPort}}
	got = fromSnapshot(asked, recorded, map[string]json.RawMessage{"mods": nil, "ports": nil})
	if want := []avsysv1.ExtraPort{ownPort}; !reflect.DeepEqual(got.Ports, want) {
		t.Errorf("ports said: %+v, want %+v", got.Ports, want)
	}
}

// A restore puts back the mods a snapshot had, and with them the ports
// opened for them then; mods it takes off take theirs.
func TestModPortsOfARestore(t *testing.T) {
	geyser := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "geyser"}
	moved := avsysv1.ExtraPort{Name: "geyser", Port: 19133, Protocol: corev1.ProtocolUDP, For: "geyser"}
	now := &avsysv1.MinecraftServerSpec{Ports: []avsysv1.ExtraPort{ownPort}}
	snapshot := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{geyser}, Ports: []avsysv1.ExtraPort{moved}}
	restoreSoftware(now, snapshot)
	if want := []avsysv1.ExtraPort{ownPort, moved}; !reflect.DeepEqual(now.Ports, want) {
		t.Errorf("Geyser back: %+v, want %+v", now.Ports, want)
	}
	restoreSoftware(now, &avsysv1.MinecraftServerSpec{})
	if want := []avsysv1.ExtraPort{ownPort}; !reflect.DeepEqual(now.Ports, want) {
		t.Errorf("Geyser gone: %+v, want %+v", now.Ports, want)
	}
}
