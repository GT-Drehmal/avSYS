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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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

// The Content page asks for a look, and anyone who sees the server may; the
// view of the server says what is there. Only an admin removes one, which
// deletes its file, and only one found there.
func TestFoundListedAndRemoved(t *testing.T) {
	p := newPanel(t)
	icon := aPicture(t)
	root := t.TempDir()
	zoom := jarOf(t, map[string]string{"fabric.mod.json": `{"name":"WI Zoom","version":"1.5","icon":"icon.png"}`, "icon.png": icon})
	lay(t, root, map[string][]byte{
		"mods/WI-Zoom-1.5.jar":          zoom,
		"mods/c2me.jar":                 []byte("c2me"),
		"world/datapacks/a/pack.mcmeta": []byte(`{"pack":{"description":"A"}}`),
		"world/datapacks/b.zip":         []byte("b"),
		"world/level.dat":               []byte("not content"),
		"server.properties":             []byte("motd=hi\n"),
	})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}

	code, view := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look", "")
	if code != http.StatusOK {
		t.Fatalf("look: %d %v", code, view)
	}
	var paths []string
	for _, f := range view["found"].([]any) {
		paths = append(paths, f.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, " ") != "mods/WI-Zoom-1.5.jar mods/c2me.jar world/datapacks/a world/datapacks/b.zip" {
		t.Fatalf("found %v", paths)
	}
	// Its picture, drawn again by the panel, to whoever sees the server.
	rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/found/icon?path="+url.QueryEscape("mods/WI-Zoom-1.5.jar"), nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("its picture: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	// Kept by the panel only a while: read out of the file again.
	p.srv.foundIcons = foundIcons{}
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/found/icon?path="+url.QueryEscape("mods/WI-Zoom-1.5.jar"), nil); rec.Code != 200 {
		t.Errorf("its picture, read again: %d", rec.Code)
	}
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/found/icon?path="+url.QueryEscape("mods/c2me.jar"), nil); rec.Code != 404 {
		t.Errorf("one with no picture: %d", rec.Code)
	}

	remove := func(who, file string) int {
		code, _ := p.call(who, "DELETE", "/orgs/alpha/servers/world/content/found?path="+url.QueryEscape(file), "")
		return code
	}
	if code := remove("operator", "mods/c2me.jar"); code != http.StatusForbidden {
		t.Errorf("an operator removing: %d", code)
	}
	if code := remove("admin", "mods/c2me.jar"); code != http.StatusOK {
		t.Fatalf("an admin removing: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "mods/c2me.jar")); !os.IsNotExist(err) {
		t.Errorf("its file is still there: %v", err)
	}
	if code := remove("admin", "world/datapacks/a"); code != http.StatusOK {
		t.Fatalf("a datapack folder: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "world/datapacks/a")); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/WI-Zoom-1.5.jar world/datapacks/b.zip" {
		t.Errorf("found after removing: %s", got)
	}
	// Nothing else of the server's: only what was found.
	for _, file := range []string{"world/level.dat", "server.properties", "mods/c2me.jar", "../etc/passwd"} {
		if code := remove("admin", file); code != http.StatusNotFound {
			t.Errorf("removing %s: %d", file, code)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "world/level.dat")); err != nil {
		t.Errorf("the world: %v", err)
	}
	// Nor one the annotation was made to name elsewhere.
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
	if _, err := os.Stat(filepath.Join(root, "server.properties")); err != nil {
		t.Errorf("server.properties: %v", err)
	}
}

// A look that read the folders before a file was removed through the panel
// does not list it again once it is done.
func TestFoundRemovedDuringALook(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("a"), "mods/b.jar": []byte("b")})
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	ctx := context.Background()
	key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}
	if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/world/content/look", ""); code != http.StatusOK {
		t.Fatalf("look: %d", code)
	}
	// A look reads the folders; then a.jar is removed; then the look is done.
	started := time.Now()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, key, mc); err != nil {
		t.Fatal(err)
	}
	found, err := p.srv.findContent(ctx, "test", DirFiles(root), &mc.Spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/servers/world/content/found?path=mods/a.jar", ""); code != http.StatusOK {
		t.Fatalf("remove: %d", code)
	}
	st := p.srv.foundStateOf(key)
	if err := p.srv.keepFound(ctx, key, func([]FoundFile) []FoundFile { return st.without(found, started) }); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/b.jar" {
		t.Errorf("found %s", got)
	}
	// A look begun after it, which would not find it anyway, is not held to it.
	if got := pathsOf(st.without([]FoundFile{{Path: "mods/a.jar"}}, time.Now())); len(got) != 1 {
		t.Errorf("a later look: %v", got)
	}
}

// Content put in a server's folders behind the panel's back, over SFTP or
// the terminal, is found without anyone opening its page: at once when it
// came through the panel's files, and otherwise with the next look, which
// comes sooner once the server is started or stopped.
func TestFoundWithoutAsking(t *testing.T) {
	p := newPanel(t)
	root := t.TempDir()
	p.fileServer(root)
	p.srv.catalog.http.Transport = &hashModrinth{}
	ctx := context.Background()
	p.srv.lookRoundOnce(ctx)
	if got := p.foundOn("world"); len(got) != 0 {
		t.Fatalf("found in an empty server: %v", got)
	}
	lay(t, root, map[string][]byte{"mods/a.jar": []byte("a")})
	// Not due yet: looked at a moment ago, and nothing said it changed.
	p.srv.lookRoundOnce(ctx)
	if got := p.foundOn("world"); len(got) != 0 {
		t.Fatalf("looked at again at once: %v", got)
	}
	// Started: due.
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	p.srv.lookRoundOnce(ctx)
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/a.jar" {
		t.Fatalf("after it started: %s", got)
	}
	// A file put there through the panel's own files.
	lay(t, root, map[string][]byte{"plugins/b.jar": []byte("b")})
	p.srv.lookSoon(p.srv.Namespace("alpha"), "world")
	p.srv.lookRoundOnce(ctx)
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/a.jar plugins/b.jar" {
		t.Fatalf("after its files changed: %s", got)
	}
	// And after a while, whatever happened.
	lay(t, root, map[string][]byte{"mods/c.jar": []byte("c")})
	st := p.srv.foundStateOf(client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"})
	st.mu.Lock()
	st.last = time.Now().Add(-lookEvery - time.Second)
	st.mu.Unlock()
	p.srv.lookRoundOnce(ctx)
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/a.jar mods/c.jar plugins/b.jar" {
		t.Fatalf("a while later: %s", got)
	}
	// Not while its files are being put back: then once they are.
	if err := p.srv.markRestoring(ctx, client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}, "abcd1234"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "mods/c.jar"))
	p.srv.lookSoon(p.srv.Namespace("alpha"), "world")
	p.srv.lookRoundOnce(ctx)
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/a.jar mods/c.jar plugins/b.jar" {
		t.Fatalf("during a restore: %s", got)
	}
	if err := p.srv.markRestoring(ctx, client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}, ""); err != nil {
		t.Fatal(err)
	}
	p.srv.lookRoundOnce(ctx)
	if got := strings.Join(pathsOf(p.foundOn("world")), " "); got != "mods/a.jar plugins/b.jar" {
		t.Fatalf("after the restore: %s", got)
	}
}

// A server imported from an archive of its folder lists the mods, plugins
// and datapacks it brought as its content, once the import is done: as
// Modrinth knows them, or as they say. Those the form listed for the panel
// to install instead are not kept as files, and are not found twice.
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
			versions: map[string]string{sha1Of(bingo): `{"id":"V1","project_id":"P1","version_number":"2.9.6+mc1.20.1"}`},
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
		for _, f := range view["found"].([]any) {
			f := f.(map[string]any)
			got = append(got, f["kind"].(string)+" "+f["title"].(string)+" "+stringOr(f["version"]))
		}
		want := []string{"mod WI Zoom 1.5", "mod Yet Another Bingo 2.9.6+mc1.20.1", "plugin Essentials 2.20.1", "datapack emis ", "datapack hi_drehmal "}
		if listed {
			// Its jars and datapack files are left out, for the list to
			// install; a datapack in a folder stays.
			want = []string{"datapack emis "}
		}
		if strings.Join(got, ", ") != strings.Join(want, ", ") {
			t.Errorf("listed=%v: found %q, want %q", listed, got, want)
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
		if code, _ := p.call("viewer", "POST", "/orgs/alpha/servers/"+name+"/content/look", ""); code != http.StatusOK {
			t.Fatalf("look at %s: %d", name, code)
		}
	}
	for name := range roots {
		rec := p.send("viewer", "GET", "/orgs/alpha/servers/"+name+"/content/found/icon?path="+url.QueryEscape("world/datapacks/same"), nil)
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
