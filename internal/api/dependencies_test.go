package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// depVersion is a version as Modrinth tells of it, with its file.
type depVersion struct {
	ID            string           `json:"id"`
	ProjectID     string           `json:"project_id"`
	VersionNumber string           `json:"version_number"`
	VersionType   string           `json:"version_type"`
	GameVersions  []string         `json:"game_versions"`
	Loaders       []string         `json:"loaders"`
	Files         []map[string]any `json:"files"`
	Dependencies  []map[string]any `json:"dependencies"`
}

// depModrinth answers what the panel asks Modrinth about projects and
// versions, newest first and narrowed as Modrinth narrows them, and gives
// their files.
type depModrinth struct {
	mu       sync.Mutex
	projects map[string]modrinthProjectInfo
	versions []depVersion
	files    map[string][]byte
	// down are paths that get no answer, "" for every one.
	down  []string
	asked map[string]int
}

func (m *depModrinth) project(id, slug, title, kind string) {
	m.projects[id] = modrinthProjectInfo{ID: id, Slug: slug, Title: title, ProjectType: kind}
}

// version adds a version of a project, for one game and loader, whose file
// holds a fabric.mod.json with this id; needs are the projects it requires,
// as "project" or "project@version".
func (m *depModrinth) version(t *testing.T, project, id, game, loader, modID string, needs ...string) {
	file := "https://cdn.test/" + t.Name() + "/" + id + ".jar"
	if modID != "" {
		m.files[file] = jarOf(t, map[string]string{"fabric.mod.json": `{"id":"` + modID + `","version":"` + id + `"}`})
	}
	v := depVersion{ID: id, ProjectID: project, VersionNumber: id, VersionType: "release", GameVersions: []string{game}, Loaders: []string{loader},
		Files: []map[string]any{{"url": file, "filename": id + ".jar", "primary": true}}, Dependencies: []map[string]any{}}
	for _, n := range needs {
		p, ver, _ := strings.Cut(n, "@")
		v.Dependencies = append(v.Dependencies, map[string]any{"project_id": p, "version_id": ver, "dependency_type": "required"})
	}
	// An optional one is not what it needs.
	v.Dependencies = append(v.Dependencies, map[string]any{"project_id": "POPTIONAL", "dependency_type": "optional"})
	m.versions = append([]depVersion{v}, m.versions...)
}

func newDepModrinth() *depModrinth {
	return &depModrinth{projects: map[string]modrinthProjectInfo{}, files: map[string][]byte{}, asked: map[string]int{}}
}

func (m *depModrinth) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked[r.URL.Path]++
	for _, d := range m.down {
		if strings.HasPrefix(r.URL.Path, d) {
			return nil, errors.New("offline")
		}
	}
	answer := func(v any) (*http.Response, error) {
		body, _ := json.Marshal(v)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	}
	notFound := &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}
	if r.URL.Host == "cdn.test" {
		if b, ok := m.files[r.URL.String()]; ok {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
		}
		return notFound, nil
	}
	byIDOrSlug := func(ref string) (modrinthProjectInfo, bool) {
		for _, p := range m.projects {
			if p.ID == ref || p.Slug == ref {
				return p, true
			}
		}
		return modrinthProjectInfo{}, false
	}
	if id, ok := strings.CutPrefix(r.URL.Path, "/v2/version/"); ok {
		for _, v := range m.versions {
			if v.ID == id {
				return answer(v)
			}
		}
		return notFound, nil
	}
	if ref, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v2/project/"), "/version"); ok {
		p, ok := byIDOrSlug(ref)
		if !ok {
			return notFound, nil
		}
		var loaders, games []string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("loaders")), &loaders)
		_ = json.Unmarshal([]byte(r.URL.Query().Get("game_versions")), &games)
		out := []depVersion{}
		for _, v := range m.versions {
			if v.ProjectID == p.ID && (len(loaders) == 0 || slices.ContainsFunc(v.Loaders, func(l string) bool { return slices.Contains(loaders, l) })) &&
				(len(games) == 0 || slices.ContainsFunc(v.GameVersions, func(g string) bool { return slices.Contains(games, g) })) {
				out = append(out, v)
			}
		}
		return answer(out)
	}
	if ref, ok := strings.CutPrefix(r.URL.Path, "/v2/project/"); ok {
		if p, ok := byIDOrSlug(ref); ok {
			return answer(p)
		}
		return notFound, nil
	}
	return nil, errors.New("offline: " + r.URL.String())
}

// harpyModrinth is Harpy Express's shape on Modrinth, for Fabric 1.21.1:
// Kinswathe needs Wathe, naming a version of it for another game; Wathe
// needs Ratatouille; Shooter Punishments needs a modpack and a resource
// pack, which are never added.
func harpyModrinth(t *testing.T, p *panel) *depModrinth {
	m := newDepModrinth()
	m.project("PKINS", "kinswathe", "Kinswathe", "mod")
	m.project("PWATHE", "wathe", "Wathe", "mod")
	m.project("PRATA", "ratatouille", "Ratatouille", "mod")
	m.project("PSHOOT", "shooterpunishments", "Shooter Punishments", "mod")
	m.project("PPACK", "harpy-express", "The Last Voyage of the Harpy Express", "modpack")
	m.project("PRES", "harpy-textures", "Harpy Textures", "resourcepack")
	m.version(t, "PRATA", "vRATA", "1.21.1", "fabric", "ratatouille")
	m.version(t, "PWATHE", "vWATHE1", "1.21.1", "fabric", "wathe", "PRATA")
	m.version(t, "PWATHE", "vWATHE2", "1.21.4", "fabric", "wathe", "PRATA")
	m.version(t, "PKINS", "vKINS", "1.21.1", "fabric", "kinswathe", "PWATHE@vWATHE2")
	m.version(t, "PSHOOT", "vSHOOT", "1.21.1", "fabric", "shooterpunishments", "PPACK", "PRES")
	p.srv.catalog.http.Transport = m
	p.srv.Downloads = m
	backoff := modrinthBackoff
	modrinthBackoff = 0
	t.Cleanup(func() { modrinthBackoff = backoff })
	return m
}

// listing is a server's content as "project@version", with what needs each
// the panel added: "PWATHE@vWATHE1 <- PKINS".
func listing(spec *avsysv1.MinecraftServerSpec) []string {
	var out []string
	for _, m := range spec.Mods {
		entry := m.Project
		if m.Version != "" {
			entry += "@" + m.Version
		}
		if m.Dependency {
			entry += " <- " + strings.Join(m.NeededBy, ",")
		}
		out = append(out, entry)
	}
	return out
}

func wantListing(t *testing.T, what string, spec *avsysv1.MinecraftServerSpec, want ...string) {
	t.Helper()
	if got := listing(spec); !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

const harpyServer = `{"name":"harpy","spec":{"eula":true,"loader":"fabric","version":"1.21.1","state":"Stopped","mods":[%s]}}`

func makeHarpy(t *testing.T, p *panel, mods string) {
	t.Helper()
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", strings.Replace(harpyServer, "%s", mods, 1)); code != 201 {
		t.Fatalf("create: %d %s", code, msg)
	}
}

// A new server lists what its Modrinth content needs, and what that needs,
// as entries the panel added: Wathe at the newest version for the server's
// game, since the one Kinswathe names is for another. One is not taken off
// while what needs it stays; all go with it. What needs them is the panel's
// to say, as is whether the server lists them at all.
func TestDependenciesListedWhenAdded(t *testing.T) {
	p := newPanel(t)
	harpyModrinth(t, p)
	makeHarpy(t, p, `{"source":"modrinth","kind":"mod","project":"kinswathe"}`)
	mc := p.serverOf("harpy")
	if !mc.Spec.DependenciesListed {
		t.Fatal("a new server does not list what its content needs")
	}
	wantListing(t, "made", &mc.Spec, "kinswathe@vKINS", "PWATHE@vWATHE1 <- kinswathe", "PRATA@vRATA <- PWATHE")
	if w := mc.Spec.Mods[1]; w.Title != "Wathe" || w.Kind != avsysv1.KindMod || !slices.Equal(w.Provides, []string{"wathe"}) {
		t.Errorf("Wathe as added: %+v", w)
	}

	patch := func(body string) (int, string) {
		t.Helper()
		return p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", body)
	}
	mods := func(keep ...int) string {
		var out []string
		for _, i := range keep {
			b, _ := json.Marshal(p.serverOf("harpy").Spec.Mods[i])
			out = append(out, string(b))
		}
		return `{"spec":{"mods":[` + strings.Join(out, ",") + `]}}`
	}
	// Saved as it is, and with the panel's notes left out of the request.
	if code, msg := patch(mods(0, 1, 2)); code != 200 {
		t.Fatalf("saved as it is: %d %s", code, msg)
	}
	if code, msg := patch(`{"spec":{"mods":[{"source":"modrinth","kind":"mod","project":"kinswathe","version":"vKINS"},{"source":"modrinth","kind":"mod","project":"PWATHE","version":"vWATHE1"},{"source":"modrinth","kind":"mod","project":"PRATA","version":"vRATA"}]}}`); code != 200 {
		t.Fatalf("saved without notes: %d %s", code, msg)
	}
	wantListing(t, "saved", &p.serverOf("harpy").Spec, "kinswathe@vKINS", "PWATHE@vWATHE1 <- kinswathe", "PRATA@vRATA <- PWATHE")
	if code, _ := patch(`{"spec":{"dependenciesListed":false}}`); code != 200 || !p.serverOf("harpy").Spec.DependenciesListed {
		t.Errorf("a request switched it off: %d", code)
	}
	// Wathe is needed by Kinswathe.
	if code, msg := patch(mods(0, 2)); code != 400 {
		t.Errorf("Wathe taken off while needed: %d %s", code, msg)
	}
	// Kinswathe taken off takes what it needed, and what that needed.
	if code, msg := patch(mods()); code != 200 {
		t.Fatalf("Kinswathe taken off: %d %s", code, msg)
	}
	wantListing(t, "Kinswathe taken off", &p.serverOf("harpy").Spec)

	// A project the list has, by its slug, is not added again, nor marked.
	makeHarpyNamed(t, p, "both", `{"source":"modrinth","kind":"mod","project":"kinswathe"},{"source":"modrinth","kind":"mod","project":"wathe","version":"vWATHE1"}`)
	wantListing(t, "both", &p.serverOf("both").Spec, "kinswathe@vKINS", "wathe@vWATHE1", "PRATA@vRATA <- wathe")
}

func makeHarpyNamed(t *testing.T, p *panel, name, mods string) {
	t.Helper()
	body := strings.Replace(strings.Replace(harpyServer, "%s", mods, 1), `"name":"harpy"`, `"name":"`+name+`"`, 1)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != 201 {
		t.Fatalf("create %s: %d %s", name, code, msg)
	}
}

// Nothing is added for what the server has already, by mod ID, as the
// loader goes: a fork of Wathe uploaded under its own name, a renamed copy
// in its folders, a mod that says it provides Wathe, or one with Wathe in
// it.
func TestDependencyProvidedByID(t *testing.T) {
	inner := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"wathe","version":"1.4.1"}`})
	bundle := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"harpy-bundle","jars":[{"file":"META-INF/jars/wathe.jar"}]}`, "META-INF/jars/wathe.jar": string(inner)})
	for _, c := range []struct {
		name string
		jar  []byte
	}{
		{"fork", jarOf(t, map[string]string{"fabric.mod.json": `{"id":"wathe","version":"1.4.1-1.21.1+wathez.1"}`})},
		{"provides", jarOf(t, map[string]string{"fabric.mod.json": `{"id":"wathe-plus","provides":["wathe"]}`})},
		{"nested", bundle},
		{"quilt", jarOf(t, map[string]string{"quilt.mod.json": `{"quilt_loader":{"id":"wathe-q","provides":[{"id":"harpy:wathe"}]}}`})},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Uploaded through the panel, then Kinswathe added.
			p := newPanel(t)
			harpyModrinth(t, p)
			makeHarpy(t, p, "")
			serverFilesFrom(t, p, "harpy", sftp.InMemHandler())
			if rec := p.send("owner", "PUT", "/orgs/alpha/servers/harpy/content?kind=mod&name=wathe-1.4.1-1.21.1%2Bwathez.1.jar", c.jar); rec.Code != 200 {
				t.Fatalf("upload: %d %s", rec.Code, rec.Body)
			}
			if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":`+modsJSON(p, "harpy", `{"source":"modrinth","kind":"mod","project":"kinswathe"}`)+`}}`); code != 200 {
				t.Fatalf("Kinswathe added: %d %s", code, msg)
			}
			wantListing(t, "uploaded first", &p.serverOf("harpy").Spec, "wathe-1.4.1-1.21.1+wathez.1.jar", "kinswathe@vKINS")

			// A file in the server's folders under another name, found there.
			root := t.TempDir()
			lay(t, root, map[string][]byte{"mods/w.jar": c.jar})
			found, err := p.srv.findContent(context.Background(), "test", DirFiles(root), &avsysv1.MinecraftServerSpec{}, nil)
			if err != nil || len(found) != 1 || len(found[0].Provides) == 0 {
				t.Fatalf("found %+v, %v", found, err)
			}
			spec := &avsysv1.MinecraftServerSpec{Loader: avsysv1.LoaderFabric, Version: "1.21.1", DependenciesListed: true,
				Mods: []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "kinswathe", Version: "vKINS"}}}
			if err := p.srv.resolveContent(context.Background(), spec, &avsysv1.MinecraftServerSpec{}, found); err != nil {
				t.Fatal(err)
			}
			wantListing(t, "found in the folders", spec, "kinswathe@vKINS")
		})
	}
}

// modsJSON is a server's list with more entries after it, as a request
// sends it.
func modsJSON(p *panel, server string, more ...string) string {
	var out []string
	for _, m := range p.serverOf(server).Spec.Mods {
		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}
	return "[" + strings.Join(append(out, more...), ",") + "]"
}

// An import lists the archive's Modrinth content when the server is made
// and uploads the rest after: the fork uploaded then takes the place of the
// Wathe the panel added for Kinswathe, and what Wathe needed is needed by
// the fork. So the server ends with one Wathe, as the archive has.
func TestUploadTakesPlaceOfDependency(t *testing.T) {
	p := newPanel(t)
	harpyModrinth(t, p)
	makeHarpy(t, p, `{"source":"modrinth","kind":"mod","project":"PKINS","version":"vKINS","title":"kinswathe-1.6.4.jar"}`)
	wantListing(t, "made", &p.serverOf("harpy").Spec, "PKINS@vKINS", "PWATHE@vWATHE1 <- PKINS", "PRATA@vRATA <- PWATHE")
	serverFilesFrom(t, p, "harpy", sftp.InMemHandler())
	// One that is not Wathe changes nothing.
	other := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"noellesroles"}`})
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/harpy/content?kind=mod&name=noellesroles.jar", other); rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	fork := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"wathe","version":"1.4.1-1.21.1+wathez.1"}`})
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/harpy/content?kind=mod&name=wathe-fork.jar", fork); rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	wantListing(t, "fork uploaded", &p.serverOf("harpy").Spec, "PKINS@vKINS", "PRATA@vRATA <- wathe-fork.jar", "noellesroles.jar", "wathe-fork.jar")
}

// Only a mod, plugin or datapack is added: Shooter Punishments says it
// needs the Harpy Express modpack and a resource pack, neither of which is.
func TestModpackDependencyNotAdded(t *testing.T) {
	p := newPanel(t)
	harpyModrinth(t, p)
	makeHarpy(t, p, `{"source":"modrinth","kind":"mod","project":"shooterpunishments"}`)
	wantListing(t, "made", &p.serverOf("harpy").Spec, "shooterpunishments@vSHOOT")
}

// When Modrinth does not answer, after being asked again, what is added
// cannot be known, and the change is refused with nothing changed.
func TestDependenciesWhileModrinthIsDown(t *testing.T) {
	p := newPanel(t)
	m := harpyModrinth(t, p)
	makeHarpy(t, p, "")
	m.down = []string{"/v2/project/PWATHE"}
	code, body := p.call("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[{"source":"modrinth","kind":"mod","project":"kinswathe"}]}}`)
	if code != http.StatusBadGateway || !strings.Contains(body["message"].(string), "Modrinth is not answering, so what Kinswathe needs cannot be worked out. Nothing was changed.") {
		t.Errorf("refused with %d %v", code, body)
	}
	if n := m.asked["/v2/project/PWATHE"]; n != 3 {
		t.Errorf("Modrinth asked %d times, want 3", n)
	}
	wantListing(t, "after", &p.serverOf("harpy").Spec)
	// Nor a new server made.
	code, _ = p.do("owner", "POST", "/orgs/alpha/servers", strings.Replace(strings.Replace(harpyServer, "%s", `{"source":"modrinth","kind":"mod","project":"kinswathe"}`, 1), "harpy", "second", 1))
	if code != http.StatusBadGateway {
		t.Errorf("made: %d", code)
	}
	if code, _ := p.do("owner", "GET", "/orgs/alpha/servers/second", ""); code != 404 {
		t.Errorf("the server was made: %d", code)
	}
	// Back, it is added.
	m.down = nil
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/harpy", `{"spec":{"mods":[{"source":"modrinth","kind":"mod","project":"kinswathe"}]}}`); code != 200 {
		t.Fatalf("back: %d %s", code, msg)
	}
	wantListing(t, "back", &p.serverOf("harpy").Spec, "kinswathe@vKINS", "PWATHE@vWATHE1 <- kinswathe", "PRATA@vRATA <- PWATHE")
}

// A server made before the panel listed what content needs, or one whose
// own env says how the image is to download it, or a modpack's, has
// nothing added: the image downloads it, as before.
func TestDependenciesLeftToTheImage(t *testing.T) {
	p := newPanel(t)
	harpyModrinth(t, p)
	for name, spec := range map[string]avsysv1.MinecraftServerSpec{
		"older": {},
		"own":   {DependenciesListed: true, Env: []corev1.EnvVar{{Name: "MODRINTH_DOWNLOAD_DEPENDENCIES", Value: "required"}}},
	} {
		spec.EULA, spec.Loader, spec.Version, spec.State = true, avsysv1.LoaderFabric, "1.21.1", avsysv1.StateStopped
		mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: name}, Spec: spec}
		p.srv.applyDefaults("alpha", name, &mc.Spec)
		if err := p.srv.Client.Create(context.Background(), mc); err != nil {
			t.Fatal(err)
		}
		p.setPhase("alpha", name, avsysv1.PhaseStopped)
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/"+name, `{"spec":{"mods":[{"source":"modrinth","kind":"mod","project":"kinswathe"}]}}`); code != 200 {
			t.Fatalf("%s: %d %s", name, code, msg)
		}
		after := p.serverOf(name)
		wantListing(t, name, &after.Spec, "kinswathe@vKINS")
		if name == "older" && after.Spec.DependenciesListed {
			t.Error("an older server was switched over")
		}
	}
}

// A copy, a server brought back or made from a template, and a restore
// list what content needs as the server they are of does.
func TestDependenciesListedFollowsTheOriginal(t *testing.T) {
	for _, listed := range []bool{true, false} {
		recorded := &avsysv1.MinecraftServerSpec{DependenciesListed: listed}
		if got := fromSnapshot(&avsysv1.MinecraftServerSpec{DependenciesListed: !listed}, recorded, nil); got.DependenciesListed != listed {
			t.Errorf("copy of %v: %v", listed, got.DependenciesListed)
		}
		dst := &avsysv1.MinecraftServerSpec{DependenciesListed: !listed}
		if restoreSoftware(dst, recorded); dst.DependenciesListed != listed {
			t.Errorf("restore of %v: %v", listed, dst.DependenciesListed)
		}
	}
}

// What a jar gives a server is read as the loaders read it.
func TestModIDs(t *testing.T) {
	inner := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"inner"}`})
	deeper := jarOf(t, map[string]string{"fabric.mod.json": `{"id":"middle","jars":[{"file":"META-INF/jars/inner.jar"}]}`, "META-INF/jars/inner.jar": string(inner)})
	cases := map[string]struct {
		files map[string]string
		want  []string
	}{
		"fabric":   {map[string]string{"fabric.mod.json": "\ufeff" + `{"id":"sodium","provides":["indium-compat"]}`}, []string{"sodium", "indium-compat"}},
		"nested":   {map[string]string{"fabric.mod.json": `{"id":"outer","jars":[{"file":"META-INF/jars/middle.jar"}]}`, "META-INF/jars/middle.jar": string(deeper)}, []string{"outer", "middle", "inner"}},
		"quilt":    {map[string]string{"quilt.mod.json": `{"quilt_loader":{"id":"q","provides":["a",{"id":"org:b"}]}}`}, []string{"q", "a", "b"}},
		"forge":    {map[string]string{"META-INF/mods.toml": "modLoader=\"javafml\"\n[[mods]]\nmodId=\"sophisticatedcore\"\n[[mods]]\n  modId = 'second'\n[[dependencies.sophisticatedcore]]\nmodId=\"forge\"\n"}, []string{"sophisticatedcore", "second"}},
		"neoforge": {map[string]string{"META-INF/neoforge.mods.toml": "[[mods]]\nmodId=\"neo\"\n", "META-INF/jarjar/metadata.json": `{"jars":[{"path":"META-INF/jarjar/inner.jar"}]}`, "META-INF/jarjar/inner.jar": string(inner)}, []string{"neo", "inner"}},
		"plugin":   {map[string]string{"plugin.yml": "name: ViaVersion\nprovides: [ViaVersionLegacy]\n"}, []string{"ViaVersion", "ViaVersionLegacy"}},
		"nothing":  {map[string]string{"pack.mcmeta": "{}"}, nil},
	}
	for name, c := range cases {
		raw := jarOf(t, c.files)
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if got := modIDs(zr); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
