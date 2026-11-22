package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// What a mod needs of another is what its file says will do (fabric.mod.json
// "depends"), not the version its Modrinth page names, which is only the
// one it was made with. A dependency the panel added is never pinned older
// for such a name; it is pinned again only where what the server has will
// not do, to the newest that will for all that need it. One pinned by
// someone stays as they pinned it, and what it will not do for says so, as
// does a mod that does not work with what the server has ("breaks").
func TestDependencyFollowsDeclaredRanges(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "f3", project: "FAPI", number: "0.95", date: "2026-03", file: fabricJar(t, "fabric-api", "0.95.0"), deps: required("CLOTH")})
	m.add(fakeVersion{id: "f2", project: "FAPI", number: "0.92", date: "2026-02", file: fabricJar(t, "fabric-api", "0.92.0")})
	m.add(fakeVersion{id: "f1", project: "FAPI", number: "0.86", date: "2026-01", file: fabricJar(t, "fabric-api", "0.86.0")})
	m.add(fakeVersion{id: "c1", project: "CLOTH", number: "15", file: fabricJar(t, "cloth-config", "15")})
	named := func(v string) []modrinthDependency { return []modrinthDependency{{VersionID: v, Type: "required"}} }
	jar := func(id, wants string) []byte { return fabricJar(t, id, "1", "depends", `{"fabric-api":"`+wants+`"}`) }
	// Each names 0.86 on Modrinth, as made with it.
	m.add(fakeVersion{id: "a1", project: "AAA", number: "1", file: jar("aaa", ">=0.80"), deps: named("f1")})
	m.add(fakeVersion{id: "a2", project: "AAA", number: "2", file: jar("aaa", ">=0.93"), deps: named("f1")})
	m.add(fakeVersion{id: "b1", project: "BBB", number: "1", file: jar("bbb", "*"), deps: named("f1")})
	m.add(fakeVersion{id: "c9", project: "CCC", number: "1", file: jar("ccc", "<0.90"), deps: named("f1")})
	m.add(fakeVersion{id: "d1", project: "DDD", number: "1", file: fabricJar(t, "ddd", "1", "breaks", `{"fabric-api":">=0.95"}`)})
	p.withModrinth(m)
	// The one named, as it fits and AAA says it will do.
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"ranges","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+modrinthEntry("AAA", "a1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("ranges", "AAA@a1", "+FAPI@f1 <- AAA")
	entry := func(project string) string {
		t.Helper()
		for _, e := range p.serverOf("ranges").Spec.Mods {
			if e.Project == project {
				raw, _ := json.Marshal(e)
				return string(raw)
			}
		}
		t.Fatalf("no %s", project)
		return ""
	}
	patch := func(mods ...string) {
		t.Helper()
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/ranges", `{"spec":{"mods":[`+strings.Join(mods, ",")+`]}}`); code != 200 {
			t.Fatalf("patch: %d %s", code, msg)
		}
	}
	unmet := func() string {
		var out []string
		for _, e := range p.serverOf("ranges").Spec.Mods {
			for _, u := range e.Unmet {
				out = append(out, fmt.Sprintf("%s: %+v", e.Project, u))
			}
		}
		return strings.Join(out, "; ")
	}
	// Changed to a version that wants 0.93 or newer: pinned again, to the
	// newest that will do, never older for a name; another that takes any
	// changes nothing.
	patch(modrinthEntry("AAA", "a2"), entry("FAPI"), modrinthEntry("BBB", "b1"))
	p.expectMods("ranges", "AAA@a2", "BBB@b1", "+FAPI@f3 <- AAA,BBB", "+CLOTH@c1 <- FAPI")
	if got := unmet(); got != "" {
		t.Errorf("said %s", got)
	}
	// One that wants older than 0.90: the server's will not do, and none
	// will for both: said.
	patch(entry("AAA"), entry("BBB"), entry("FAPI"), entry("CLOTH"), modrinthEntry("CCC", "c9"))
	p.expectMods("ranges", "AAA@a2", "BBB@b1", "CCC@c9", "+FAPI@f3 <- AAA,BBB,CCC", "+CLOTH@c1 <- FAPI")
	if got := unmet(); got != "CCC: {ID:fabric-api Wants:<0.90 Has:0.95.0 By:Title of FAPI Breaks:false}" {
		t.Errorf("said %s", got)
	}
	// Without the one that wants 0.93: pinned again, to the newest that
	// will do for what is left.
	patch(entry("BBB"), entry("CCC"), entry("FAPI"), entry("CLOTH"))
	p.expectMods("ranges", "BBB@b1", "CCC@c9", "+FAPI@f1 <- BBB,CCC")
	if got := unmet(); got != "" {
		t.Errorf("said %s", got)
	}
	// Someone's own, older than one wants: it stays, and is said.
	patch(entry("BBB"), modrinthEntry("FAPI", "f2"), modrinthEntry("AAA", "a2"))
	p.expectMods("ranges", "AAA@a2", "BBB@b1", "FAPI@f2 <- AAA,BBB")
	if got := unmet(); got != "AAA: {ID:fabric-api Wants:>=0.93 Has:0.92.0 By:Title of FAPI Breaks:false}" {
		t.Errorf("said %s", got)
	}
	// What does not work with the server's Fabric API says so.
	patch(modrinthEntry("FAPI", "f3"), modrinthEntry("DDD", "d1"))
	if got := unmet(); got != "DDD: {ID:fabric-api Wants:>=0.95 Has:0.95.0 By:Title of FAPI Breaks:true}" {
		t.Errorf("said %s", got)
	}
}

// Bingo, as it runs (Fabric 1.20.1): its mods' Modrinth pages name the
// versions of Fabric API and Sodium they were made with, older than the
// server's, and Indium's one for another game version. Their files take
// what the server has (as the real jars say), so nothing is said, and
// nothing is pinned older.
func TestBingoSaysNothing(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	game := func(v fakeVersion) fakeVersion { v.games = []string{"1.20.1"}; return v }
	m.add(game(fakeVersion{id: "fapi92", project: "FAPI", number: "0.92.2+1.20.1", date: "2024-06", file: fabricJar(t, "fabric-api", "0.92.2+1.20.1", "provides", "fabric")}))
	m.add(game(fakeVersion{id: "fapi86", project: "FAPI", number: "0.86.1+1.20.1", date: "2023-08", file: fabricJar(t, "fabric-api", "0.86.1+1.20.1", "provides", "fabric")}))
	m.add(game(fakeVersion{id: "fapi84", project: "FAPI", number: "0.84.0+1.20.1", date: "2023-06", file: fabricJar(t, "fabric-api", "0.84.0+1.20.1", "provides", "fabric")}))
	m.add(game(fakeVersion{id: "sod58", project: "SODIUM", number: "mc1.20.1-0.5.8", date: "2024-02", file: fabricJar(t, "sodium", "0.5.8+mc1.20.1",
		"breaks", `{"indium":"<=1.0.27","iris":"<=1.6.11","sodium-extra":"<0.5.4","cullleaves":"<=3.1.0","optifabric":"*"}`)}))
	m.add(game(fakeVersion{id: "sod56", project: "SODIUM", number: "mc1.20.1-0.5.6", date: "2024-01", file: fabricJar(t, "sodium", "0.5.6+mc1.20.1")}))
	m.add(fakeVersion{id: "sod58b", project: "SODIUM", number: "mc1.20.4-0.5.8", date: "2024-02", games: []string{"1.20.4"}, file: fabricJar(t, "sodium", "0.5.8+mc1.20.4")})
	m.add(game(fakeVersion{id: "anim", project: "ANIM", number: "0.6+1.20", file: fabricJar(t, "animatica", "0.6+1.20", "depends", `{"fabric":"*","minecraft":["1.19.4","1.20.x"]}`),
		deps: []modrinthDependency{{VersionID: "fapi86", Type: "required"}}}))
	m.add(game(fakeVersion{id: "ebe", project: "EBE", number: "0.9+1.20", file: fabricJar(t, "enhancedblockentities", "0.9+1.20", "depends", `{"fabric":"*","minecraft":">=1.20"}`, "breaks", `{"optifabric":"*"}`),
		deps: []modrinthDependency{{VersionID: "fapi84", Type: "required"}}}))
	m.add(game(fakeVersion{id: "sx", project: "SX", number: "0.5.4", file: fabricJar(t, "sodium-extra", "0.5.4+mc1.20.1-build.115", "depends", `{"fabric-api":"*","sodium":">=0.5.6"}`),
		deps: []modrinthDependency{{ProjectID: "FAPI", Type: "required"}, {VersionID: "sod56", Type: "required"}}}))
	m.add(game(fakeVersion{id: "ind", project: "IND", number: "1.0.30+mc1.20.4", file: fabricJar(t, "indium", "1.0.30+mc1.20.4", "depends", `{"sodium":"0.5.8","minecraft":"~1.20.1"}`),
		deps: []modrinthDependency{{VersionID: "sod58b", Type: "required"}}}))
	m.add(game(fakeVersion{id: "iris", project: "IRIS", number: "1.7.0+1.20.1", file: fabricJar(t, "iris", "1.7.0+mc1.20.1", "depends", `{"sodium":"0.5.x"}`), deps: required("SODIUM")}))
	m.add(game(fakeVersion{id: "cull", project: "CULL", number: "3.2.0", file: fabricJar(t, "cullleaves", "3.2.0")}))
	p.withModrinth(m)
	// As it runs: its own mods, with Fabric API and Sodium as its files.
	var mods []string
	for _, e := range [][2]string{{"ANIM", "anim"}, {"EBE", "ebe"}, {"SX", "sx"}, {"IND", "ind"}, {"IRIS", "iris"}, {"CULL", "cull"}, {"FAPI", "fapi92"}, {"SODIUM", "sod58"}} {
		mods = append(mods, modrinthEntry(e[0], e[1]))
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"bingo","spec":{"eula":true,"loader":"fabric","version":"1.20.1","state":"Stopped","mods":[`+strings.Join(mods, ",")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	says := func() {
		t.Helper()
		for _, e := range p.serverOf("bingo").Spec.Mods {
			if len(e.Unmet) > 0 {
				t.Errorf("%s says %+v", e.Project, e.Unmet)
			}
		}
	}
	p.expectMods("bingo", "ANIM@anim", "EBE@ebe", "SX@sx", "IND@ind", "IRIS@iris", "CULL@cull",
		"FAPI@fapi92 <- ANIM,EBE,SX", "SODIUM@sod58 <- IND,IRIS,SX")
	says()
	// Without its own Fabric API and Sodium, they are added as the image
	// would: Fabric API at the version Animatica names, the first; Sodium
	// not at the one Sodium Extra names, which Indium says will not do, but
	// the newest that does for all. Nothing is said.
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/bingo", `{"spec":{"mods":[`+strings.Join(mods[:6], ",")+`]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	p.expectMods("bingo", "ANIM@anim", "EBE@ebe", "SX@sx", "IND@ind", "IRIS@iris", "CULL@cull",
		"+FAPI@fapi86 <- ANIM,EBE,SX", "+SODIUM@sod58 <- IND,IRIS,SX")
	says()
}

// A version Modrinth names for another game version is not taken where one
// for this game fits, as Bingo's Indium names Sodium for 1.20.4: the newest
// for this game is. Where none fits, the one named is, as the image would.
func TestNamedForAnotherGame(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "l1", project: "LIB", number: "1.0", date: "2026-01", file: fabricJar(t, "lib", "1.0")})
	m.add(fakeVersion{id: "l2", project: "LIB", number: "2.0", date: "2026-05", games: []string{"1.21.4"}, file: fabricJar(t, "lib", "2.0")})
	m.add(fakeVersion{id: "a1", project: "AAA", number: "1", file: fabricJar(t, "aaa", "1"), deps: []modrinthDependency{{VersionID: "l2", Type: "required"}}})
	p.withModrinth(m)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"newer","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+modrinthEntry("AAA", "a1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("newer", "AAA@a1", "+LIB@l1 <- AAA")
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"older","spec":{"eula":true,"loader":"fabric","version":"1.20.1","state":"Stopped","mods":[`+modrinthEntry("AAA", "a1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("older", "AAA@a1", "+LIB@l2 <- AAA")
}
