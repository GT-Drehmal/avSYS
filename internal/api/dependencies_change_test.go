package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A dependency the panel added follows the version what needs it names:
// a mod changed to a version that names another has it pinned to that
// one, and what it needs worked out with it. Where two name two versions,
// the first named is kept and the other said; one pinned by someone stays
// as they pinned it, and what names another says so.
func TestDependencyFollowsTheVersionNamed(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "f2", project: "FAPI", number: "0.92", file: fabricJar(t, "fabric-api", "0.92"), deps: required("CLOTH")})
	m.add(fakeVersion{id: "f1", project: "FAPI", number: "0.86", file: fabricJar(t, "fabric-api", "0.86")})
	m.add(fakeVersion{id: "c1", project: "CLOTH", number: "15", file: fabricJar(t, "cloth-config", "15")})
	named := func(v string) []modrinthDependency { return []modrinthDependency{{VersionID: v, Type: "required"}} }
	m.add(fakeVersion{id: "a2", project: "AAA", number: "2", file: fabricJar(t, "aaa", "2"), deps: named("f2")})
	m.add(fakeVersion{id: "a1", project: "AAA", number: "1", file: fabricJar(t, "aaa", "1"), deps: named("f1")})
	m.add(fakeVersion{id: "b1", project: "BBB", number: "1", file: fabricJar(t, "bbb", "1"), deps: named("f1")})
	p.withModrinth(m)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"named","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+modrinthEntry("AAA", "a1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("named", "AAA@a1", "+FAPI@f1 <- AAA")
	entry := func(project string) string {
		t.Helper()
		for _, e := range p.serverOf("named").Spec.Mods {
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
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/named", `{"spec":{"mods":[`+strings.Join(mods, ",")+`]}}`); code != 200 {
			t.Fatalf("patch: %d %s", code, msg)
		}
	}
	unmetOf := func(project string) string {
		for _, e := range p.serverOf("named").Spec.Mods {
			if e.Project == project {
				return fmt.Sprint(e.Unmet)
			}
		}
		return ""
	}
	// Changed to the version that names 0.92: Fabric API follows, and what
	// that version needs comes with it.
	patch(modrinthEntry("AAA", "a2"), entry("FAPI"))
	p.expectMods("named", "AAA@a2", "+FAPI@f2 <- AAA", "+CLOTH@c1 <- FAPI")
	// Another that names 0.86: the first named stays, and this one says so.
	patch(entry("AAA"), entry("FAPI"), entry("CLOTH"), modrinthEntry("BBB", "b1"))
	p.expectMods("named", "AAA@a2", "BBB@b1", "+FAPI@f2 <- AAA,BBB", "+CLOTH@c1 <- FAPI")
	if got := unmetOf("BBB"); got != "[{fabric-api 0.86 0.92 Title of FAPI}]" {
		t.Errorf("BBB unmet %s", got)
	}
	// Pinned by someone, it stays as they pinned it.
	patch(entry("AAA"), entry("BBB"), modrinthEntry("FAPI", "f1"))
	p.expectMods("named", "AAA@a2", "BBB@b1", "FAPI@f1 <- AAA,BBB")
	if got := unmetOf("AAA"); got != "[{fabric-api 0.92 0.86 Title of FAPI}]" {
		t.Errorf("AAA unmet %s", got)
	}
	if got := unmetOf("BBB"); got != "[]" {
		t.Errorf("BBB unmet %s", got)
	}
}

// What a datapack needs that is a mod is added as a mod, as its own
// version says, whether named by its project or by one version of it.
func TestDatapackNeedsAMod(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "lib1", project: "LIB", number: "1", file: fabricJar(t, "lib", "1")})
	m.add(fakeVersion{id: "other1", project: "OTHER", number: "1", file: fabricJar(t, "other", "1")})
	m.add(fakeVersion{id: "dp1", project: "DP", number: "1", loaders: []string{"datapack"}, filename: "dp-1.zip", file: jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"pack_format":48}}`}),
		deps: []modrinthDependency{{VersionID: "lib1", Type: "required"}, {ProjectID: "OTHER", Type: "required"}}})
	p.withModrinth(m)
	body := `{"name":"packs","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[{"source":"modrinth","kind":"datapack","project":"DP","version":"dp1"}]}}`
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("packs", "DP@dp1", "+LIB@lib1 <- DP", "+OTHER@other1 <- DP")
	for _, e := range p.serverOf("packs").Spec.Mods {
		if e.Dependency && e.Kind != avsysv1.KindMod {
			t.Errorf("%s added as a %s", e.Project, e.Kind)
		}
	}
	if env := envOf(p.serverOf("packs")); env["MODRINTH_PROJECTS"] != "datapack:DP:dp1\nLIB:lib1\nOTHER:other1" {
		t.Errorf("given %q", env["MODRINTH_PROJECTS"])
	}
}

// A file found in the server's folders that was what some content needs,
// taken off through the panel or gone by hand, has what it was added in its
// place: the image downloads nothing itself.
func TestFoundProviderGone(t *testing.T) {
	for _, how := range []string{"through the panel", "by hand"} {
		t.Run(how, func(t *testing.T) {
			p := newPanel(t)
			m := &depModrinth{}
			m.add(fakeVersion{id: "w1", project: "WATHE", number: "1.4.1", file: fabricJar(t, "wathe", "1.4.1")})
			m.add(fakeVersion{id: "k1", project: "KINS", number: "1.6.4", file: fabricJar(t, "kinswathe", "1.6.4"), deps: required("WATHE")})
			p.withModrinth(m)
			root := t.TempDir()
			// A name the list cannot hold: it stays found, not taken in.
			lay(t, root, map[string][]byte{"mods/wathe,fork.jar": fabricJar(t, "wathe", "1.4.1+fork")})
			p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return DirFiles(root), nil }
			if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"harpy","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped"}}`); code != http.StatusCreated {
				t.Fatalf("create: %d %s", code, msg)
			}
			look := func() {
				t.Helper()
				st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "harpy"})
				st.mu.Lock()
				st.last = time.Time{}
				st.mu.Unlock()
				if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/harpy/content/look?wait=true", ""); code != http.StatusOK {
					t.Fatalf("look: %d", code)
				}
			}
			look()
			if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[`+modrinthEntry("KINS", "k1")+`]}}`); code != 200 {
				t.Fatalf("patch: %d %s", code, msg)
			}
			p.expectMods("harpy", "KINS@k1")
			if got := pathsOf(foundOf(p.serverOf("harpy"))); len(got) != 1 {
				t.Fatalf("found %v", got)
			}
			if how == "by hand" {
				if err := os.Remove(filepath.Join(root, "mods/wathe,fork.jar")); err != nil {
					t.Fatal(err)
				}
				look()
			} else if code, msg := p.do("owner", "DELETE", "/orgs/alpha/servers/harpy/content/found?path="+url.QueryEscape("mods/wathe,fork.jar"), ""); code != 200 {
				t.Fatalf("remove: %d %s", code, msg)
			}
			p.expectMods("harpy", "KINS@k1", "+WATHE@w1 <- KINS")
			// Saved as it is after, it stays.
			var all []string
			for _, e := range p.serverOf("harpy").Spec.Mods {
				raw, _ := json.Marshal(e)
				all = append(all, string(raw))
			}
			if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[`+strings.Join(all, ",")+`]}}`); code != 200 {
				t.Fatalf("patch: %d %s", code, msg)
			}
			p.expectMods("harpy", "KINS@k1", "+WATHE@w1 <- KINS")
		})
	}
}
