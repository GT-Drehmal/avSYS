package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// class returns the first bytes of a class file compiled for the given
// class file version.
func class(major uint16) []byte {
	b := binary.BigEndian.AppendUint32(nil, 0xCAFEBABE)
	b = binary.BigEndian.AppendUint16(b, 0)
	return binary.BigEndian.AppendUint16(b, major)
}

func jar(t *testing.T, files map[string][]byte) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestJavaOfJar(t *testing.T) {
	cases := []struct {
		name  string
		files map[string][]byte
		want  int
	}{
		{"java 21 plugin", map[string][]byte{"com/example/Plugin.class": class(65), "plugin.yml": []byte("name: x")}, 21},
		{"newest class decides", map[string][]byte{"a/Old.class": class(52), "a/New.class": class(69)}, 25},
		// Classes for newer JVMs under META-INF/versions are optional extras.
		{"multi-release extras ignored", map[string][]byte{"a/Main.class": class(61), "META-INF/versions/25/a/Main.class": class(69)}, 17},
		{"module descriptor ignored", map[string][]byte{"a/Main.class": class(52), "module-info.class": class(65)}, 8},
		{"datapack has no classes", map[string][]byte{"pack.mcmeta": []byte("{}"), "data/x/y.json": []byte("{}")}, 0},
		{"not really a class", map[string][]byte{"a/Fake.class": []byte("hello world")}, 0},
	}
	for _, c := range cases {
		r := jar(t, c.files)
		got, err := javaOfJar(r, r.Size())
		if err != nil || got != c.want {
			t.Errorf("%s: java = %d, %v; want %d", c.name, got, err, c.want)
		}
	}
	if _, err := javaOfJar(bytes.NewReader([]byte("not a zip")), 9); err == nil {
		t.Error("a file that is not a zip should be an error")
	}
}

func TestContentFilename(t *testing.T) {
	good := map[string]string{
		"worldedit-bukkit-7.4.2.jar":       "worldedit-bukkit-7.4.2.jar",
		"C:\\Users\\me\\My Mod (1).jar":    "My Mod (1).jar",
		"/home/me/mods/lithium+mc1.21.jar": "lithium+mc1.21.jar",
		// Commas and newlines would split the list the server image reads.
		"a,b\nc.jar": "a_b_c.jar",
	}
	for in, want := range good {
		if got, err := contentFilename(in, avsysv1.KindMod); err != nil || got != want {
			t.Errorf("contentFilename(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".jar", "notes.txt", "../../etc/passwd", ".hidden.jar"} {
		if got, err := contentFilename(bad, avsysv1.KindMod); err == nil {
			t.Errorf("contentFilename(%q) = %q, want an error", bad, got)
		}
	}
	if _, err := contentFilename("pack.zip", avsysv1.KindDatapack); err != nil {
		t.Errorf("a datapack zip was refused: %v", err)
	}
	if _, err := contentFilename("pack.jar", avsysv1.KindDatapack); err == nil {
		t.Error("a jar was accepted as a datapack")
	}
}

func TestPreferReleases(t *testing.T) {
	mixed := []modrinthVersion{{ID: "b", VersionType: "beta"}, {ID: "r", VersionType: "release"}, {ID: "a", VersionType: "alpha"}}
	if got := preferReleases(mixed); len(got) != 1 || got[0].ID != "r" {
		t.Errorf("mixed: got %+v", got)
	}
	onlyBeta := []modrinthVersion{{ID: "b1", VersionType: "beta"}, {ID: "b2", VersionType: "beta"}}
	if got := preferReleases(onlyBeta); len(got) != 2 {
		t.Errorf("with no release, betas should be kept: got %+v", got)
	}
}

// What is deleted for content taken off a server: an upload's stored file,
// and a datapack's installed one when it came from a URL or an upload. What
// the image removes itself, and what is still listed, is left alone.
func TestLeftBehind(t *testing.T) {
	url := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: "https://cdn.example.org/packs/Veinminer-1.2.4.zip?x=1"}
	upload := avsysv1.ContentRef{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "ours.zip"}
	uploadedMod := avsysv1.ContentRef{Source: avsysv1.SourceUpload, Kind: avsysv1.KindMod, Project: "mod.jar"}
	modrinth := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindDatapack, Project: "terralith", Version: "abc"}
	urlMod := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindMod, Project: "https://example.org/mod.jar"}
	before := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{url, upload, uploadedMod, modrinth, urlMod}}
	level := func(name string, mods ...avsysv1.ContentRef) *avsysv1.MinecraftServerSpec {
		return &avsysv1.MinecraftServerSpec{Env: []corev1.EnvVar{{Name: "LEVEL", Value: name}}, Mods: mods}
	}

	got := leftBehind(&avsysv1.MinecraftServerSpec{}, before)
	want := []string{
		"/data/world/datapacks/Veinminer-1.2.4.zip",
		"/data/.avsys/content/ours.zip",
		"/data/world/datapacks/ours.zip",
		"/data/.avsys/content/mod.jar",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("all taken off:\n got %v\nwant %v", got, want)
	}
	// Still listed: nothing.
	if got := leftBehind(before, before); len(got) != 0 {
		t.Errorf("nothing taken off: %v", got)
	}
	// A world of another name, by the server's own LEVEL.
	if got := leftBehind(level("drehmal"), level("drehmal", url)); len(got) != 1 || got[0] != "/data/drehmal/datapacks/Veinminer-1.2.4.zip" {
		t.Errorf("with LEVEL: %v", got)
	}
	// Taken off as the world changes: from where it was put.
	if got := leftBehind(level("newworld"), level("oldworld", url)); len(got) != 1 || got[0] != "/data/oldworld/datapacks/Veinminer-1.2.4.zip" {
		t.Errorf("with LEVEL changed: %v", got)
	}
	// A LEVEL that would lead outside /data, or that is read from elsewhere,
	// is not guessed at: the datapack's file is not deleted.
	if got := leftBehind(level("../etc"), level("../etc", url)); len(got) != 0 {
		t.Errorf("with a LEVEL outside: %v", got)
	}
	secret := &avsysv1.MinecraftServerSpec{Env: []corev1.EnvVar{{Name: "LEVEL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "level"}}}}}
	if got := leftBehind(secret, &avsysv1.MinecraftServerSpec{Env: secret.Env, Mods: []avsysv1.ContentRef{url, upload}}); len(got) != 1 || got[0] != "/data/.avsys/content/ours.zip" {
		t.Errorf("with LEVEL from a secret: %v", got)
	}
	// A URL that names no zip file is not guessed at...
	odd := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: "https://example.org/download?id=7"}
	if got := leftBehind(&avsysv1.MinecraftServerSpec{}, &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{odd}}); len(got) != 0 {
		t.Errorf("a URL with no file name: %v", got)
	}
	// ...but the name its download gave it, recorded, is used.
	odd.File = "pack-v2.zip"
	if got := leftBehind(&avsysv1.MinecraftServerSpec{}, &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{odd}}); len(got) != 1 || got[0] != "/data/world/datapacks/pack-v2.zip" {
		t.Errorf("a URL with its file recorded: %v", got)
	}
	odd.File = "../server.properties"
	if got := leftBehind(&avsysv1.MinecraftServerSpec{}, &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{odd}}); len(got) != 0 {
		t.Errorf("a recorded file outside the folder: %v", got)
	}
	// A file another datapack still listed is in stays.
	sameName := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: "https://example.org/ours.zip"}
	both := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{upload, sameName}}
	if got := leftBehind(&avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{sameName}}, both); len(got) != 1 || got[0] != "/data/.avsys/content/ours.zip" {
		t.Errorf("a file still used: %v", got)
	}
}

// Content that stops being a datapack has its installed datapack deleted;
// an upload's stored file stays while the upload is listed as anything.
func TestLeftBehindKindChange(t *testing.T) {
	url := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: "https://example.org/thing.zip"}
	upload := avsysv1.ContentRef{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "ours.zip"}
	before := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{url, upload}}
	url.Kind, upload.Kind = avsysv1.KindMod, avsysv1.KindPlugin
	got := leftBehind(&avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{url, upload}}, before)
	if want := "/data/world/datapacks/thing.zip /data/world/datapacks/ours.zip"; strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}
	// A world the image works out from a modpack is not guessed at.
	cf := []corev1.EnvVar{{Name: "CF_SET_LEVEL_FROM", Value: "WORLD_FROM_SAVES"}}
	if got := leftBehind(&avsysv1.MinecraftServerSpec{Env: cf}, &avsysv1.MinecraftServerSpec{Env: cf, Mods: before.Mods}); strings.Join(got, " ") != "/data/.avsys/content/ours.zip" {
		t.Errorf("with the level from a modpack: %v", got)
	}
}

// What a note of content left to remove may name: only files the panel puts
// there itself.
func TestContentLeftOf(t *testing.T) {
	note := strings.Join([]string{
		"/data/.avsys/content/ours.zip",
		"/data/world/datapacks/Veinminer-1.2.4.zip",
		"/data/drehmal/datapacks/pack.zip",
		"/data/server.properties",
		"/data/world/../server.properties",
		"/data/world/datapacks/../../server.properties",
		"/etc/passwd",
		"/data/a/b/datapacks/x.zip",
		"",
	}, "\n")
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{avsysv1.AnnotationContentLeft: note}}}
	want := "/data/.avsys/content/ours.zip /data/world/datapacks/Veinminer-1.2.4.zip /data/drehmal/datapacks/pack.zip"
	if got := strings.Join(contentLeftOf(mc), " "); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

// Content taken off while the server's files cannot be reached is noted on
// the server, to be removed when they can.
func TestContentLeftIsNoted(t *testing.T) {
	p := newPanel(t)
	p.addServer("alpha", "packs", "")
	ns := p.srv.Namespace("alpha")
	before := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "ours.zip"}}}
	p.srv.removeDroppedUploads(context.Background(), ns, "packs", &avsysv1.MinecraftServerSpec{}, before)
	var mc avsysv1.MinecraftServer
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "packs"}, &mc); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(contentLeftOf(&mc), " "); got != "/data/.avsys/content/ours.zip /data/world/datapacks/ours.zip" {
		t.Errorf("noted: %q", got)
	}
	// Noted again, nothing twice.
	p.srv.removeDroppedUploads(context.Background(), ns, "packs", &avsysv1.MinecraftServerSpec{}, before)
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "packs"}, &mc); err != nil {
		t.Fatal(err)
	}
	if got := len(contentLeftOf(&mc)); got != 2 {
		t.Errorf("noted twice: %d entries", got)
	}
}

// A datapack from a URL is recorded under the name its download saves it as.
func TestSavedName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/named/download.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="pack-v2.zip"`)
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/files/Real-1.0.zip?sig=x", http.StatusFound)
	})
	mux.HandleFunc("/files/Real-1.0.zip", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/sneaky.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="../../server.properties"`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// The panel only fetches from public addresses; the test's is not one.
	public := publicHTTP
	publicHTTP = srv.Client()
	defer func() { publicHTTP = public }()
	ctx := context.Background()
	for path, want := range map[string]string{
		"/named/download.zip": "pack-v2.zip",
		"/moved":              "Real-1.0.zip",
		"/sneaky.zip":         "server.properties",
		"/missing.zip":        "",
	} {
		if got := savedName(ctx, srv.URL+path); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// The file a datapack from a URL was saved as is the panel's to record: what
// a request says of it is not taken, as the panel deletes that file.
func TestDatapackFileIsRecordedByThePanel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="pack-v2.zip"`)
	}))
	defer srv.Close()
	public := publicHTTP
	publicHTTP = srv.Client()
	defer func() { publicHTTP = public }()

	kept := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: srv.URL + "/old.zip", File: "old-1.0.zip"}
	// Added before the name was recorded.
	older := avsysv1.ContentRef{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: srv.URL + "/older.zip"}
	previous := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{kept, older}}
	spec := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{
		{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: kept.Project, File: "server.properties"},
		{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: older.Project},
		{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: srv.URL + "/download.zip", File: "server.properties"},
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "ours.zip", File: "server.properties"},
	}}
	if err := (&Server{}).resolveContent(context.Background(), spec, previous); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"old-1.0.zip", "pack-v2.zip", "pack-v2.zip", ""} {
		if got := spec.Mods[i].File; got != want {
			t.Errorf("entry %d: file %q, want %q", i, got, want)
		}
	}
}
