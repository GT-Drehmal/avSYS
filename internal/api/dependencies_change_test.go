package api

import (
	"context"
	"encoding/json"
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

// A fork put in the server's folder after the panel added what it is takes
// its place once it is found: the dependency added goes, and the server is
// not noted as running that, its file going at the next start. So does a
// copy of the very file the panel lists, under another name.
func TestFoundForkTakesThePlace(t *testing.T) {
	for _, c := range []struct {
		name, file string
		fork       bool
	}{{"a fork", "wathe-fork+wathez.1.jar", true}, {"a renamed copy", "wathe-copy.jar", false}} {
		t.Run(c.name, func(t *testing.T) {
			p := newPanel(t)
			m := &depModrinth{}
			official := fabricJar(t, "wathe", "1.3.2")
			m.add(fakeVersion{id: "w1", project: "WATHE", number: "1.3.2", file: official})
			m.add(fakeVersion{id: "b1", project: "BLOOD", number: "1.1.6", file: fabricJar(t, "wathe_blood", "1.1.6"), deps: []modrinthDependency{{VersionID: "w1", Type: "required"}}})
			p.withModrinth(m)
			root := t.TempDir()
			p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return DirFiles(root), nil }
			if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"harpy","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+modrinthEntry("BLOOD", "b1")+`]}}`); code != http.StatusCreated {
				t.Fatalf("create: %d %s", code, msg)
			}
			p.expectMods("harpy", "BLOOD@b1", "+WATHE@w1 <- BLOOD")
			file := official
			if c.fork {
				file = fabricJar(t, "wathe", "1.3.2+wathez.1")
			}
			lay(t, root, map[string][]byte{"mods/" + c.file: file})
			// Looked at, twice for the copy: it is found beside what it is a
			// copy of, which then goes, and is taken in after.
			for range 2 {
				st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "harpy"})
				st.mu.Lock()
				st.last = time.Time{}
				st.mu.Unlock()
				if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/harpy/content/look?wait=true", ""); code != http.StatusOK {
					t.Fatalf("look: %d", code)
				}
			}
			p.expectMods("harpy", "BLOOD@b1", c.file+" <- BLOOD")
			if got := p.serverOf("harpy").Annotations[avsysv1.AnnotationSettled]; got != "" {
				t.Errorf("settled %q", got)
			}
		})
	}
}
