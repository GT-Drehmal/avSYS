package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// What a jar says it is: its mod ID and those it provides, the jars inside
// it, and the versions of others it needs.
func TestReadModMeta(t *testing.T) {
	inner := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"cardinal-components-base","version":"6.1.1"}`})
	deeper := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"deep","version":"1","jars":[{"file":"META-INF/jars/inner.jar"}]}`, "META-INF/jars/inner.jar": string(inner)})
	for _, c := range []struct {
		name    string
		kind    avsysv1.ContentKind
		files   map[string]string
		ids     []string
		all     []string
		version string
		depends map[string][]string
	}{
		{"fabric", avsysv1.KindMod, map[string]string{
			"fabric.mod.json":        `{"id":"fabric-api","version":"0.116.18+1.21.1","provides":["fabric"],"depends":{"fabricloader":">=0.15.11","minecraft":[">=1.21- <1.21.2-"]},"jars":[{"file":"META-INF/jars/deep.jar"}]}`,
			"META-INF/jars/deep.jar": string(deeper),
		}, []string{"fabric-api", "fabric"}, []string{"fabric-api", "fabric", "deep", "cardinal-components-base"}, "0.116.18+1.21.1", map[string][]string{"fabricloader": {">=0.15.11"}, "minecraft": {">=1.21- <1.21.2-"}}},
		{"quilt", avsysv1.KindMod, map[string]string{
			"quilt.mod.json": `{"quilt_loader":{"id":"qsl","version":"8.0.0","provides":["quilted_fabric_api",{"id":"org.quiltmc:fabric-api","version":"0.1"}],"depends":["quilt_loader",{"id":"minecraft","versions":">=1.21"},{"id":"opt","optional":true}]}}`,
		}, []string{"qsl", "quilted_fabric_api", "fabric-api"}, nil, "8.0.0", map[string][]string{"quilt_loader": nil, "minecraft": {">=1.21"}}},
		{"forge, two mods and one inside", avsysv1.KindMod, map[string]string{
			"META-INF/mods.toml":            "modLoader=\"javafml\"\n[[mods]]\nmodId=\"create\"\nversion=\"6.0.4\"\n[[mods]]\nmodId='flywheel'\n[[dependencies.create]]\nmodId=\"forge\"\nmandatory=true\nversionRange=\"[47,)\"\n[[dependencies.create]]\nmodId=\"jei\"\nmandatory=false\nversionRange=\"[15,)\"\n[[dependencies.create]]\nmodId=\"ponder\"\ntype=\"required\"\nversionRange=\"[1.0,2.0)\"\n",
			"META-INF/jarjar/metadata.json": `{"jars":[{"identifier":{"group":"x","artifact":"inner"},"path":"META-INF/jarjar/inner.jar"}]}`,
			"META-INF/jarjar/inner.jar":     string(jarOf(t, map[string]string{"META-INF/neoforge.mods.toml": "[[mods]]\nmodId=\"registrate\"\nversion=\"1.3\"\n"})),
		}, []string{"create", "flywheel"}, []string{"create", "flywheel", "registrate"}, "6.0.4", map[string][]string{"forge": {"[47,)"}, "ponder": {"[1.0,2.0)"}}},
		{"plugin", avsysv1.KindPlugin, map[string]string{
			"plugin.yml": "name: LuckPerms\nversion: 5.4\nprovides: [Vault-Perms]\n",
		}, []string{"LuckPerms", "Vault-Perms"}, nil, "5.4", nil},
		{"nothing to say", avsysv1.KindMod, map[string]string{"a/B.class": "x"}, nil, nil, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := jarOf(t, c.files)
			zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			m := readModMeta(zr, c.kind)
			if !slices.Equal(m.IDs, c.ids) || m.Version != c.version {
				t.Errorf("ids %q version %q, want %q %q", m.IDs, m.Version, c.ids, c.version)
			}
			all := c.all
			if all == nil {
				all = c.ids
			}
			if got := m.all(); !slices.Equal(got, all) {
				t.Errorf("all %q, want %q", got, all)
			}
			if c.depends != nil && fmt.Sprint(m.Depends) != fmt.Sprint(c.depends) {
				t.Errorf("depends %v, want %v", m.Depends, c.depends)
			}
		})
	}
}

// Whether a version is one a mod says it works with, as the loader reads
// its ranges. What cannot be read is taken as fitting: the panel only says
// what it is sure of.
func TestVersionRanges(t *testing.T) {
	for _, c := range []struct {
		version string
		ranges  []string
		forge   bool
		want    bool
	}{
		{"1.4.1-1.21.1+wathez.1", []string{">=1.4.1"}, false, false}, // a pre-release of 1.4.1
		{"1.4.1+wathez.1", []string{">=1.4.1"}, false, true},
		{"1.4.1", []string{">=1.5.0"}, false, false},
		{"1.5.2", []string{">=1.5.0 <2"}, false, true},
		{"2.0.0", []string{">=1.5.0 <2"}, false, false},
		{"2.0.0", []string{"<1", ">=2"}, false, true},
		{"1.21.1", []string{"~1.21"}, false, true},
		{"1.22", []string{"~1.21"}, false, false},
		{"1.9.3", []string{"^1.2"}, false, true},
		{"2.0", []string{"^1.2"}, false, false},
		{"1.2.7", []string{"1.2.x"}, false, true},
		{"1.3.0", []string{"1.2.x"}, false, false},
		{"1.21.1", []string{">=1.21- <1.21.2-"}, false, true},
		{"1.21.2", []string{">=1.21- <1.21.2-"}, false, false},
		{"anything", []string{"*"}, false, true},
		{"local", []string{">=1.0"}, false, true},   // not a version that can be compared
		{"1.0", []string{"what even"}, false, true}, // not a range that can be read
		{"47.2.0", []string{"[47,)"}, true, true},
		{"46.0.1", []string{"[47,)"}, true, false},
		{"2.0", []string{"[1.0,2.0)"}, true, false},
		{"2.0", []string{"[1.0,2.0]"}, true, true},
		{"3.5", []string{"[1,2),[3,4)"}, true, true},
		{"1.0", []string{"[1.0]"}, true, true},
		{"9.9", []string{"1.0"}, true, true}, // a bare version in Maven is any
	} {
		if got := fits(c.version, c.ranges, c.forge); got != c.want {
			t.Errorf("%q in %q (forge %v): %v, want %v", c.version, c.ranges, c.forge, got, c.want)
		}
	}
}

// depModrinth stands in for Modrinth, its API and its files, for what the
// panel asks about content and what it needs.
type depModrinth struct {
	mu       sync.Mutex
	versions []fakeVersion // newest first
	slugs    map[string]string
	down     bool
	asked    []string
}

type fakeVersion struct {
	id, project, number string
	loaders             []string
	games               []string
	file                []byte
	filename            string
	deps                []modrinthDependency
}

func (f fakeVersion) url() string {
	return "https://cdn.modrinth.test/" + sha1Of(f.file) + "/" + f.filename
}

func (f fakeVersion) json() map[string]any {
	deps := []map[string]any{}
	for _, d := range f.deps {
		deps = append(deps, map[string]any{"project_id": nullable(d.ProjectID), "version_id": nullable(d.VersionID), "dependency_type": d.Type})
	}
	return map[string]any{
		"id": f.id, "project_id": f.project, "version_number": f.number, "version_type": "release",
		"game_versions": f.games, "loaders": f.loaders, "dependencies": deps,
		"files": []map[string]any{{"url": f.url(), "filename": f.filename, "primary": true, "hashes": map[string]string{"sha1": sha1Of(f.file)}}},
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *depModrinth) add(v fakeVersion) {
	if v.filename == "" {
		v.filename = v.project + "-" + v.number + ".jar"
	}
	if v.loaders == nil {
		v.loaders = []string{"fabric"}
	}
	if v.games == nil {
		v.games = []string{"1.21.1"}
	}
	m.versions = append(m.versions, v)
}

func (m *depModrinth) version(id string) (fakeVersion, bool) {
	for _, v := range m.versions {
		if v.id == id {
			return v, true
		}
	}
	return fakeVersion{}, false
}

func (m *depModrinth) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, errors.New("offline")
	}
	u := r.URL.String()
	m.asked = append(m.asked, r.Method+" "+u)
	answer := func(v any) (*http.Response, error) {
		raw, _ := json.Marshal(v)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
	}
	notFound := &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}
	switch {
	case strings.HasPrefix(u, "https://cdn.modrinth.test/"):
		for _, v := range m.versions {
			if v.url() == u {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(v.file)), Request: r}, nil
			}
		}
		return notFound, nil
	case r.Method == http.MethodPost && u == "https://api.modrinth.com/v2/version_files":
		var in struct {
			Hashes []string `json:"hashes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		out := map[string]any{}
		for _, h := range in.Hashes {
			for _, v := range m.versions {
				if sha1Of(v.file) == h {
					out[h] = v.json()
				}
			}
		}
		return answer(out)
	case strings.HasPrefix(r.URL.Path, "/v2/version/"):
		if v, ok := m.version(strings.TrimPrefix(r.URL.Path, "/v2/version/")); ok {
			return answer(v.json())
		}
		return notFound, nil
	case strings.HasPrefix(r.URL.Path, "/v2/project/") && strings.HasSuffix(r.URL.Path, "/version"):
		project := m.id(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/project/"), "/version"))
		var loaders, games []string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("loaders")), &loaders)
		_ = json.Unmarshal([]byte(r.URL.Query().Get("game_versions")), &games)
		out := []any{}
		for _, v := range m.versions {
			if v.project != project || (len(loaders) > 0 && !overlaps(v.loaders, loaders)) || (len(games) > 0 && !overlaps(v.games, games)) {
				continue
			}
			out = append(out, v.json())
		}
		return answer(out)
	case strings.HasPrefix(r.URL.Path, "/v2/project/"):
		id := m.id(strings.TrimPrefix(r.URL.Path, "/v2/project/"))
		slug := id
		for s, i := range m.slugs {
			if i == id {
				slug = s
			}
		}
		return answer(map[string]any{"id": id, "slug": slug, "title": "Title of " + slug, "icon_url": "https://cdn.modrinth.test/" + slug + ".png"})
	case strings.HasPrefix(r.URL.Path, "/v2/projects"):
		return answer([]any{})
	}
	return nil, errors.New("offline: " + u)
}

func (m *depModrinth) id(project string) string {
	if id, ok := m.slugs[project]; ok {
		return id
	}
	return project
}

func overlaps(a, b []string) bool {
	return slices.ContainsFunc(a, func(x string) bool { return slices.Contains(b, x) })
}

// withModrinth has the panel ask m, for Modrinth's API and its files.
func (p *panel) withModrinth(m *depModrinth) {
	p.srv.catalog.http.Transport = m
	public := publicHTTP
	publicHTTP = &http.Client{Transport: m}
	p.t.Cleanup(func() { publicHTTP = public })
}

func fabricJar(t *testing.T, id, version string, more ...string) []byte {
	t.Helper()
	manifest := map[string]any{"schemaVersion": 1, "id": id, "version": version}
	files := map[string]string{}
	for i := 0; i+1 < len(more); i += 2 {
		switch more[i] {
		case "provides":
			manifest["provides"] = strings.Split(more[i+1], ",")
		case "depends":
			var d map[string]any
			if err := json.Unmarshal([]byte(more[i+1]), &d); err != nil {
				t.Fatal(err)
			}
			manifest["depends"] = d
		case "jar":
			name := "META-INF/jars/" + fmt.Sprint(i) + ".jar"
			files[name] = more[i+1]
			jars, _ := manifest["jars"].([]map[string]string)
			manifest["jars"] = append(jars, map[string]string{"file": name})
		}
	}
	raw, _ := json.Marshal(manifest)
	files["fabric.mod.json"] = string(raw)
	files["a/Main.class"] = "\xca\xfe\xba\xbe\x00\x00\x00\x41"
	return jarOf(t, files)
}

func required(projects ...string) []modrinthDependency {
	var out []modrinthDependency
	for _, p := range projects {
		out = append(out, modrinthDependency{ProjectID: p, Type: "required"})
	}
	return out
}

// mods is a server's content as "project@version", the panel's
// dependencies marked "+" and what needs them after.
func (p *panel) mods(name string) []string {
	p.t.Helper()
	var out []string
	for _, m := range p.serverOf(name).Spec.Mods {
		e := m.Project
		if m.Version != "" {
			e += "@" + m.Version
		}
		if m.Dependency {
			e = "+" + e
		}
		if len(m.NeededBy) > 0 {
			e += " <- " + strings.Join(m.NeededBy, ",")
		}
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

func (p *panel) expectMods(name string, want ...string) {
	p.t.Helper()
	sort.Strings(want)
	if got := p.mods(name); !slices.Equal(got, want) {
		p.t.Errorf("%s has\n  %q\nwant\n  %q", name, got, want)
	}
}

func modrinthEntry(project, version string) string {
	return fmt.Sprintf(`{"source":"modrinth","kind":"mod","project":%q,"version":%q}`, project, version)
}

// A new server's Modrinth content has what it needs listed with it: what
// its versions require, and what that requires in turn, each once, pinned
// to the version Modrinth names or the newest that fits; not what is only
// optional, does not work with it, or is in its file already. Its image is
// told to download nothing more itself.
func TestDependenciesListed(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{slugs: map[string]string{"fabric-api": "FAPI", "cloth": "CLOTH"}}
	m.add(fakeVersion{id: "fapi2", project: "FAPI", number: "0.116.18", file: fabricJar(t, "fabric-api", "0.116.18", "provides", "fabric")})
	m.add(fakeVersion{id: "fapi1", project: "FAPI", number: "0.110.0", file: fabricJar(t, "fabric-api", "0.110.0")})
	m.add(fakeVersion{id: "cloth2", project: "CLOTH", number: "15.0", file: fabricJar(t, "cloth-config", "15.0"), deps: required("FAPI")})
	m.add(fakeVersion{id: "cloth1", project: "CLOTH", number: "14.0", file: fabricJar(t, "cloth-config", "14.0"), deps: required("FAPI")})
	m.add(fakeVersion{id: "a1", project: "AAA", number: "1.0", file: fabricJar(t, "aaa", "1.0"), deps: []modrinthDependency{
		{ProjectID: "FAPI", Type: "required"},
		{VersionID: "cloth1", Type: "required"},
		{ProjectID: "OPT", Type: "optional"},
		{ProjectID: "BAD", Type: "incompatible"},
		{ProjectID: "EMB", Type: "embedded"},
	}})
	m.add(fakeVersion{id: "b1", project: "BBB", number: "1.0", file: fabricJar(t, "bbb", "1.0"), deps: required("FAPI")})
	p.withModrinth(m)

	body := `{"name":"deps","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[` + modrinthEntry("AAA", "a1") + `,` + modrinthEntry("BBB", "b1") + `]}}`
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("deps", "AAA@a1", "BBB@b1", "+FAPI@fapi2 <- AAA,BBB,CLOTH", "+CLOTH@cloth1 <- AAA")
	mc := p.serverOf("deps")
	if !mc.Spec.DependenciesListed {
		t.Error("not noted as listed")
	}
	for _, e := range mc.Spec.Mods {
		if e.Project == "FAPI" && (e.Title != "Title of fabric-api" || e.Java != 21 || !slices.Equal(e.Provides, []string{"fabric-api", "fabric"})) {
			t.Errorf("Fabric API as added: %+v", e)
		}
	}
	if env := envOf(mc); env[controller.DependencyEnv] != "none" {
		t.Errorf("image told %q", env[controller.DependencyEnv])
	}

	patch := func(mods ...string) {
		t.Helper()
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/deps", `{"spec":{"mods":[`+strings.Join(mods, ",")+`]}}`); code != 200 {
			t.Fatalf("patch: %d %s", code, msg)
		}
	}
	entries := func() map[string]string {
		out := map[string]string{}
		for _, e := range p.serverOf("deps").Spec.Mods {
			raw, _ := json.Marshal(e)
			out[e.Project] = string(raw)
		}
		return out
	}
	// Taken off, what needed something alone takes it with it; what
	// another needs too stays.
	was := entries()
	patch(was["BBB"], was["FAPI"], was["CLOTH"])
	p.expectMods("deps", "BBB@b1", "+FAPI@fapi2 <- BBB")
	// Another version of a mod is looked at again for what it needs.
	m.add(fakeVersion{id: "b2", project: "BBB", number: "2.0", file: fabricJar(t, "bbb", "2.0"), deps: required("CLOTH")})
	was = entries()
	patch(modrinthEntry("BBB", "b2"), was["FAPI"])
	p.expectMods("deps", "BBB@b2", "+CLOTH@cloth2 <- BBB", "+FAPI@fapi2 <- CLOTH")
	// One the panel added that someone picks a version of is theirs, and
	// stays once nothing needs it.
	was = entries()
	patch(was["BBB"], modrinthEntry("CLOTH", "cloth1"), was["FAPI"])
	p.expectMods("deps", "BBB@b2", "CLOTH@cloth1 <- BBB", "+FAPI@fapi2 <- CLOTH")
	patch(modrinthEntry("CLOTH", "cloth1"), entries()["FAPI"])
	p.expectMods("deps", "CLOTH@cloth1", "+FAPI@fapi2 <- CLOTH")
	// Added again by someone, under its slug, it is theirs alone.
	patch(entries()["CLOTH"], entries()["FAPI"], modrinthEntry("fabric-api", "fapi1"))
	p.expectMods("deps", "CLOTH@cloth1", "fabric-api@fapi1 <- CLOTH")
	// Taken off by hand while needed, it is put back.
	patch(entries()["CLOTH"])
	p.expectMods("deps", "CLOTH@cloth1", "+FAPI@fapi2 <- CLOTH")
	// The last that needs it gone, so is it.
	patch()
	p.expectMods("deps")

	// What Modrinth does not have for this game is said.
	m.add(fakeVersion{id: "n1", project: "NEEDY", number: "1", file: fabricJar(t, "needy", "1"), deps: required("GONE")})
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/deps", `{"spec":{"mods":[`+modrinthEntry("NEEDY", "n1")+`]}}`); code != 400 || msg != "invalid" {
		t.Errorf("needing what has no version: %d %s", code, msg)
	}
}

// envOf is the image's environment for a server, as the controller gives it.
func envOf(mc *avsysv1.MinecraftServer) map[string]string {
	out := map[string]string{}
	for _, e := range controller.MinecraftEnv(mc) {
		out[e.Name] = e.Value
	}
	return out
}

// Something on the server that is what a dependency is, as the loader knows
// it, is what is needed: a fork of it under the same mod ID, a file of it
// renamed, a mod that says it provides it, one that carries it inside, a
// Forge mod among others in one jar. Nothing is added beside it.
func TestDependenciesProvided(t *testing.T) {
	wathe := func(t *testing.T) []byte { return fabricJar(t, "wathe", "1.4.1-1.21.1") }
	for _, c := range []struct {
		name   string
		loader string
		file   []byte
		dep    []byte
	}{
		{"a fork, its ID the same", "fabric", fabricJar(t, "wathe", "1.4.1+wathez.1"), nil},
		{"the same file renamed", "fabric", fabricJar(t, "wathe", "1.4.1-1.21.1"), nil},
		{"one that says it provides it", "fabric", fabricJar(t, "wathe-reborn", "2.0", "provides", "wathe"), nil},
		{"one that carries it inside", "fabric", fabricJar(t, "harpy-bundle", "1.0", "jar", string(fabricJar(t, "wathe", "1.4.1"))), nil},
		{"a Forge jar of two mods", "forge", jarOf(t, map[string]string{"META-INF/mods.toml": "[[mods]]\nmodId=\"bundle\"\n[[mods]]\nmodId=\"wathe\"\nversion=\"1.4.1\"\n"}),
			jarOf(t, map[string]string{"META-INF/mods.toml": "[[mods]]\nmodId=\"wathe\"\nversion=\"1.4.1\"\n"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPanel(t)
			dep := c.dep
			if dep == nil {
				dep = wathe(t)
			}
			m := &depModrinth{}
			m.add(fakeVersion{id: "w1", project: "WATHE", number: "1.4.1-1.21.1", loaders: []string{c.loader}, file: dep})
			m.add(fakeVersion{id: "k1", project: "KINS", number: "1.6.4", loaders: []string{c.loader}, file: fabricJar(t, "kinswathe", "1.6.4"), deps: required("WATHE")})
			p.withModrinth(m)
			sc := p.withFiles("alpha", "harpy")
			_ = sc
			body := `{"name":"harpy","spec":{"eula":true,"loader":"` + c.loader + `","version":"1.21.1","state":"Stopped"}}`
			if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != http.StatusCreated {
				t.Fatalf("create: %d %s", code, msg)
			}
			// Uploaded through the panel.
			if rec := p.send("owner", "PUT", "/orgs/alpha/servers/harpy/content?kind=mod&name=wathe-fork.jar", c.file); rec.Code != 200 {
				t.Fatalf("upload: %d %s", rec.Code, rec.Body)
			}
			if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[`+uploadEntry(p, "harpy", "wathe-fork.jar")+`,`+modrinthEntry("KINS", "k1")+`]}}`); code != 200 {
				t.Fatalf("patch: %d %s", code, msg)
			}
			p.expectMods("harpy", "KINS@k1", "wathe-fork.jar <- KINS")
		})
	}
}

// uploadEntry is an upload of a server's as the panel lists it.
func uploadEntry(p *panel, server, name string) string {
	p.t.Helper()
	for _, m := range p.serverOf(server).Spec.Mods {
		if m.Source == avsysv1.SourceUpload && m.Project == name {
			raw, _ := json.Marshal(m)
			return string(raw)
		}
	}
	p.t.Fatalf("%s has no upload %s", server, name)
	return ""
}

// A dependency the panel added goes once something else on the server is
// it, as an upload of a fork; and comes back once that goes. The fork is
// taken to need what the project it stands in for needs.
func TestDependencyGivesWayToAnUpload(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "f1", project: "FAPI", number: "0.116", file: fabricJar(t, "fabric-api", "0.116")})
	m.add(fakeVersion{id: "w1", project: "WATHE", number: "1.4.1", file: fabricJar(t, "wathe", "1.4.1"), deps: required("FAPI")})
	m.add(fakeVersion{id: "k1", project: "KINS", number: "1.6.4", file: fabricJar(t, "kinswathe", "1.6.4"), deps: required("WATHE")})
	p.withModrinth(m)
	p.withFiles("alpha", "harpy")
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"harpy","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+modrinthEntry("KINS", "k1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("harpy", "KINS@k1", "+WATHE@w1 <- KINS", "+FAPI@f1 <- WATHE")
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/harpy/content?kind=mod&name=wathe-fork.jar", fabricJar(t, "wathe", "1.4.1+wathez.1")); rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	// The fork is taken to need what Wathe does.
	p.expectMods("harpy", "KINS@k1", "wathe-fork.jar <- KINS", "+FAPI@f1 <- wathe-fork.jar")
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[`+modrinthEntry("KINS", "k1")+`]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	p.expectMods("harpy", "KINS@k1", "+WATHE@w1 <- KINS", "+FAPI@f1 <- WATHE")
}

// What the server has of a dependency, but not in a version what needs it
// says it works with, is said on the entry: a second copy beside it would
// not start either, and none is added.
func TestDependencyVersionNotMet(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "w1", project: "WATHE", number: "1.5.0", file: fabricJar(t, "wathe", "1.5.0")})
	m.add(fakeVersion{id: "k1", project: "KINS", number: "1.6.4", file: fabricJar(t, "kinswathe", "1.6.4", "depends", `{"wathe":">=1.5.0","minecraft":"1.21.1"}`), deps: required("WATHE")})
	p.withModrinth(m)
	fork := `{"source":"upload","kind":"mod","project":"wathe-fork.jar","versionName":"1.4.1+wathez.1","provides":["wathe"]}`
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"harpy","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+fork+`,`+modrinthEntry("KINS", "k1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("harpy", "KINS@k1", "wathe-fork.jar <- KINS")
	for _, e := range p.serverOf("harpy").Spec.Mods {
		want := []avsysv1.UnmetDependency(nil)
		if e.Project == "KINS" {
			want = []avsysv1.UnmetDependency{{ID: "wathe", Wants: ">=1.5.0", Has: "1.4.1+wathez.1", By: "wathe-fork"}}
		}
		if fmt.Sprint(e.Unmet) != fmt.Sprint(want) {
			t.Errorf("%s: unmet %+v, want %+v", e.Project, e.Unmet, want)
		}
	}
}

// A server whose own environment says how its image downloads what content
// needs keeps that: the panel lists nothing for it, and tells the image
// nothing else. Without it again, what is needed is listed.
func TestOwnDependencySettingKept(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "w1", project: "WATHE", number: "1.4.1", file: fabricJar(t, "wathe", "1.4.1")})
	m.add(fakeVersion{id: "k1", project: "KINS", number: "1.6.4", file: fabricJar(t, "kinswathe", "1.6.4"), deps: required("WATHE")})
	p.withModrinth(m)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"own","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","env":[{"name":"MODRINTH_DOWNLOAD_DEPENDENCIES","value":"none"}],"mods":[`+modrinthEntry("KINS", "k1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("own", "KINS@k1")
	mc := p.serverOf("own")
	if mc.Spec.DependenciesListed {
		t.Error("listed for a server that says its own")
	}
	var told []string
	for _, e := range controller.MinecraftEnv(mc) {
		if e.Name == controller.DependencyEnv {
			told = append(told, e.Value)
		}
	}
	// The server's own comes last, and wins.
	if len(told) == 0 || told[len(told)-1] != "none" {
		t.Errorf("image told %q", told)
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/own", `{"spec":{"env":[]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	p.expectMods("own", "KINS@k1", "+WATHE@w1 <- KINS")
	if env := envOf(p.serverOf("own")); env[controller.DependencyEnv] != "none" {
		t.Errorf("then told %q", env[controller.DependencyEnv])
	}
}

// Modrinth not answering, a change that adds nothing from it keeps what
// was listed, but what nothing needs any more.
func TestDependenciesWhileModrinthIsDown(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "f1", project: "FAPI", number: "1", file: fabricJar(t, "fabric-api", "1")})
	m.add(fakeVersion{id: "a1", project: "AAA", number: "1", file: fabricJar(t, "aaa", "1"), deps: required("FAPI")})
	m.add(fakeVersion{id: "b1", project: "BBB", number: "1", file: fabricJar(t, "bbb", "1"), deps: required("CCC")})
	m.add(fakeVersion{id: "c1", project: "CCC", number: "1", file: fabricJar(t, "ccc", "1")})
	p.withModrinth(m)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"down","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[`+modrinthEntry("AAA", "a1")+`,`+modrinthEntry("BBB", "b1")+`]}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	p.expectMods("down", "AAA@a1", "BBB@b1", "+FAPI@f1 <- AAA", "+CCC@c1 <- BBB")
	var keep []string
	for _, e := range p.serverOf("down").Spec.Mods {
		if e.Project != "BBB" {
			raw, _ := json.Marshal(e)
			keep = append(keep, string(raw))
		}
	}
	p.srv.catalog = newCatalog("test")
	m.mu.Lock()
	m.down = true
	m.mu.Unlock()
	p.srv.catalog.http.Transport = m
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/down", `{"spec":{"mods":[`+strings.Join(keep, ",")+`]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	p.expectMods("down", "AAA@a1", "+FAPI@f1 <- AAA")
}

// Harpy Express, as it came from Crafty: a fork of Wathe that Modrinth
// does not have, beside mods Modrinth has that need Wathe, and what those
// need. Imported, or found in its folders, it is listed as it is, the fork
// being what they need: no second Wathe, nothing to download, and no
// setting of its own needed for it.
func TestHarpyExpress(t *testing.T) {
	p := newPanel(t)
	m := &depModrinth{slugs: map[string]string{"wathe": "WATHE", "kinswathe": "KINS", "harpymodloader": "HARPY", "wathe-blood": "BLOOD", "fabric-api": "FAPI", "yacl": "YACL"}}
	m.add(fakeVersion{id: "fapi", project: "FAPI", number: "0.116.18+1.21.1", filename: "fabric-api-0.116.18+1.21.1.jar", file: fabricJar(t, "fabric-api", "0.116.18+1.21.1", "provides", "fabric")})
	m.add(fakeVersion{id: "yacl", project: "YACL", number: "3.8.2+1.21.1-fabric", filename: "yet_another_config_lib_v3-3.8.2+1.21.1-fabric.jar", file: fabricJar(t, "yet_another_config_lib_v3", "3.8.2+1.21.1-fabric"), deps: required("FAPI")})
	m.add(fakeVersion{id: "wathe", project: "WATHE", number: "1.4.1-1.21.1", filename: "wathe-1.4.1-1.21.1.jar", file: fabricJar(t, "wathe", "1.4.1-1.21.1", "depends", `{"fabric-api":"*"}`), deps: required("FAPI")})
	m.add(fakeVersion{id: "harpy", project: "HARPY", number: "1.2.5-h1.4", filename: "harpymodloader-1.2.5-h1.4.jar", file: fabricJar(t, "harpymodloader", "1.2.5-h1.4", "depends", `{"fabric":"*","yet_another_config_lib_v3":">=3.8.0+1.21.1-fabric"}`),
		deps: append(required("YACL"), modrinthDependency{VersionID: "wathe", Type: "required"})})
	m.add(fakeVersion{id: "kins", project: "KINS", number: "1.6.4", filename: "kinswathe-1.6.4.jar", file: fabricJar(t, "kinswathe", "1.6.4", "depends", `{"fabric-api":"*","harpymodloader":"*"}`), deps: required("HARPY", "FAPI")})
	m.add(fakeVersion{id: "blood", project: "BLOOD", number: "1.0.2", filename: "wathe_blood-1.0.2.jar", file: fabricJar(t, "wathe_blood", "1.0.2", "depends", `{"wathe":">=1.4.0"}`), deps: required("WATHE")})
	p.withModrinth(m)
	fork := fabricJar(t, "wathe", "1.4.1-1.21.1+wathez.1", "depends", `{"fabric-api":"*"}`)
	root := t.TempDir()
	files := map[string][]byte{"mods/wathe-1.4.1-1.21.1+wathez.1.jar": fork}
	for _, id := range []string{"fapi", "yacl", "harpy", "kins", "blood"} {
		v, _ := m.version(id)
		files["mods/"+v.filename] = v.file
	}
	lay(t, root, files)
	p.srv.ContentFiles = func(_ context.Context, namespace, name string) (ServerFiles, error) {
		if name == "harpy" {
			return DirFiles(root), nil
		}
		return nil, errors.New("not reachable")
	}
	// Made as the Import form makes one, then its files looked at.
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"harpy","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped"}}`); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	if !p.serverOf("harpy").Spec.DependenciesListed {
		t.Fatal("a new server is not listed")
	}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/harpy/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	p.expectMods("harpy",
		"wathe-1.4.1-1.21.1+wathez.1.jar <- BLOOD,HARPY",
		"KINS@kins", "BLOOD@blood", "HARPY@harpy <- KINS", "YACL@yacl <- HARPY",
		// The fork needs what Wathe does.
		"FAPI@fapi <- KINS,YACL,wathe-1.4.1-1.21.1+wathez.1.jar",
	)
	mc := p.serverOf("harpy")
	if len(foundOf(mc)) != 0 {
		t.Errorf("left found: %v", pathsOf(foundOf(mc)))
	}
	for _, e := range mc.Spec.Mods {
		if e.Project == "WATHE" || len(e.Unmet) > 0 {
			t.Errorf("%+v", e)
		}
	}
	env := envOf(mc)
	if env[controller.DependencyEnv] != "none" || strings.Contains(env["MODRINTH_PROJECTS"], "WATHE") {
		t.Errorf("image told %q, given %q", env[controller.DependencyEnv], env["MODRINTH_PROJECTS"])
	}
	// The next start finds each of its files as it is, and nothing more.
	refs := strings.Split(env["MODRINTH_PROJECTS"], "\n")
	var installs []string
	for _, ref := range refs {
		_, id, _ := strings.Cut(ref, ":")
		v, ok := m.version(id)
		if !ok {
			t.Fatalf("given %s", ref)
		}
		installs = append(installs, "mods/"+v.filename)
	}
	installs = append(installs, "mods/wathe-1.4.1-1.21.1+wathez.1.jar")
	sort.Strings(installs)
	var there []string
	for name := range files {
		there = append(there, name)
	}
	sort.Strings(there)
	if !slices.Equal(installs, there) {
		t.Errorf("a start has\n  %q\nnot\n  %q", installs, there)
	}

	// Changed after, it is worked out again the same way.
	p.withFiles("alpha", "harpy")
	var all []string
	for _, e := range p.serverOf("harpy").Spec.Mods {
		if e.Project != "BLOOD" {
			raw, _ := json.Marshal(e)
			all = append(all, string(raw))
		}
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[`+strings.Join(all, ",")+`]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	p.expectMods("harpy",
		"wathe-1.4.1-1.21.1+wathez.1.jar <- HARPY",
		"KINS@kins", "HARPY@harpy <- KINS", "YACL@yacl <- HARPY", "FAPI@fapi <- KINS,YACL,wathe-1.4.1-1.21.1+wathez.1.jar",
	)
}

// A server whose image downloaded what its content needs, as every server
// did before, is brought over to the panel listing it, once, with its
// files as they are: each dependency at the very version it has, though a
// newer one is out, and what the image put there that the panel would not
// have (a second copy beside a fork) too. The next start has exactly the
// files it has now, and a game running is not said to wait for a restart.
func TestDependenciesBroughtOver(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	m := &depModrinth{slugs: map[string]string{"fabric-api": "FAPI"}}
	// What the server's mods need, a version since than it has, and what
	// it has: the shape of a Fabric server of two dozen mods.
	m.add(fakeVersion{id: "fapi-new", project: "FAPI", number: "0.120.0", file: fabricJar(t, "fabric-api", "0.120.0")})
	m.add(fakeVersion{id: "fapi-old", project: "FAPI", number: "0.116.18", file: fabricJar(t, "fabric-api", "0.116.18")})
	m.add(fakeVersion{id: "cloth-new", project: "CLOTH", number: "16", file: fabricJar(t, "cloth-config", "16"), deps: required("FAPI")})
	m.add(fakeVersion{id: "cloth-old", project: "CLOTH", number: "15", file: fabricJar(t, "cloth-config", "15"), deps: required("FAPI")})
	m.add(fakeVersion{id: "arch", project: "ARCH", number: "13", file: fabricJar(t, "architectury", "13"), deps: required("FAPI")})
	m.add(fakeVersion{id: "wathe", project: "WATHE", number: "1.4.1", file: fabricJar(t, "wathe", "1.4.1")})
	listed := []string{}
	files := map[string][]byte{}
	manifest := []string{}
	for i := range 24 {
		id := fmt.Sprintf("m%02d", i)
		var deps []modrinthDependency
		switch {
		case i%3 == 0:
			deps = required("FAPI", "CLOTH")
		case i%3 == 1:
			deps = required("FAPI")
		case i == 2:
			deps = required("ARCH", "WATHE")
		}
		v := fakeVersion{id: id, project: "P" + id, number: "1.0", file: fabricJar(t, "mod"+id, "1.0"), deps: deps}
		m.add(v)
		v, _ = m.version(id)
		listed = append(listed, modrinthEntry("P"+id, id))
		files["mods/"+v.filename] = v.file
		manifest = append(manifest, "mods/"+v.filename)
	}
	// What the image downloaded for them, when they were added.
	for _, id := range []string{"fapi-old", "cloth-old", "arch", "wathe"} {
		v, _ := m.version(id)
		files["mods/"+v.filename] = v.file
		manifest = append(manifest, "mods/"+v.filename)
	}
	// A fork of Wathe, put there by hand and found.
	files["mods/wathe-fork.jar"] = fabricJar(t, "wathe", "1.4.1+fork")
	raw, _ := json.Marshal(map[string]any{"@type": "me.itzg.helpers.modrinth.ModrinthManifest", "files": manifest, "projects": []string{"Pm00:m00"}})
	files[".modrinth-manifest.json"] = raw
	root := t.TempDir()
	lay(t, root, files)
	p.withModrinth(m)
	p.srv.ContentFiles = func(_ context.Context, namespace, name string) (ServerFiles, error) {
		if name == "bingo" {
			return DirFiles(root), nil
		}
		return nil, errors.New("not reachable")
	}

	// Made by a panel before this one: its image downloads what it needs.
	var mods []avsysv1.ContentRef
	if err := json.Unmarshal([]byte("["+strings.Join(listed, ",")+"]"), &mods); err != nil {
		t.Fatal(err)
	}
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "bingo"},
		Spec:       avsysv1.MinecraftServerSpec{EULA: true, Version: "1.21.1", Loader: avsysv1.LoaderFabric, Mods: mods},
	}
	p.srv.applyDefaults("alpha", "bingo", &mc.Spec)
	if err := p.srv.Client.Create(ctx, mc); err != nil {
		t.Fatal(err)
	}
	before := envOf(mc)
	if before[controller.DependencyEnv] != "required" {
		t.Fatalf("before: %q", before[controller.DependencyEnv])
	}
	// Its game running, nothing waiting for a restart.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: mc.Namespace, Name: "bingo", UID: types.UID("pod-1")}}
	if err := p.srv.Client.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	mc = p.serverOf("bingo")
	mc.Status.Phase, mc.Status.ObservedGeneration = avsysv1.PhaseRunning, mc.Generation
	if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
		t.Fatal(err)
	}
	// A change through the panel before it is brought over lists nothing.
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/bingo", `{"spec":{"properties":{"motd":"Bingo"}}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	if got := p.serverOf("bingo"); got.Spec.DependenciesListed || len(got.Spec.Mods) != 24 {
		t.Fatalf("listed before its files were looked at: %d", len(got.Spec.Mods))
	}
	mc = p.serverOf("bingo")
	mc.Status.ObservedGeneration = mc.Generation
	if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
		t.Fatal(err)
	}

	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/bingo/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	mc = p.serverOf("bingo")
	if !mc.Spec.DependenciesListed {
		t.Fatal("not brought over")
	}
	deps := map[string]avsysv1.ContentRef{}
	for _, e := range mc.Spec.Mods {
		if e.Dependency {
			deps[e.Project] = e
		}
	}
	for project, want := range map[string]string{"FAPI": "fapi-old", "CLOTH": "cloth-old", "ARCH": "arch", "WATHE": "wathe"} {
		if deps[project].Version != want {
			t.Errorf("%s pinned to %q, want %q", project, deps[project].Version, want)
		}
	}
	if len(deps) != 4 {
		t.Errorf("dependencies %v", p.mods("bingo"))
	}
	if got := deps["FAPI"].NeededBy; len(got) != 18 {
		t.Errorf("Fabric API needed by %d: %v", len(got), got)
	}
	// The next start gets the same files, all of them, and no more.
	after := envOf(mc)
	if after[controller.DependencyEnv] != "none" {
		t.Errorf("after: %q", after[controller.DependencyEnv])
	}
	var gets []string
	for _, ref := range strings.Split(after["MODRINTH_PROJECTS"], "\n") {
		_, id, _ := strings.Cut(ref, ":")
		v, ok := m.version(id)
		if !ok {
			t.Fatalf("given %s", ref)
		}
		gets = append(gets, "mods/"+v.filename)
	}
	sort.Strings(gets)
	sort.Strings(manifest)
	if !slices.Equal(gets, manifest) {
		t.Errorf("the next start gets\n  %q\nnot what it has,\n  %q", gets, manifest)
	}
	for name, body := range files {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || !bytes.Equal(got, body) {
			t.Errorf("%s changed: %v", name, err)
		}
	}
	// Its game runs what it lists already.
	if got, want := mc.Annotations[avsysv1.AnnotationSettled], fmt.Sprintf("pod-1@%d", mc.Generation); got != want {
		t.Errorf("settled %q, want %q", got, want)
	}
	// A start while Modrinth does not answer has them.
	record, _ := os.ReadFile(filepath.Join(root, ".avsys/imported-modrinth"))
	for project, e := range deps {
		if !strings.Contains(string(record), project+":"+e.Version+"\tmods/") {
			t.Errorf("record %q has no %s", record, project)
		}
	}
	// Once only: looked at again, nothing changes.
	gen := mc.Generation
	st := p.srv.foundStateOf(client.ObjectKeyFromObject(mc))
	st.mu.Lock()
	st.last = time.Time{}
	st.mu.Unlock()
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/bingo/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	if got := p.serverOf("bingo").Generation; got != gen {
		t.Errorf("changed again: generation %d, was %d", got, gen)
	}
	// The image has them as entries, after its next start: the record of
	// what to start with while Modrinth does not answer has done its part.
	given := strings.Split(after["MODRINTH_PROJECTS"], "\n")
	raw, _ = json.Marshal(map[string]any{"files": manifest, "projects": given})
	lay(t, root, map[string][]byte{".modrinth-manifest.json": raw})
	p.srv.settleOfflineRecord(ctx, DirFiles(root), client.ObjectKeyFromObject(mc))
	if _, err := os.Stat(filepath.Join(root, ".avsys/imported-modrinth")); !errors.Is(err, os.ErrNotExist) {
		record, _ := os.ReadFile(filepath.Join(root, ".avsys/imported-modrinth"))
		t.Errorf("record left: %q", record)
	}

	// Saved as it is through the panel, nothing changes; changed, it is
	// worked out again: the second copy of Wathe goes, the fork found
	// beside it being Wathe.
	var all []string
	for _, e := range p.serverOf("bingo").Spec.Mods {
		raw, _ := json.Marshal(e)
		all = append(all, string(raw))
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/bingo", `{"spec":{"mods":[`+strings.Join(all, ",")+`]}}`); code != 200 {
		t.Fatalf("saved as it is: %d %s", code, msg)
	}
	if got := len(p.serverOf("bingo").Spec.Mods); got != 29 {
		t.Errorf("saved as it is: %d entries", got)
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/bingo", `{"spec":{"mods":[`+strings.Join(all[1:], ",")+`]}}`); code != 200 {
		t.Fatalf("changed: %d %s", code, msg)
	}
	for _, e := range p.serverOf("bingo").Spec.Mods {
		if e.Project == "WATHE" {
			t.Errorf("a second copy kept: %+v", e)
		}
	}
}

// The image installs two versions of one project for two mods that each
// name another, as it did for Bingo's Animatica and Dynamic FPS. It takes
// a project once in its list, so those cannot both be listed: such a
// server is left to its image, files and all, and not looked at again for
// it until its image's record changes. Its content changed through the
// panel, what that needs is listed, as the image would have downloaded it
// anew.
func TestTwoVersionsLeftToTheImage(t *testing.T) {
	ctx := context.Background()
	p := newPanel(t)
	m := &depModrinth{}
	m.add(fakeVersion{id: "fapi2", project: "FAPI", number: "0.92.12", file: fabricJar(t, "fabric-api", "0.92.12")})
	m.add(fakeVersion{id: "fapi1", project: "FAPI", number: "0.86.1", file: fabricJar(t, "fabric-api", "0.86.1")})
	m.add(fakeVersion{id: "fps", project: "FPS", number: "3.4", file: fabricJar(t, "dynamic_fps", "3.4"), deps: required("FAPI")})
	m.add(fakeVersion{id: "anim", project: "ANIM", number: "0.6", file: fabricJar(t, "animatica", "0.6"), deps: []modrinthDependency{{VersionID: "fapi1", Type: "required"}}})
	m.add(fakeVersion{id: "lith", project: "LITH", number: "0.11", file: fabricJar(t, "lithium", "0.11")})
	p.withModrinth(m)
	files := map[string][]byte{}
	var manifest []string
	for _, id := range []string{"fps", "anim", "fapi2", "fapi1"} {
		v, _ := m.version(id)
		files["mods/"+v.filename] = v.file
		manifest = append(manifest, "mods/"+v.filename)
	}
	raw, _ := json.Marshal(map[string]any{"files": manifest, "projects": []string{"FPS:fps", "ANIM:anim"}})
	files[".modrinth-manifest.json"] = raw
	root := t.TempDir()
	lay(t, root, files)
	p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return DirFiles(root), nil }
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "bingo"},
		Spec: avsysv1.MinecraftServerSpec{EULA: true, Version: "1.21.1", Loader: avsysv1.LoaderFabric, State: avsysv1.StateStopped, Mods: []avsysv1.ContentRef{
			{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "FPS", Version: "fps"},
			{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "ANIM", Version: "anim"},
		}},
	}
	p.srv.applyDefaults("alpha", "bingo", &mc.Spec)
	if err := p.srv.Client.Create(ctx, mc); err != nil {
		t.Fatal(err)
	}
	was := p.serverOf("bingo").Generation
	look := func() {
		t.Helper()
		st := p.srv.foundStateOf(client.ObjectKeyFromObject(mc))
		st.mu.Lock()
		st.last = time.Time{}
		st.mu.Unlock()
		if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/bingo/content/look?wait=true", ""); code != http.StatusOK {
			t.Fatalf("look: %d", code)
		}
	}
	asked := func() int {
		m.mu.Lock()
		defer m.mu.Unlock()
		n := 0
		for _, a := range m.asked {
			if a == "POST https://api.modrinth.com/v2/version_files" {
				n++
			}
		}
		return n
	}
	look()
	if got := p.serverOf("bingo"); got.Spec.DependenciesListed || got.Generation != was || envOf(got)[controller.DependencyEnv] != "required" {
		t.Fatalf("brought over: %v", p.mods("bingo"))
	}
	before := asked()
	look()
	if asked() != before {
		t.Error("its files were looked up again, its image's record as it was")
	}
	// Its content changed: listed, one version of Fabric API.
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/bingo", `{"spec":{"mods":[`+modrinthEntry("FPS", "fps")+`,`+modrinthEntry("ANIM", "anim")+`,`+modrinthEntry("LITH", "lith")+`]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	p.expectMods("bingo", "FPS@fps", "ANIM@anim", "LITH@lith", "+FAPI@fapi2 <- ANIM,FPS")
	if !p.serverOf("bingo").Spec.DependenciesListed {
		t.Error("not listed once its content changed")
	}
}

// Not brought over: a server whose own environment says how its image
// downloads what content needs, nor one running a modpack.
func TestDependenciesNotBroughtOver(t *testing.T) {
	for name, change := range map[string]func(*avsysv1.MinecraftServerSpec){
		"own setting": func(s *avsysv1.MinecraftServerSpec) {
			s.Env = []corev1.EnvVar{{Name: controller.DependencyEnv, Value: "none"}}
		},
		"modpack": func(s *avsysv1.MinecraftServerSpec) {
			s.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "pack", Version: "pv", GameVersion: "1.21.1", Loader: "fabric"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			p := newPanel(t)
			m := &depModrinth{}
			m.add(fakeVersion{id: "f1", project: "FAPI", number: "1", file: fabricJar(t, "fabric-api", "1")})
			m.add(fakeVersion{id: "a1", project: "AAA", number: "1", file: fabricJar(t, "aaa", "1"), deps: required("FAPI")})
			p.withModrinth(m)
			root := t.TempDir()
			v, _ := m.version("f1")
			a, _ := m.version("a1")
			raw, _ := json.Marshal(map[string]any{"files": []string{"mods/" + v.filename, "mods/" + a.filename}})
			lay(t, root, map[string][]byte{"mods/" + v.filename: v.file, "mods/" + a.filename: a.file, ".modrinth-manifest.json": raw})
			p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return DirFiles(root), nil }
			mc := &avsysv1.MinecraftServer{
				ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "kept"},
				Spec:       avsysv1.MinecraftServerSpec{EULA: true, Version: "1.21.1", Loader: avsysv1.LoaderFabric, State: avsysv1.StateStopped, Mods: []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "AAA", Version: "a1"}}},
			}
			change(&mc.Spec)
			p.srv.applyDefaults("alpha", "kept", &mc.Spec)
			if err := p.srv.Client.Create(ctx, mc); err != nil {
				t.Fatal(err)
			}
			was := p.serverOf("kept").Generation
			if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/kept/content/look?wait=true", ""); code != http.StatusOK {
				t.Fatalf("look: %d", code)
			}
			if got := p.serverOf("kept"); got.Spec.DependenciesListed || got.Generation != was {
				t.Errorf("brought over: %v", p.mods("kept"))
			}
		})
	}
}
