package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// jarOf is a jar or zip holding these files.
func jarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// aPicture is a small PNG.
func aPicture(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := range 16 {
		img.Set(i%4, i/4, color.NRGBA{R: 200, G: 40, B: 90, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// What each loader's mods, plugins and datapacks say of themselves is read
// from them: their name, version and picture.
func TestContentMeta(t *testing.T) {
	icon := aPicture(t)
	for _, c := range []struct {
		name                  string
		kind                  avsysv1.ContentKind
		files                 map[string]string
		title, version, about string
		picture               bool
	}{
		{"fabric", avsysv1.KindMod, map[string]string{
			"fabric.mod.json":                 `{"schemaVersion":1,"id":"wi_zoom","name":"WI Zoom","version":"1.5-MC1.20.1","description":"Zoom.","icon":"assets/wi_zoom/icon.png"}`,
			"assets/wi_zoom/icon.png":         icon,
			"net/wurstclient/zoom/Zoom.class": "\xca\xfe\xba\xbe\x00\x00\x00\x3d",
		}, "WI Zoom", "1.5-MC1.20.1", "Zoom.", true},
		{"fabric, pictures by size, the largest taken", avsysv1.KindMod, map[string]string{
			"fabric.mod.json": `{"id":"x","name":"Sized","version":"2.0","icon":{"16":"small.png","128":"large.png","32":"mid.png"}}`,
			"large.png":       icon,
		}, "Sized", "2.0", "", true},
		{"fabric, a version left to the build", avsysv1.KindMod, map[string]string{
			"fabric.mod.json": `{"id":"x","name":"Dev","version":"${version}"}`,
		}, "Dev", "", "", false},
		{"quilt", avsysv1.KindMod, map[string]string{
			"quilt.mod.json": `{"schema_version":1,"quilt_loader":{"id":"q","version":"3.1.0","metadata":{"name":"Quilted","icon":"icon.png"}}}`,
			"icon.png":       icon,
		}, "Quilted", "3.1.0", "", true},
		{"neoforge, its version in its manifest", avsysv1.KindMod, map[string]string{
			"META-INF/neoforge.mods.toml": "modLoader=\"javafml\"\nlogoFile=\"logo.png\"\n[[mods]]\nmodId=\"jei\"\nversion=\"${file.jarVersion}\"\ndisplayName=\"Just Enough Items\"\n[[dependencies.jei]]\nmodId=\"neoforge\"\nversion=\"[21,)\"\n",
			"META-INF/MANIFEST.MF":        "Manifest-Version: 1.0\r\nImplementation-Version: 19.21.0.247\r\n",
			"logo.png":                    icon,
		}, "Just Enough Items", "19.21.0.247", "", true},
		{"forge", avsysv1.KindMod, map[string]string{
			"META-INF/mods.toml": "modLoader='javafml'\n[[mods]]\nmodId='create'\nversion='0.5.1.f'\ndisplayName='Create'\nlogoFile='create.png'\n[[mods]]\ndisplayName='Second'\n",
		}, "Create", "0.5.1.f", "", false},
		{"plugin.yml, its version a number", avsysv1.KindPlugin, map[string]string{
			"plugin.yml": "name: Chunky\nversion: 1.4\nmain: org.popcraft.chunky.ChunkyBukkit\ndescription: Pre-generates chunks\n",
		}, "Chunky", "1.4", "Pre-generates chunks", false},
		{"paper-plugin.yml", avsysv1.KindPlugin, map[string]string{
			"paper-plugin.yml": "name: Mapped\nversion: '2.0.1'\nmain: x.Y\n",
		}, "Mapped", "2.0.1", "", false},
		{"datapack", avsysv1.KindDatapack, map[string]string{
			"pack.mcmeta": "\xef\xbb\xbf" + `{"pack":{"pack_format":15,"description":[{"text":"Bern's ","color":"gold"},{"text":"§atrades"}]}}`,
			"pack.png":    icon,
		}, "", "", "Bern's trades", true},
		{"fabric.mod.json that is not JSON", avsysv1.KindMod, map[string]string{
			"fabric.mod.json": "{\"name\": \"Broken\n",
		}, "", "", "", false},
		{"nothing to say", avsysv1.KindMod, map[string]string{"a/B.class": "x"}, "", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := jarOf(t, c.files)
			zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			got := contentMeta(zr, c.kind)
			if got.title != c.title || got.version != c.version || got.about != c.about || (got.icon != nil) != c.picture {
				t.Errorf("got %q %q %q picture %v; want %q %q %q picture %v", got.title, got.version, got.about, got.icon != nil, c.title, c.version, c.about, c.picture)
			}
		})
	}
}

// modrinthFiles answers what the panel asks Modrinth about files, and
// counts the questions.
type hashModrinth struct {
	mu       sync.Mutex
	versions map[string]string // sha1 -> JSON of the version
	projects string
	asked    int
	hashes   [][]string
	down     bool
	// noProjects is Modrinth answering about files but not projects.
	noProjects bool
}

func (m *hashModrinth) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, errors.New("offline")
	}
	body := ""
	switch {
	case r.Method == http.MethodPost && r.URL.String() == "https://api.modrinth.com/v2/version_files":
		m.asked++
		var in struct {
			Hashes    []string `json:"hashes"`
			Algorithm string   `json:"algorithm"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Algorithm != "sha1" {
			return nil, errors.New("not sha1")
		}
		m.hashes = append(m.hashes, in.Hashes)
		var parts []string
		for _, h := range in.Hashes {
			if v, ok := m.versions[h]; ok {
				parts = append(parts, `"`+h+`":`+v)
			}
		}
		body = "{" + strings.Join(parts, ",") + "}"
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.String(), "https://api.modrinth.com/v2/version/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/version/")
		body = `{"id":"` + id + `","version_number":"` + id + `","files":[]}`
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.String(), "https://api.modrinth.com/v2/project/"):
		id := strings.TrimPrefix(r.URL.Path, "/v2/project/")
		body = `{"id":"` + id + `","slug":"` + id + `","title":"` + id + `"}`
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.String(), "https://api.modrinth.com/v2/projects?"):
		if m.noProjects {
			return nil, errors.New("offline: projects")
		}
		body = m.projects
	default:
		return nil, errors.New("offline: " + r.URL.String())
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// lay writes files into a server's folder on this machine.
func lay(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func pathsOf(found []FoundFile) []string {
	out := []string{}
	for _, f := range found {
		out = append(out, f.Path)
	}
	return out
}

// What is in a server's folders that the panel did not put there is found:
// what Modrinth has by its hash as that project at that version, the rest as
// the files say, or by name. What the panel or the image put there is not.
func TestFindContent(t *testing.T) {
	p := newPanel(t)
	icon := aPicture(t)
	bingo := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"bingo","version":"local"}`})
	lithium := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"Lithium","version":"0.11.2"}`})
	zoom := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"WI Zoom","version":"1.5","icon":"icon.png"}`, "icon.png": icon})
	essentials := jarOf(t, map[string]string{"plugin.yml": "name: Essentials\nversion: 2.20.1\n"})
	bern := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Bern's trades"}}`, "pack.png": icon})
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"mods/bingo-2.9.6+mc1.20.1.jar":    bingo,
		"mods/lithium-fabric-0.11.2.jar":   lithium,
		"mods/WI-Zoom-1.5.jar":             zoom,
		"mods/c2me.jar":                    []byte("not a zip"),
		"mods/off.jar.disabled":            zoom,
		"mods/.hidden.jar":                 zoom,
		"mods/notes.txt":                   []byte("x"),
		"mods/managed-by-modrinth.jar":     zoom,
		"mods/from-a-url.jar":              zoom,
		"mods/from-curseforge.jar":         zoom,
		"mods/in-the-pack.jar":             zoom,
		"mods/sub/deep.jar":                zoom,
		"plugins/EssentialsX-2.20.1.jar":   essentials,
		"world/datapacks/bern.zip":         bern,
		"world/datapacks/uploaded.zip":     bern,
		"world/datapacks/mael/pack.mcmeta": []byte(`{"pack":{"description":{"text":"Mael's"}}}`),
		"world/datapacks/mael/pack.png":    []byte(icon),
		"other/datapacks/elsewhere.zip":    bern,
		".modrinth-manifest.json":          []byte(`{"files":["mods/managed-by-modrinth.jar"],"projects":["x"]}`),
		"mods/.var-list-manifest.json":     []byte(`{"files":["from-a-url.jar"]}`),
		".curseforge-files-manifest.json":  []byte(`{"entries":[{"ids":{"modId":1,"fileId":2},"filePath":"mods/from-curseforge.jar"}]}`),
		".modrinth-modpack-manifest.json":  []byte(`{"files":["mods/in-the-pack.jar","config/x.toml"]}`),
	})
	modrinth := &hashModrinth{
		versions: map[string]string{
			sha1Of(bingo):   `{"id":"V1","project_id":"P1","version_number":"2.9.6+mc1.20.1"}`,
			sha1Of(lithium): `{"id":"V2","project_id":"P2","version_number":"mc1.20.1-0.11.2"}`,
		},
		projects: `[{"id":"P1","slug":"yet-another-bingo","title":"Yet Another Bingo","icon_url":"https://cdn.modrinth.com/bingo.png"},{"id":"P2","slug":"lithium","title":"Lithium","icon_url":"https://cdn.modrinth.com/lithium.png"}]`,
	}
	p.srv.catalog.http.Transport = modrinth
	spec := &avsysv1.MinecraftServerSpec{Loader: avsysv1.LoaderFabric, Version: "1.20.1", Mods: []avsysv1.ContentRef{
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "uploaded.zip"},
		// Lithium, at the version of the file on the server: the panel's own.
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "lithium", Version: "V2"},
	}}
	// Lithium's file is of the very version the panel pinned: it is the
	// panel's, though the image has not said so yet.
	found, err := p.srv.findContent(context.Background(), "test", DirFiles(root), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mods/WI-Zoom-1.5.jar", "mods/bingo-2.9.6+mc1.20.1.jar", "mods/c2me.jar", "plugins/EssentialsX-2.20.1.jar", "world/datapacks/bern.zip", "world/datapacks/mael"}
	if got := pathsOf(found); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("found %v, want %v", got, want)
	}
	by := map[string]FoundFile{}
	for _, f := range found {
		by[f.Path] = f
	}
	if f := by["mods/bingo-2.9.6+mc1.20.1.jar"]; f.Title != "Yet Another Bingo" || f.Version != "2.9.6+mc1.20.1" || f.Project != "P1" || f.VersionID != "V1" || f.Slug != "yet-another-bingo" || f.IconURL != "https://cdn.modrinth.com/bingo.png" || f.Kind != avsysv1.KindMod {
		t.Errorf("bingo, from Modrinth: %+v", f)
	}
	if f := by["mods/WI-Zoom-1.5.jar"]; f.Title != "WI Zoom" || f.Version != "1.5" || !f.Picture || f.Project != "" || f.Looked == 0 {
		t.Errorf("WI Zoom, as it says: %+v", f)
	}
	if f := by["mods/c2me.jar"]; f.Title != "c2me" || f.Version != "" || f.Picture {
		t.Errorf("c2me, by its name: %+v", f)
	}
	if f := by["plugins/EssentialsX-2.20.1.jar"]; f.Title != "Essentials" || f.Version != "2.20.1" || f.Kind != avsysv1.KindPlugin {
		t.Errorf("a plugin: %+v", f)
	}
	if f := by["world/datapacks/bern.zip"]; f.Title != "bern" || f.About != "Bern's trades" || !f.Picture || f.Kind != avsysv1.KindDatapack {
		t.Errorf("a datapack: %+v", f)
	}
	if f := by["world/datapacks/mael"]; !f.Folder || f.Title != "mael" || f.About != "Mael's" || !f.Picture {
		t.Errorf("a datapack in a folder: %+v", f)
	}
	if modrinth.asked != 1 || len(modrinth.hashes[0]) != 6 {
		t.Errorf("Modrinth was asked %d times, about %v; want once, about the 6 files read", modrinth.asked, modrinth.hashes)
	}

	// Looked at again, nothing that has not changed is read or asked about.
	again, err := p.srv.findContent(context.Background(), "test", DirFiles(root), spec, found)
	if err != nil {
		t.Fatal(err)
	}
	if modrinth.asked != 1 {
		t.Errorf("asked Modrinth again about files it answered for: %d", modrinth.asked)
	}
	if a, b := jsonOf(t, again), jsonOf(t, found); a != b {
		t.Errorf("looked at again: %s, want %s", a, b)
	}
	// Nor by another panel, or this one started again, that has what was
	// found before: what Modrinth said is kept with it. Only what was not
	// kept, the panel's own Lithium, is asked about again.
	p.srv.recognitions.Clear()
	if _, err := p.srv.findContent(context.Background(), "test", DirFiles(root), spec, found); err != nil {
		t.Fatal(err)
	}
	if modrinth.asked != 2 || strings.Join(modrinth.hashes[1], " ") != sha1Of(lithium) {
		t.Errorf("asked Modrinth again after a restart: %d times, %v", modrinth.asked, modrinth.hashes)
	}

	// Modrinth not answering: files are found all the same, as they say,
	// and asked about the next time.
	p.srv.recognitions.Clear()
	modrinth.down = true
	off, err := p.srv.findContent(context.Background(), "test", DirFiles(root), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := off[1]; f.Path != "mods/bingo-2.9.6+mc1.20.1.jar" || f.Title != "bingo" || f.Version != "local" || f.Looked != 0 {
		t.Errorf("bingo, Modrinth down: %+v", f)
	}
	modrinth.down = false
	back, err := p.srv.findContent(context.Background(), "test", DirFiles(root), spec, off)
	if err != nil {
		t.Fatal(err)
	}
	if f := back[1]; f.Title != "Yet Another Bingo" || f.Project != "P1" {
		t.Errorf("bingo, Modrinth back: %+v", f)
	}

	// A server with none of the folders has nothing found.
	if _, err := p.srv.findContent(context.Background(), "test", DirFiles(filepath.Join(root, "nowhere")), spec, nil); err != nil {
		t.Errorf("a server with none of the folders: %v", err)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A world named in the server's properties, or its settings, is where its
// datapacks are found.
func TestFoundDatapacksOfItsWorld(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"world/datapacks/a.zip": []byte("a"),
		"lobby/datapacks/b.zip": []byte("b"),
		"server.properties":     []byte("motd=x\nlevel-name=lobby\n"),
		"realm/datapacks/c.zip": []byte("c"),
	})
	ctx := context.Background()
	// Its settings say nothing for sure (LEVEL comes from elsewhere): the
	// properties say.
	unsure := &avsysv1.MinecraftServerSpec{Env: []corev1.EnvVar{{Name: "CF_SET_LEVEL_FROM", Value: "WORLD_FILE"}}}
	if found, _ := p.srv.findContent(ctx, "test", DirFiles(root), unsure, nil); strings.Join(pathsOf(found), " ") != "lobby/datapacks/b.zip" {
		t.Errorf("by its properties: %v", pathsOf(found))
	}
	named := &avsysv1.MinecraftServerSpec{Env: []corev1.EnvVar{{Name: "LEVEL", Value: "realm"}}}
	if found, _ := p.srv.findContent(ctx, "test", DirFiles(root), named, nil); strings.Join(pathsOf(found), " ") != "realm/datapacks/c.zip" {
		t.Errorf("by its settings: %v", pathsOf(found))
	}
}

// fileServer gives alpha's server "world" its files from a folder here.
func (p *panel) fileServer(root string) {
	p.srv.ContentFiles = func(_ context.Context, namespace, name string) (ServerFiles, error) {
		if namespace == p.srv.Namespace("alpha") && name == "world" {
			return DirFiles(root), nil
		}
		return nil, errors.New("not reachable")
	}
}

func (p *panel) foundOn(server string) []FoundFile {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: server}, mc); err != nil {
		p.t.Fatal(err)
	}
	return foundOf(mc)
}

// listedOn is a server's content as "source kind project@version", in order.
func (p *panel) listedOn(server string) []string {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: server}, mc); err != nil {
		p.t.Fatal(err)
	}
	var out []string
	for _, m := range mc.Spec.Mods {
		entry := string(m.Source) + " " + string(m.Kind) + " " + m.Project
		if m.Version != "" {
			entry += "@" + m.Version
		}
		out = append(out, entry)
	}
	return out
}

func (p *panel) serverOf(name string) *avsysv1.MinecraftServer {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: name}, mc); err != nil {
		p.t.Fatal(err)
	}
	return mc
}

// modrinthVersionOf is how hashModrinth tells of a version: its project,
// and the file it has under this name and hash.
func modrinthVersionOf(id, project, number, filename string, file []byte, requires ...string) string {
	deps := `{"project_id":"optional-one","dependency_type":"optional"}`
	for _, r := range requires {
		deps += `,{"project_id":"` + r + `","dependency_type":"required"}`
	}
	return `{"id":"` + id + `","project_id":"` + project + `","version_number":"` + number + `","files":[{"filename":"` + filename + `","primary":true,"hashes":{"sha1":"` + sha1Of(file) + `"}}],"dependencies":[` + deps + `]}`
}

func exists(t *testing.T, file string) bool {
	t.Helper()
	_, err := os.Stat(file)
	return err == nil
}

// Content found in a server's folders is taken into its list as if it had
// been added there, the files left as they are. A file Modrinth has, under
// Modrinth's own name for it, is that project at that version, as long as
// all it needs is in the list too; anything else is an upload. Looked at
// again, nothing is taken twice.
func TestFoundTakenIntoTheList(t *testing.T) {
	p := newPanel(t)
	icon := aPicture(t)
	bingo := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"bingo","version":"local"}`, "a/B.class": "\xca\xfe\xba\xbe\x00\x00\x00\x3d"})
	api := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"Fabric API","version":"0.92.2"}`})
	lithium := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"Lithium","version":"0.11.2"}`})
	needy := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"Needy","version":"1.0"}`})
	zoom := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"WI Zoom","version":"1.5","icon":"icon.png"}`, "icon.png": icon})
	bern := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Bern's trades"}}`})
	root := t.TempDir()
	files := map[string][]byte{
		"mods/bingo-2.9.6.jar":             bingo,
		"mods/fabric-api-0.92.2.jar":       api,
		"mods/lithium-renamed.jar":         lithium,
		"mods/needy-1.0.jar":               needy,
		"mods/WI-Zoom-1.5.jar":             zoom,
		"mods/bad,name.jar":                zoom,
		"world/datapacks/bern.zip":         bern,
		"world/datapacks/mael/pack.mcmeta": []byte(`{"pack":{"description":"Mael's"}}`),
	}
	lay(t, root, files)
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{
		versions: map[string]string{
			sha1Of(bingo):   modrinthVersionOf("V1", "P1", "2.9.6+mc1.20.1", "bingo-2.9.6.jar", bingo, "P2"),
			sha1Of(api):     modrinthVersionOf("V2", "P2", "0.92.2+1.20.1", "fabric-api-0.92.2.jar", api),
			sha1Of(lithium): modrinthVersionOf("V3", "P3", "mc1.20.1-0.11.2", "lithium-fabric-mc1.20.1-0.11.2.jar", lithium),
			sha1Of(needy):   modrinthVersionOf("V4", "P4", "1.0", "needy-1.0.jar", needy, "P9"),
		},
		projects: `[{"id":"P1","slug":"yet-another-bingo","title":"Yet Another Bingo","icon_url":"https://cdn.modrinth.com/bingo.png"},{"id":"P2","slug":"fabric-api","title":"Fabric API"},{"id":"P3","slug":"lithium","title":"Lithium"},{"id":"P4","slug":"needy","title":"Needy"}]`,
	}

	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	want := []string{
		"upload mod WI-Zoom-1.5.jar", // Modrinth does not have it
		"modrinth mod P1@V1",         // bingo, and what it needs, Fabric API, is taken with it
		"modrinth mod P2@V2",
		"upload mod lithium-renamed.jar", // not under Modrinth's name for it: the image would fetch another
		"upload mod needy-1.0.jar",       // what it needs is not in the list: the image would fetch it
		"upload datapack bern.zip",
		"upload datapack mael", // a folder, kept where it is
	}
	if got := p.listedOn("world"); strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("listed %q,\nwant %q", got, want)
	}
	mc := p.serverOf("world")
	for _, m := range mc.Spec.Mods {
		switch m.Project {
		case "P1":
			if m.Title != "Yet Another Bingo" || m.VersionName != "2.9.6+mc1.20.1" || m.IconURL != "https://cdn.modrinth.com/bingo.png" || m.Java != 17 {
				t.Errorf("bingo: %+v", m)
			}
		case "WI-Zoom-1.5.jar":
			if m.Title != "WI Zoom" || m.VersionName != "1.5" {
				t.Errorf("WI Zoom: %+v", m)
			}
		}
	}
	// What cannot be listed as it is named stays found.
	if got := pathsOf(foundOf(mc)); strings.Join(got, " ") != "mods/bad,name.jar" {
		t.Errorf("left found: %v", got)
	}
	// No file was changed, and none copied: the uploads are kept where they
	// were found, and say where.
	for name, body := range files {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || !bytes.Equal(got, body) {
			t.Errorf("%s changed: %v", name, err)
		}
	}
	if exists(t, filepath.Join(root, ".avsys/content")) {
		t.Error("files were copied to where uploads are kept")
	}
	for _, m := range mc.Spec.Mods {
		if m.Source != avsysv1.SourceUpload {
			continue
		}
		want := "mods/" + m.Project
		if m.Kind == avsysv1.KindDatapack {
			want = "world/datapacks/" + m.Project
		}
		if m.File != want {
			t.Errorf("%s kept at %q, want %q", m.Project, m.File, want)
		}
	}
	// What a start without Modrinth starts with.
	if record, _ := os.ReadFile(filepath.Join(root, ".avsys/imported-modrinth")); string(record) != "P1:V1\tmods/bingo-2.9.6.jar\nP2:V2\tmods/fabric-api-0.92.2.jar\n" {
		t.Errorf("record %q", record)
	}
	// Its uploads' pictures are theirs.
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/icon?kind=mod&name=WI-Zoom-1.5.jar", nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("an upload's picture: %d", rec.Code)
	}
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/icon?kind=mod&name=needy-1.0.jar", nil); rec.Code != 404 {
		t.Errorf("one with no picture: %d", rec.Code)
	}

	// Looked at again, nothing more.
	st := p.srv.foundStateOf(client.ObjectKeyFromObject(mc))
	st.mu.Lock()
	st.last = time.Time{}
	st.mu.Unlock()
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	if got := p.listedOn("world"); len(got) != len(want) {
		t.Errorf("looked at again: %q", got)
	}
}

// What was taken into the list is changed as any other: taken off, its file
// goes, as one the image installed would; another version of it, the file
// of the one before goes, so that it is not beside the new one; a datapack
// folder taken off goes with all it holds.
func TestFoundTakenOffAndChanged(t *testing.T) {
	p := newPanel(t)
	bingo := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"bingo"}`})
	api := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"Fabric API"}`})
	zoom := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"WI Zoom"}`})
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"mods/bingo.jar":                   bingo,
		"mods/fabric-api.jar":              api,
		"mods/WI-Zoom.jar":                 zoom,
		"world/datapacks/mael/pack.mcmeta": []byte(`{"pack":{"description":"Mael's"}}`),
		"world/datapacks/mael/data/x.json": []byte(`{}`),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{
		versions: map[string]string{
			sha1Of(bingo): modrinthVersionOf("V1", "P1", "1", "bingo.jar", bingo),
			sha1Of(api):   modrinthVersionOf("V2", "P2", "2", "fabric-api.jar", api),
		},
	}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	if got := p.listedOn("world"); len(got) != 4 {
		t.Fatalf("listed %q", got)
	}
	patch := func(mods []avsysv1.ContentRef) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"mods": mods}})
		if code, msg := p.do("admin", "PATCH", "/orgs/alpha/servers/world", string(raw)); code != 200 {
			t.Fatalf("patch: %d %s", code, msg)
		}
	}
	mods := p.serverOf("world").Spec.Mods
	// Bingo to another version; WI Zoom and the datapack taken off.
	var next []avsysv1.ContentRef
	for _, m := range mods {
		switch m.Project {
		case "P1":
			m.Version, m.VersionName = "V1b", ""
			next = append(next, m)
		case "P2":
			next = append(next, m)
		}
	}
	patch(next)
	for name, there := range map[string]bool{
		"mods/bingo.jar":       false, // another version: the image fetches it
		"mods/fabric-api.jar":  true,
		"mods/WI-Zoom.jar":     false,
		"world/datapacks/mael": false,
	} {
		if got := exists(t, filepath.Join(root, name)); got != there {
			t.Errorf("%s there: %v, want %v", name, got, there)
		}
	}
	if record, _ := os.ReadFile(filepath.Join(root, ".avsys/imported-modrinth")); string(record) != "P2:V2\tmods/fabric-api.jar\n" {
		t.Errorf("record %q", record)
	}
	if got := importedOf(p.serverOf("world")); len(got) != 1 || got["mods/fabric-api.jar"].Key == "" {
		t.Errorf("noted as taken from its files: %v", got)
	}
	// Nothing taken off is found and taken again.
	st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"})
	st.mu.Lock()
	st.last = time.Time{}
	st.mu.Unlock()
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	if got := p.listedOn("world"); strings.Join(got, ", ") != "modrinth mod P1@V1b, modrinth mod P2@V2" {
		t.Errorf("listed %q", got)
	}
	// Once the image has installed it as its own, it is the image's.
	lay(t, root, map[string][]byte{".modrinth-manifest.json": []byte(`{"files":["mods/fabric-api.jar","mods/bingo-1b.jar"],"projects":["P1:V1b","P2:V2"]}`)})
	st.mu.Lock()
	st.last = time.Time{}
	st.mu.Unlock()
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	if got := importedOf(p.serverOf("world")); len(got) != 0 {
		t.Errorf("still noted: %v", got)
	}
	if exists(t, filepath.Join(root, ".avsys/imported-modrinth")) {
		t.Error("the record is still there")
	}
}

// A running server whose content was taken from its own files is not asked
// to restart for it: the pod is noted as running what it lists. One that
// was waiting for a restart already still is.
func TestFoundTakenWithoutARestart(t *testing.T) {
	for _, pendingBefore := range []bool{false, true} {
		p := newPanel(t)
		root := t.TempDir()
		lay(t, root, map[string][]byte{"mods/a.jar": []byte("a")})
		p.fileServer(root)
		p.srv.catalog.http.Transport = &hashModrinth{}
		ctx := context.Background()
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "world", UID: "pod-1"}}
		if err := p.srv.Client.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		mc := p.serverOf("world")
		mc.Status.Phase = avsysv1.PhaseRunning
		mc.Status.ObservedGeneration = mc.Generation
		mc.Status.PendingRestart = pendingBefore
		if err := p.srv.Client.Status().Update(ctx, mc); err != nil {
			t.Fatal(err)
		}
		if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
			t.Fatalf("look: %d", code)
		}
		mc = p.serverOf("world")
		if len(mc.Spec.Mods) != 1 {
			t.Fatalf("listed %q", p.listedOn("world"))
		}
		if got := settledOn(mc, "pod-1"); got == pendingBefore {
			t.Errorf("waiting for a restart before: %v; noted as running it: %v (%q at generation %d)", pendingBefore, got, mc.Annotations[avsysv1.AnnotationSettled], mc.Generation)
		}
	}
}

// Nothing is taken from a server's folders while its game is starting or
// stopping, when the image may be putting files there or taking them away.
func TestFoundNotTakenWhileStarting(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("a")})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	p.setPhase("alpha", "world", avsysv1.PhaseStarting)
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	if got := p.listedOn("world"); len(got) != 0 {
		t.Errorf("taken while starting: %q", got)
	}
	if got := pathsOf(p.foundOn("world")); strings.Join(got, " ") != "mods/a.jar" {
		t.Errorf("found: %v", got)
	}
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	p.srv.lookRoundOnce(context.Background())
	if got := p.listedOn("world"); strings.Join(got, " ") != "upload mod a.jar" {
		t.Errorf("once running: %q", got)
	}
}

// A file of another version of a project the list has is a second copy of
// it: it is not listed besides, but left to be removed.
func TestFoundSecondCopyNotListed(t *testing.T) {
	p := newPanel(t)
	old := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"Lithium","version":"0.10"}`})
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/lithium-0.10.jar": old})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{
		versions: map[string]string{sha1Of(old): modrinthVersionOf("V0", "P3", "0.10", "lithium-0.10.jar", old)},
	}
	mc := p.serverOf("world")
	mc.Spec.Mods = []avsysv1.ContentRef{{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "P3", Version: "V3", Title: "Lithium", IconURL: "x", Java: 17}}
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	if got := p.listedOn("world"); strings.Join(got, ", ") != "modrinth mod P3@V3" {
		t.Errorf("listed %q", got)
	}
	if got := pathsOf(p.foundOn("world")); strings.Join(got, " ") != "mods/lithium-0.10.jar" {
		t.Errorf("found %v", got)
	}
}

// What is left found, as a file whose name the list cannot have, can be
// removed by an admin, which deletes it; nothing else of the server's can.
func TestFoundLeftRemoved(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"mods/bad,name.jar": []byte("x"),
		"world/level.dat":   []byte("not content"),
		"server.properties": []byte("motd=hi\n"),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	if got := pathsOf(p.foundOn("world")); strings.Join(got, " ") != "mods/bad,name.jar" {
		t.Fatalf("found %v", got)
	}
	remove := func(who, file string) int {
		code, _ := p.call(who, "DELETE", "/orgs/alpha/servers/world/content/found?path="+url.QueryEscape(file), "")
		return code
	}
	if code := remove("operator", "mods/bad,name.jar"); code != http.StatusForbidden {
		t.Errorf("an operator removing: %d", code)
	}
	if code := remove("admin", "mods/bad,name.jar"); code != http.StatusOK {
		t.Fatalf("an admin removing: %d", code)
	}
	if exists(t, filepath.Join(root, "mods/bad,name.jar")) {
		t.Error("its file is still there")
	}
	for _, file := range []string{"world/level.dat", "server.properties", "mods/bad,name.jar", "../etc/passwd"} {
		if code := remove("admin", file); code != http.StatusNotFound {
			t.Errorf("removing %s: %d", file, code)
		}
	}
	if err := p.srv.keepFound(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}, func(was []FoundFile) []FoundFile {
		return append(was, FoundFile{Path: "world/level.dat", Kind: avsysv1.KindMod}, FoundFile{Path: "mods/../server.properties", Kind: avsysv1.KindMod})
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"world/level.dat", "mods/../server.properties"} {
		if code := remove("admin", file); code != http.StatusBadRequest {
			t.Errorf("removing %s, named as found: %d", file, code)
		}
	}
	if !exists(t, filepath.Join(root, "server.properties")) || !exists(t, filepath.Join(root, "world/level.dat")) {
		t.Error("a file that is not content was removed")
	}
}

// A look that read the folders before a file was removed through the panel
// does not list it again once it is done.
func TestFoundRemovedDuringALook(t *testing.T) {
	p := newPanel(t)
	st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"})
	found := []FoundFile{{Path: "mods/a.jar"}, {Path: "mods/b.jar"}}
	started := time.Now()
	st.mu.Lock()
	st.removed["mods/a.jar"] = started.Add(time.Millisecond)
	st.mu.Unlock()
	if got := pathsOf(st.without(found, started)); strings.Join(got, " ") != "mods/b.jar" {
		t.Errorf("found %v", got)
	}
	// A look begun after it, which would not find it anyway, is not held to it.
	if got := pathsOf(st.without(found, time.Now().Add(time.Second))); len(got) != 2 {
		t.Errorf("a later look: %v", got)
	}
}

// Content put in a server's folders behind the panel's back, over SFTP or
// the terminal, is taken into its list without anyone opening its page: at
// once when it came through the panel's files, and otherwise with the next
// look, which comes sooner once the server is started or stopped.
func TestFoundWithoutAsking(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	ctx := context.Background()
	key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}
	listed := func() string { return strings.Join(p.listedOn("world"), ", ") }
	p.srv.lookRoundOnce(ctx)
	if got := listed(); got != "" {
		t.Fatalf("an empty server: %s", got)
	}
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("a")})
	// Not due yet: looked at a moment ago, and nothing said it changed.
	p.srv.lookRoundOnce(ctx)
	if got := listed(); got != "" {
		t.Fatalf("looked at again at once: %s", got)
	}
	// Stopped, from running: due. (Started, it waits for its game to be up.)
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	p.srv.lookRoundOnce(ctx)
	if got := listed(); got != "upload mod a.jar" {
		t.Fatalf("once it ran: %s", got)
	}
	// A file put there through the panel's own files.
	lay(t, root, map[string][]byte{"plugins/b.jar": []byte("b")})
	p.srv.lookSoon(key.Namespace, key.Name)
	p.srv.lookRoundOnce(ctx)
	if got := listed(); got != "upload mod a.jar, upload plugin b.jar" {
		t.Fatalf("after its files changed: %s", got)
	}
	// And after a while, whatever happened.
	lay(t, root, map[string][]byte{"mods/c.jar": []byte("c")})
	st := p.srv.foundStateOf(key)
	st.mu.Lock()
	st.last = time.Now().Add(-lookEvery - time.Second)
	st.mu.Unlock()
	p.srv.lookRoundOnce(ctx)
	if got := listed(); got != "upload mod a.jar, upload plugin b.jar, upload mod c.jar" {
		t.Fatalf("a while later: %s", got)
	}
	// Not while its files are being put back: then once they are.
	if err := p.srv.markRestoring(ctx, key, "abcd1234"); err != nil {
		t.Fatal(err)
	}
	lay(t, root, map[string][]byte{"mods/d.jar": []byte("d")})
	p.srv.lookSoon(key.Namespace, key.Name)
	p.srv.lookRoundOnce(ctx)
	if got := listed(); strings.Contains(got, "d.jar") {
		t.Fatalf("during a restore: %s", got)
	}
	if err := p.srv.markRestoring(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	p.srv.lookRoundOnce(ctx)
	if got := listed(); !strings.Contains(got, "upload mod d.jar") {
		t.Fatalf("after the restore: %s", got)
	}
}

// A server imported from an archive of its folder has the mods, plugins and
// datapacks it brought in its list once the import is done: as Modrinth
// knows them, or as uploads. Those the form listed for the panel to install
// instead are not kept as files, and are not listed twice.
func TestImportedContentFound(t *testing.T) {
	for _, tool := range []string{"bsdtar", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	icon := aPicture(t)
	bingo := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"bingo","version":"2.9.6"}`})
	zoom := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"WI Zoom","version":"1.5","icon":"icon.png"}`, "icon.png": icon})
	essentials := jarOf(t, map[string]string{"plugin.yml": "name: Essentials\nversion: 2.20.1\n"})
	hi := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Hi Drehmal"}}`})
	for _, listed := range []bool{false, true} {
		p := newPanel(t)
		root := t.TempDir()
		data := filepath.Join(root, "data")
		src := filepath.Join(root, "src")
		lay(t, src, map[string][]byte{
			"Bingo/server.properties":                  []byte("motd=Bingo\nlevel-name=drehmal\n"),
			"Bingo/mods/bingo-2.9.6+mc1.20.1.jar":      bingo,
			"Bingo/mods/WI-Zoom-1.5.jar":               zoom,
			"Bingo/plugins/EssentialsX.jar":            essentials,
			"Bingo/drehmal/level.dat":                  []byte("level"),
			"Bingo/drehmal/datapacks/hi_drehmal.zip":   hi,
			"Bingo/drehmal/datapacks/emis/pack.mcmeta": []byte(`{"pack":{"description":"Emi's"}}`),
		})
		if err := os.MkdirAll(filepath.Join(data, ".avsys"), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bsdtar", "-cf", filepath.Join(data, ".avsys", "import.archive"), "-C", src, "Bingo").CombinedOutput(); err != nil {
			t.Fatalf("bsdtar: %v: %s", err, out)
		}
		script := regexp.MustCompile(regexp.QuoteMeta(dataRoot)+`\b`).ReplaceAllString(serverScript, data)
		cmd := exec.Command("sh", "-c", script)
		line := ""
		if listed {
			line = "listed"
		}
		cmd.Stdin = strings.NewReader("Bingo\n\n" + line + "\n\n\n\n\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script: %v: %s", err, out)
		}
		p.fileServer(data)
		p.srv.catalog.http.Transport = &hashModrinth{
			versions: map[string]string{sha1Of(bingo): modrinthVersionOf("V1", "P1", "2.9.6+mc1.20.1", "bingo-2.9.6+mc1.20.1.jar", bingo)},
			projects: `[{"id":"P1","slug":"yet-another-bingo","title":"Yet Another Bingo","icon_url":"https://cdn.modrinth.com/bingo.png"}]`,
		}
		// As the import ends.
		key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}
		if err := p.srv.markImporting(context.Background(), key, ""); err != nil {
			t.Fatal(err)
		}
		p.srv.lookAtImported(context.Background(), key)

		_, view := p.call("viewer", "GET", "/orgs/alpha/servers/world", "")
		var got []string
		for _, m := range view["spec"].(map[string]any)["mods"].([]any) {
			m := m.(map[string]any)
			got = append(got, m["source"].(string)+" "+m["kind"].(string)+" "+stringOr(m["title"])+" "+stringOr(m["versionName"]))
		}
		want := []string{"upload mod WI Zoom 1.5", "modrinth mod Yet Another Bingo 2.9.6+mc1.20.1", "upload plugin Essentials 2.20.1", "upload datapack emis ", "upload datapack hi_drehmal "}
		if listed {
			// Its jars and datapack files are left out, for the list to
			// install; a datapack in a folder stays.
			want = []string{"upload datapack emis "}
		}
		if strings.Join(got, ", ") != strings.Join(want, ", ") {
			t.Errorf("listed=%v: %q, want %q", listed, got, want)
		}
		if found := view["found"].([]any); len(found) != 0 {
			t.Errorf("listed=%v: left found: %v", listed, found)
		}
	}
}

func stringOr(v any) string {
	s, _ := v.(string)
	return s
}

// No more than maxFound files are kept, whether read now or before, and
// no folder is read once there are.
func TestFoundKeptToTheLimit(t *testing.T) {
	p := newPanel(t)
	was := maxFound
	maxFound = 3
	t.Cleanup(func() { maxFound = was })
	p.srv.catalog.http.Transport = &hashModrinth{}
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"mods/a.jar": []byte("a"), "mods/b.jar": []byte("b"), "mods/c.jar": []byte("c"), "mods/d.jar": []byte("d"),
		"plugins/e.jar": []byte("e"),
	})
	spec := &avsysv1.MinecraftServerSpec{}
	ctx := context.Background()
	first, err := p.srv.findContent(ctx, "test", DirFiles(root), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("first look: %v", pathsOf(first))
	}
	for range 3 {
		again, err := p.srv.findContent(ctx, "test", DirFiles(root), spec, first)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 3 {
			t.Fatalf("looked at again with what was found: %v", pathsOf(again))
		}
		first = again
	}
}

// Two servers' datapack folders of one name and time have each their own
// picture: one server's is never shown for another's.
func TestFoundPicturesKeptApart(t *testing.T) {
	p := newPanel(t)
	p.addServer("alpha", "other", "other.alpha.example")
	p.srv.catalog.http.Transport = &hashModrinth{}
	pictureOf := func(c color.NRGBA) string {
		img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		for i := range 16 {
			img.Set(i%4, i/4, c)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	roots := map[string]string{"world": t.TempDir(), "other": t.TempDir()}
	pictures := map[string]string{"world": pictureOf(color.NRGBA{R: 255, A: 255}), "other": pictureOf(color.NRGBA{B: 255, A: 255})}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for name, root := range roots {
		lay(t, root, map[string][]byte{"world/datapacks/same/pack.mcmeta": []byte(`{"pack":{"description":"x"}}`), "world/datapacks/same/pack.png": []byte(pictures[name])})
		if err := os.Chtimes(filepath.Join(root, "world/datapacks/same"), when, when); err != nil {
			t.Fatal(err)
		}
	}
	p.srv.ContentFiles = func(_ context.Context, namespace, name string) (ServerFiles, error) {
		if root, ok := roots[name]; ok && namespace == p.srv.Namespace("alpha") {
			return DirFiles(root), nil
		}
		return nil, errors.New("not reachable")
	}
	for name := range roots {
		if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/"+name+"/content/look?wait=true", ""); code != http.StatusOK {
			t.Fatalf("look at %s: %d", name, code)
		}
	}
	for name := range roots {
		rec := p.send("viewer", "GET", "/orgs/alpha/servers/"+name+"/content/icon?kind=datapack&name=same", nil)
		want, err := squarePNG(strings.NewReader(pictures[name]), foundIconSide)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s's picture: %d, its own: %v", name, rec.Code, bytes.Equal(rec.Body.Bytes(), want))
		}
	}
}

// A file Modrinth knew, whose project's name could not be had then, is
// named once it can be: by a panel started since too, which does not ask
// about the file again.
func TestFoundNamedOnceModrinthAnswers(t *testing.T) {
	p := newPanel(t)
	bingo := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"bingo","version":"2.9.6"}`})
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/bingo.jar": bingo})
	modrinth := &hashModrinth{
		versions:   map[string]string{sha1Of(bingo): `{"id":"V1","project_id":"P1","version_number":"2.9.6+mc1.20.1"}`},
		projects:   `[{"id":"P1","slug":"yet-another-bingo","title":"Yet Another Bingo","icon_url":"https://cdn.modrinth.com/bingo.png"}]`,
		noProjects: true,
	}
	p.srv.catalog.http.Transport = modrinth
	ctx := context.Background()
	spec := &avsysv1.MinecraftServerSpec{}
	found, err := p.srv.findContent(ctx, "test", DirFiles(root), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := found[0]; f.Project != "P1" || f.Title != "bingo" || f.IconURL != "" {
		t.Fatalf("projects not answered: %+v", f)
	}
	// Started again, with Modrinth answering.
	p.srv.recognitions.Clear()
	modrinth.noProjects = false
	again, err := p.srv.findContent(ctx, "test", DirFiles(root), spec, found)
	if err != nil {
		t.Fatal(err)
	}
	if f := again[0]; f.Title != "Yet Another Bingo" || f.Slug != "yet-another-bingo" || f.IconURL != "https://cdn.modrinth.com/bingo.png" {
		t.Errorf("once answered: %+v", f)
	}
	if modrinth.asked != 1 {
		t.Errorf("asked about the file again: %d", modrinth.asked)
	}
}

// A port opened for a mod found in the server's folders stays when the
// content the panel lists is changed: the mod is still there.
func TestFoundModsKeepTheirPorts(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = offline{
		"https://api.modrinth.com/v2/version/g1": `{"id":"g1","version_number":"2.12.1","files":[]}`,
	}
	key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}
	if err := p.srv.keepFound(context.Background(), key, func([]FoundFile) []FoundFile {
		return []FoundFile{{Path: "mods/voicechat.jar", Kind: avsysv1.KindMod, Project: "9eGKb6K1", VersionID: "vc1", Title: "Simple Voice Chat"}}
	}); err != nil {
		t.Fatal(err)
	}
	patch := func(body string) []avsysv1.ExtraPort {
		t.Helper()
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", body); code != 200 {
			t.Fatalf("%s: %d %s", body, code, msg)
		}
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(context.Background(), key, mc); err != nil {
			t.Fatal(err)
		}
		return mc.Spec.Ports
	}
	patch(`{"spec":{"ports":[{"name":"voice-chat","port":24454,"protocol":"UDP","for":"simple-voice-chat"}]}}`)
	const geyser = `{"source":"modrinth","kind":"mod","project":"geyser","version":"g1","title":"Geyser","iconUrl":"https://cdn.example/geyser.png"}`
	if got := patch(`{"spec":{"mods":[` + geyser + `]}}`); len(got) != 3 || got[0].Name != "voice-chat" {
		t.Errorf("a mod added: %+v", got)
	}
	if got := patch(`{"spec":{"mods":[]}}`); len(got) != 1 || got[0].Name != "voice-chat" {
		t.Errorf("a mod taken off: %+v", got)
	}
}

// cutFiles are a server's files whose reads of some files stop short, as
// when the connection to its workspace is dropped part of the way: the
// cut-th time a file is opened, and after, it ends early, with no error.
type cutFiles struct {
	ServerFiles
	mu    sync.Mutex
	cut   map[string]int
	opens map[string]int
}

type shortFile struct {
	ServerFile
	left int64
}

func (f *shortFile) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > f.left {
		p = p[:f.left]
	}
	n, err := f.ServerFile.Read(p)
	f.left -= int64(n)
	return n, err
}

func (c *cutFiles) Open(p string) (ServerFile, error) {
	f, err := c.ServerFiles.Open(p)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens[p]++
	if at, ok := c.cut[p]; ok && c.opens[p] >= at {
		return &shortFile{ServerFile: f, left: 100}, nil
	}
	return f, nil
}

// A datapack read only part of the way, as over a connection dropped while
// it is read, is never kept in place of the whole one: nothing is written
// beside it for the image to copy over it, and it is listed only once it
// has been read whole.
func TestFoundReadCutShort(t *testing.T) {
	big := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Emi's"}}`, "data/x.json": strings.Repeat("emi ", 4000)})
	for _, cutAt := range []int{1, 2} {
		p := newPanel(t)
		root := t.TempDir()
		lay(t, root, map[string][]byte{"world/datapacks/emis.zip": big})
		cut := &cutFiles{ServerFiles: DirFiles(root), cut: map[string]int{dataRoot + "/world/datapacks/emis.zip": cutAt}, opens: map[string]int{}}
		p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return cut, nil }
		p.srv.catalog.http.Transport = &hashModrinth{}
		if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
			t.Fatalf("look: %d", code)
		}
		// The datapack as it was.
		if got, _ := os.ReadFile(filepath.Join(root, "world/datapacks/emis.zip")); !bytes.Equal(got, big) {
			t.Errorf("cut at open %d: the datapack changed", cutAt)
		}
		// No copy of it for the image to put over it.
		if copied, err := os.ReadFile(filepath.Join(root, ".avsys/content/emis.zip")); err == nil {
			t.Errorf("cut at open %d: a copy of %d bytes of %d kept", cutAt, len(copied), len(big))
		}
		mc := p.serverOf("world")
		for _, m := range mc.Spec.Mods {
			if m.Project == "emis.zip" && m.File != "world/datapacks/emis.zip" {
				t.Errorf("cut at open %d: listed as one the image is handed: %+v", cutAt, m)
			}
		}
		// Read whole the next time, it is listed, where it is.
		cut.mu.Lock()
		cut.cut = map[string]int{}
		cut.mu.Unlock()
		st := p.srv.foundStateOf(client.ObjectKeyFromObject(mc))
		st.mu.Lock()
		st.last = time.Time{}
		st.mu.Unlock()
		p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
		if got := p.listedOn("world"); strings.Join(got, ", ") != "upload datapack emis.zip" {
			t.Errorf("cut at open %d, then read whole: listed %q", cutAt, got)
		}
	}
}

// slowFiles are a server's files that are slow to open until let go.
type slowFiles struct {
	ServerFiles
	release chan struct{}
}

func (s slowFiles) Open(p string) (ServerFile, error) {
	<-s.release
	return s.ServerFiles.Open(p)
}

// Asked for by a page, a look answers at once, and goes on when the page
// is left: what it finds comes as an event. Asked to wait, and cut short,
// it goes on too.
func TestFoundLookOutlivesItsRequest(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("a")})
	slow := slowFiles{ServerFiles: DirFiles(root), release: make(chan struct{})}
	p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return slow, nil }
	p.srv.catalog.http.Transport = &hashModrinth{}
	events := p.srv.Events.subscribe()
	defer p.srv.Events.unsubscribe(events)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/v1/orgs/alpha/servers/world/content/look?wait=true", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
	rec := httptest.NewRecorder()
	answered := make(chan struct{})
	go func() {
		p.h.ServeHTTP(rec, req)
		close(answered)
	}()
	time.Sleep(50 * time.Millisecond)
	// The page is left.
	cancel()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not let go")
	}
	if got := p.listedOn("world"); len(got) != 0 {
		t.Fatalf("listed before the files were read: %q", got)
	}
	close(slow.release)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type != "server.updated" {
				continue
			}
			if got := p.listedOn("world"); strings.Join(got, ", ") == "upload mod a.jar" {
				// Asked again a moment later, it answers at once.
				if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look", ""); code != http.StatusOK {
					t.Errorf("asked again at once: %d", code)
				}
				return
			}
		case <-deadline:
			t.Fatalf("the look did not finish: listed %q", p.listedOn("world"))
		}
	}
}

// A datapack says what it is, in its pack.mcmeta, and shows its pack.png:
// one taken from the server's world, as a file or a folder, and one
// uploaded from the page alike. One without a pack.png has no picture.
func TestDatapacksDescribedAndPictured(t *testing.T) {
	p := newPanel(t)
	icon := aPicture(t)
	bern := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Bern's trades"}}`, "pack.png": icon})
	plain := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":[{"text":"Hi, "},{"text":"Drehmal"}]}}`})
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"world/datapacks/bern.zip":         bern,
		"world/datapacks/hi_drehmal.zip":   plain,
		"world/datapacks/mael/pack.mcmeta": []byte(`{"pack":{"description":"Mael's"}}`),
		"world/datapacks/mael/pack.png":    []byte(icon),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	described := map[string]string{}
	for _, m := range p.serverOf("world").Spec.Mods {
		described[m.Project] = m.Description
	}
	if want := map[string]string{"bern.zip": "Bern's trades", "hi_drehmal.zip": "Hi, Drehmal", "mael": "Mael's"}; !reflect.DeepEqual(described, want) {
		t.Errorf("described %v, want %v", described, want)
	}
	for name, want := range map[string]int{"bern.zip": 200, "mael": 200, "hi_drehmal.zip": 404} {
		if rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/icon?kind=datapack&name="+name, nil); rec.Code != want {
			t.Errorf("%s's picture: %d, want %d", name, rec.Code, want)
		}
	}

	// Uploaded from the page, to a server reached through its workspace.
	q := newPanel(t)
	filesFrom(t, q, sftp.InMemHandler())
	rec := q.send("admin", "PUT", "/orgs/alpha/servers/world/content?kind=datapack&name=bern.zip", bern)
	if rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	mods := q.serverOf("world").Spec.Mods
	if len(mods) != 1 || mods[0].Description != "Bern's trades" || mods[0].File != "" {
		t.Errorf("uploaded: %+v", mods)
	}
	if rec := q.send("viewer", "GET", "/orgs/alpha/servers/world/content/icon?kind=datapack&name=bern.zip", nil); rec.Code != 200 {
		t.Errorf("an uploaded datapack's picture: %d", rec.Code)
	}
}

// A server whose list was changed outside the panel, as by kubectl, a
// restore or a template, while the panel still notes files it took into
// the list: nothing is deleted for it. Files no longer on the list are
// found again, and taken in again.
func TestFoundNotDeletedForAListChangedElsewhere(t *testing.T) {
	p := newPanel(t)
	bern := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Bern's trades"}}`})
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"world/datapacks/bern.zip":         bern,
		"world/datapacks/mael/pack.mcmeta": []byte(`{"pack":{"description":"Mael's"}}`),
		"mods/a.jar":                       []byte("a"),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	// As an earlier import left it: noted, then the list emptied by hand.
	mc := p.serverOf("world")
	mc.Annotations = map[string]string{avsysv1.AnnotationImported: `{"world/datapacks/bern.zip":"upload:datapack/bern.zip","world/datapacks/mael":"upload:datapack/mael","mods/a.jar":"upload:mod/a.jar"}`}
	mc.Spec.Mods = nil
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, name := range []string{"world/datapacks/bern.zip", "world/datapacks/mael/pack.mcmeta", "mods/a.jar"} {
		if !exists(t, filepath.Join(root, name)) {
			t.Errorf("%s was deleted", name)
		}
	}
	if got := p.listedOn("world"); strings.Join(got, ", ") != "upload mod a.jar, upload datapack bern.zip, upload datapack mael" {
		t.Errorf("listed %q", got)
	}
}

// Taken off through the panel, a file taken from the server's folders is
// deleted only while it is the file that was taken in: one changed since
// stays, and is found and taken in again.
func TestFoundRemovedOnlyAsItWas(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("a"), "mods/b.jar": []byte("b")})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	look := func() {
		t.Helper()
		st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"})
		st.mu.Lock()
		st.last = time.Time{}
		st.mu.Unlock()
		if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
			t.Fatal(code)
		}
	}
	look()
	if got := p.listedOn("world"); strings.Join(got, ", ") != "upload mod a.jar, upload mod b.jar" {
		t.Fatalf("listed %q", got)
	}
	// a.jar is replaced by hand; then both are taken off.
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("another a")})
	if code, msg := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"mods":[]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "mods/a.jar")); string(got) != "another a" {
		t.Errorf("a.jar, changed since it was taken in: %q", got)
	}
	if exists(t, filepath.Join(root, "mods/b.jar")) {
		t.Error("b.jar, as it was taken in, is still there")
	}
	if got := removalsOf(p.serverOf("world")); len(got) != 0 {
		t.Errorf("removals left: %+v", got)
	}
	look()
	if got := p.listedOn("world"); strings.Join(got, ", ") != "upload mod a.jar" {
		t.Errorf("listed again %q", got)
	}
}

// Taken off while the server's files cannot be reached, a file is noted to
// go, is not taken in again meanwhile, and goes once they can be.
func TestFoundRemovedOnceReachable(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"world/datapacks/mael/pack.mcmeta": []byte(`{"pack":{"description":"Mael's"}}`)})
	reachable := true
	p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) {
		if !reachable {
			return nil, errors.New("not reachable")
		}
		return DirFiles(root), nil
	}
	p.srv.catalog.http.Transport = &hashModrinth{}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	if got := p.listedOn("world"); strings.Join(got, ", ") != "upload datapack mael" {
		t.Fatalf("listed %q", got)
	}
	reachable = false
	if code, msg := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"mods":[]}}`); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	if got := removalsOf(p.serverOf("world")); len(got) != 1 || got[0].Path != "world/datapacks/mael" || got[0].SHA1 == "" {
		t.Fatalf("noted to go: %+v", got)
	}
	reachable = true
	st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"})
	st.mu.Lock()
	st.last = time.Time{}
	st.mu.Unlock()
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	if exists(t, filepath.Join(root, "world/datapacks/mael")) {
		t.Error("the datapack is still there")
	}
	if got := p.listedOn("world"); len(got) != 0 {
		t.Errorf("taken in again: %q", got)
	}
	if got := removalsOf(p.serverOf("world")); len(got) != 0 {
		t.Errorf("still noted: %+v", got)
	}
}

// A file left found, removed from the page, is deleted only while it is
// the file that was found.
func TestFoundLeftRemovedOnlyAsItWas(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/bad,name.jar": []byte("x")})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	lay(t, root, map[string][]byte{"mods/bad,name.jar": []byte("y")})
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/servers/world/content/found?path="+url.QueryEscape("mods/bad,name.jar"), ""); code != http.StatusConflict {
		t.Errorf("removing one changed since: %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "mods/bad,name.jar")); string(got) != "y" {
		t.Errorf("the file: %q", got)
	}
}

// Content taken from the server's folders keeps where it is kept through
// any change to the list: a request that sends an entry back without its
// place, as an older page or the API might, does not make it an upload
// kept where uploads are, which the image would be handed and which is not
// there. Nor can a request give an upload a place of its own.
func TestFoundKeepsItsPlaceThroughChanges(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{
		"world/datapacks/bern.zip":       jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Bern's trades"}}`}),
		"world/datapacks/emis.zip":       jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Animations"}}`}),
		"world/datapacks/hi_drehmal.zip": jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Hi"}}`}),
		"mods/WI-Zoom.jar":               []byte("zoom"),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	// bern taken off, the rest sent back without where they are, and one
	// given a place it does not have.
	var mods []map[string]any
	for _, m := range p.serverOf("world").Spec.Mods {
		if m.Project == "bern.zip" {
			continue
		}
		entry := map[string]any{"source": m.Source, "kind": m.Kind, "project": m.Project, "title": m.Title, "description": m.Description}
		if m.Project == "WI-Zoom.jar" {
			entry["file"] = "mods/other.jar"
		}
		mods = append(mods, entry)
	}
	raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"mods": mods}})
	if code, msg := p.do("admin", "PATCH", "/orgs/alpha/servers/world", string(raw)); code != 200 {
		t.Fatalf("patch: %d %s", code, msg)
	}
	places := map[string]string{}
	for _, m := range p.serverOf("world").Spec.Mods {
		places[m.Project] = m.File
		if m.Description == "" && m.Kind == avsysv1.KindDatapack {
			t.Errorf("%s lost its description", m.Project)
		}
	}
	if want := map[string]string{"emis.zip": "world/datapacks/emis.zip", "hi_drehmal.zip": "world/datapacks/hi_drehmal.zip", "WI-Zoom.jar": "mods/WI-Zoom.jar"}; !reflect.DeepEqual(places, want) {
		t.Errorf("kept at %v, want %v", places, want)
	}
	if exists(t, filepath.Join(root, "world/datapacks/bern.zip")) || !exists(t, filepath.Join(root, "world/datapacks/emis.zip")) {
		t.Error("not only bern was deleted")
	}
	// A new upload cannot be given a place by a request.
	raw, _ = json.Marshal(map[string]any{"spec": map[string]any{"mods": append(mods, map[string]any{"source": "upload", "kind": "datapack", "project": "level.zip", "file": "world/datapacks/level.zip"})}})
	p.do("admin", "PATCH", "/orgs/alpha/servers/world", string(raw))
	for _, m := range p.serverOf("world").Spec.Mods {
		if m.Project == "level.zip" && m.File != "" {
			t.Errorf("a request gave an upload a place: %+v", m)
		}
	}
}

// An upload listed without its place, as an earlier version of the panel
// could leave one it had taken in, whose copy is not where uploads are
// kept, is put back in the place it was taken from.
func TestFoundPlaceRepaired(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"world/datapacks/emis.zip": []byte("emis"), "mods/kept.jar": []byte("k")})
	lay(t, root, map[string][]byte{".avsys/content/kept.jar": []byte("k")})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	mc := p.serverOf("world")
	mc.Spec.Mods = []avsysv1.ContentRef{
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "emis.zip", Title: "emis"},
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindMod, Project: "kept.jar"},
	}
	mc.Annotations = map[string]string{avsysv1.AnnotationImported: `{"world/datapacks/emis.zip":{"key":"upload:datapack/emis.zip","sha1":"x"},"mods/kept.jar":{"key":"upload:mod/kept.jar"}}`}
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look?wait=true", "")
	places := map[string]string{}
	for _, m := range p.serverOf("world").Spec.Mods {
		places[m.Project] = m.File
	}
	// kept.jar has its copy where uploads are kept: it is such an upload.
	if want := map[string]string{"emis.zip": "world/datapacks/emis.zip", "kept.jar": ""}; !reflect.DeepEqual(places, want) {
		t.Errorf("kept at %v, want %v", places, want)
	}
}

// What a file says of itself is shown as one line of plain text: the
// game's formatting codes go, and line breaks, tabs and every other control
// character become spaces. Nothing in what the panel answers then needs
// more than plain escapes, however it is passed on.
func TestSaidOnOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"§6§lVeinminer §7| §61.21+\n§eBy Miraculixx": "Veinminer | 1.21+ By Miraculixx",
		"Tabbed\tName\r\n":                  "Tabbed Name",
		"Bell\a and nul\x00 and esc\x1b[0m": "Bell and nul and esc [0m",
		"line sep para\u0085next":           "line sep para next",
		"  plain  ":                         "plain",
		"ends with §":                       "ends with",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
	// From the files themselves, and as the API answers.
	pack := jarOf(t, map[string]string{"pack.mcmeta": "{\r\n \"pack\": {\"pack_format\": 48, \"description\": \"§6§lVeinminer §7| §61.21+\\n§eBy Miraculixx\"}\r\n}"})
	mod := jarOf(t, map[string]string{"fabric.mod.json": "{\"name\":\"Multi\\nLine\\tMod\",\"version\":\"1.0\\u0007\"}"})
	p := newPanel(t)
	filesFrom(t, p, sftp.InMemHandler())
	for _, up := range []struct {
		kind, name string
		body       []byte
	}{{"datapack", "Veinminer-1.2.4.zip", pack}, {"mod", "multi.jar", mod}} {
		rec := p.send("admin", "PUT", "/orgs/alpha/servers/world/content?kind="+up.kind+"&name="+up.name, up.body)
		if rec.Code != 200 {
			t.Fatalf("upload %s: %d %s", up.name, rec.Code, rec.Body)
		}
		if bytes.Contains(rec.Body.Bytes(), []byte(`\n`)) || bytes.Contains(rec.Body.Bytes(), []byte(`\t`)) || bytes.Contains(rec.Body.Bytes(), []byte(`\u00`)) {
			t.Errorf("answered with an escaped control character: %s", rec.Body)
		}
	}
	got := map[string]string{}
	for _, m := range p.serverOf("world").Spec.Mods {
		got[m.Project] = m.Title + " / " + m.VersionName + " / " + m.Description
	}
	if want := map[string]string{"Veinminer-1.2.4.zip": "Veinminer-1.2.4.zip /  / Veinminer | 1.21+ By Miraculixx", "multi.jar": "Multi Line Mod / 1.0 / "}; !reflect.DeepEqual(got, want) {
		t.Errorf("listed %q, want %q", got, want)
	}
}

// What a server's software keeps in its content folders by itself is not
// content: on Paper, its remapped copies of plugins, plugins' own folders,
// bStats and update folders, and Bukkit's datapack in each world; on Forge
// and NeoForge, folders in mods; a folder without a pack.mcmeta is no
// datapack. A server restored from a snapshot with only its listed content
// and these has nothing taken into its list after.
func TestFoundLeavesWhatTheSoftwareKeeps(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lp, we := []byte("luckperms jar"), []byte("worldedit jar")
	lay(t, root, map[string][]byte{
		// Paper, with two plugins from Modrinth, as the image installed them.
		".modrinth-manifest.json":                          []byte(`{"files":["plugins/LuckPerms-Bukkit-5.4.jar","plugins/worldedit-bukkit-7.3.jar"],"projects":["luckperms:lp1","worldedit:we1"]}`),
		"plugins/LuckPerms-Bukkit-5.4.jar":                 lp,
		"plugins/worldedit-bukkit-7.3.jar":                 we,
		"plugins/.paper-remapped/LuckPerms-Bukkit-5.4.jar": lp,
		"plugins/.paper-remapped/index.json":               []byte("{}"),
		"plugins/.paper-remapped/unknown-origin/x.jar":     we,
		"plugins/LuckPerms/config.yml":                     []byte("x"),
		"plugins/LuckPerms/libs/h2.jar":                    []byte("lib"),
		"plugins/bStats/config.yml":                        []byte("x"),
		"plugins/update/WorldEdit.jar":                     we,
		"plugins/PluginMetrics/config.yml":                 []byte("x"),
		"world/datapacks/bukkit/pack.mcmeta":               []byte(`{"pack":{"description":"Data pack for resources provided by Bukkit plugins","pack_format":61}}`),
		"world/datapacks/notes/readme.txt":                 []byte("not a datapack"),
		"world/level.dat":                                  []byte("level"),
		// Forge and NeoForge, Fabric.
		"mods/1.20.1/old.jar":        []byte("x"),
		"mods/.connector/cache.jar":  []byte("x"),
		"mods/.index/x.json":         []byte("x"),
		".fabric/remappedJars/x.jar": []byte("x"),
		"config/x.toml":              []byte("x"),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	ctx := context.Background()
	key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}
	mc := p.serverOf("world")
	mc.Spec.Loader = avsysv1.LoaderPaper
	mc.Spec.Mods = []avsysv1.ContentRef{
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Version: "lp1"},
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "worldedit", Version: "we1"},
	}
	if err := p.srv.Client.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}
	// As a restore ends.
	if err := p.srv.markRestoring(ctx, key, "abcd1234"); err != nil {
		t.Fatal(err)
	}
	if err := p.srv.markRestoring(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	p.srv.lookRoundOnce(ctx)
	if got := p.listedOn("world"); strings.Join(got, ", ") != "modrinth plugin luckperms@lp1, modrinth plugin worldedit@we1" {
		t.Errorf("listed %q", got)
	}
	if got := p.foundOn("world"); len(got) != 0 {
		t.Errorf("found %v", pathsOf(got))
	}
}
