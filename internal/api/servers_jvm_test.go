package api

import (
	"encoding/json"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// Extra flags are options for Java: not its heap, which is the server's
// memory, and not a second collector on top of Aikar's.
func TestCheckJVM(t *testing.T) {
	on, off := true, false
	for name, c := range map[string]struct {
		jvm avsysv1.JVMSpec
		ok  bool
	}{
		"none":                       {avsysv1.JVMSpec{}, true},
		"Aikar's and a harmless one": {avsysv1.JVMSpec{AikarFlags: &on, ExtraFlags: "-XX:+AlwaysPreTouch  -Dlog4j2.formatMsgNoLookups=true"}, true},
		"another collector alone":    {avsysv1.JVMSpec{AikarFlags: &off, ExtraFlags: "-XX:+UseZGC -XX:+ZGenerational"}, true},
		"another collector on top":   {avsysv1.JVMSpec{AikarFlags: &on, ExtraFlags: "-XX:+UseZGC"}, false},
		"G1 switched off on top":     {avsysv1.JVMSpec{AikarFlags: &on, ExtraFlags: "-XX:-UseG1GC"}, false},
		"G1 switched off alone":      {avsysv1.JVMSpec{AikarFlags: &off, ExtraFlags: "-XX:-UseG1GC"}, true},
		"the heap":                   {avsysv1.JVMSpec{ExtraFlags: "-Xmx8G"}, false},
		"the heap as a share":        {avsysv1.JVMSpec{ExtraFlags: "-XX:MaxRAMPercentage=80"}, false},
		"the heap by its long name":  {avsysv1.JVMSpec{ExtraFlags: "-XX:MaxHeapSize=16G"}, false},
		"the least heap":             {avsysv1.JVMSpec{ExtraFlags: "-XX:MinHeapSize=1G"}, false},
		"Java 8's other collector":   {avsysv1.JVMSpec{AikarFlags: &on, ExtraFlags: "-XX:+UseParNewGC"}, false},
		"not an option":              {avsysv1.JVMSpec{ExtraFlags: "rm -rf /"}, false},
	} {
		jvm := c.jvm
		if err := checkJVM(&jvm); (err == nil) != c.ok {
			t.Errorf("%s: %v, want allowed %v", name, err, c.ok)
		}
	}
	jvm := avsysv1.JVMSpec{ExtraFlags: "  -XX:+AlwaysPreTouch   -Dfoo=bar \n"}
	if err := checkJVM(&jvm); err != nil || jvm.ExtraFlags != "-XX:+AlwaysPreTouch\n-Dfoo=bar" {
		t.Errorf("not tidied: %q %v", jvm.ExtraFlags, err)
	}
}

// A new server has Aikar's flags unless asked not to; one that exists is not
// given them by being changed.
func TestANewServerHasAikarsFlags(t *testing.T) {
	p := newPanel(t)
	get := func(name string) avsysv1.JVMSpec {
		t.Helper()
		var got struct {
			Spec avsysv1.MinecraftServerSpec `json:"spec"`
		}
		rec := p.send("owner", "GET", "/orgs/alpha/servers/"+name, nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Spec.JVM
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"fresh","spec":{"eula":true,"version":"1.21.4"}}`); code != 201 {
		t.Fatalf("create: %d %s", code, msg)
	}
	if jvm := get("fresh"); jvm.AikarFlags == nil || !*jvm.AikarFlags {
		t.Errorf("a new server: %+v", jvm)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"plain","spec":{"eula":true,"version":"1.21.4","jvm":{"aikarFlags":false}}}`); code != 201 {
		t.Fatalf("create without: %d %s", code, msg)
	}
	if jvm := get("plain"); jvm.AikarFlags == nil || *jvm.AikarFlags {
		t.Errorf("asked for none: %+v", jvm)
	}
	// world was made before they were offered.
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"displayName":"World"}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	if jvm := get("world"); jvm.AikarFlags != nil {
		t.Errorf("an existing server was given them: %+v", jvm)
	}
	if code, _ := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"jvm":{"aikarFlags":true,"extraFlags":"-XX:+UseZGC"}}}`); code != 400 {
		t.Errorf("a second collector on top of Aikar's: %d, want 400", code)
	}
}

// A copy of a server that is there takes its extra flags from it.
func TestFromLiveTakesExtraFlags(t *testing.T) {
	live := &avsysv1.MinecraftServerSpec{JVM: avsysv1.JVMSpec{ExtraFlags: "-XX:+AlwaysPreTouch"}}
	requested := &avsysv1.MinecraftServerSpec{}
	fromLive(requested, nil, live)
	if requested.JVM.ExtraFlags != "-XX:+AlwaysPreTouch" {
		t.Errorf("not taken: %q", requested.JVM.ExtraFlags)
	}
	asked := &avsysv1.MinecraftServerSpec{}
	fromLive(asked, map[string]json.RawMessage{"jvm": json.RawMessage(`{"extraFlags":""}`)}, live)
	if asked.JVM.ExtraFlags != "" {
		t.Errorf("the request did not win: %q", asked.JVM.ExtraFlags)
	}
}
