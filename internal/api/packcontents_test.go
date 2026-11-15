package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// packedIndex lists a mod for both sides from a Modrinth project, one for
// players only, and a resource pack from elsewhere.
const packedIndex = `{"formatVersion":1,"game":"minecraft","versionId":"2.0.1","name":"Probe Pack","files":[
{"path":"mods/lithium-fabric-0.15.3.jar","env":{"client":"required","server":"required"},"downloads":["https://cdn.modrinth.com/data/gvQqBUqZ/versions/abc/lithium-fabric-0.15.3.jar"]},
{"path":"mods/sodium-fabric-0.6.13.jar","env":{"client":"required","server":"unsupported"},"downloads":["https://cdn.modrinth.com/data/AANobbMI/versions/def/sodium-fabric-0.6.13.jar"]},
{"path":"resourcepacks/faithful.zip","downloads":["https://example.com/faithful.zip"]}
],"dependencies":{"minecraft":"1.21.4","fabric-loader":"0.16.10"}}`

// projectsAnswer is what Modrinth says of the projects in packedIndex.
const projectsAnswer = `[{"id":"gvQqBUqZ","title":"Lithium","icon_url":"https://cdn.modrinth.com/data/gvQqBUqZ/icon.png"},{"id":"AANobbMI","title":"Sodium","icon_url":"https://cdn.modrinth.com/data/AANobbMI/icon.png"}]`

// pngOf is a picture of a side.
func pngOf(t *testing.T, side int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for i := range side {
		img.Set(i, i, color.RGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// modrinthStandIn answers as Modrinth would: a version of a pack, its file
// (in parts, as asked, unless whole is set), and its projects. It counts
// what it is asked.
type modrinthStandIn struct {
	mu    sync.Mutex
	pack  []byte
	whole bool
	// broken fails each part of the pack asked for after its end.
	broken   bool
	asked    map[string]int
	sent     int
	versions map[string]string
}

func (m *modrinthStandIn) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := r.URL.String()
	key := r.URL.Host + r.URL.Path
	m.asked[key]++
	rec := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(u, "https://api.modrinth.com/v2/version/"):
		body, ok := m.versions[strings.TrimPrefix(r.URL.Path, "/v2/version/")]
		if !ok {
			rec.WriteHeader(http.StatusNotFound)
			break
		}
		rec.WriteString(body)
	case strings.HasPrefix(u, "https://api.modrinth.com/v2/projects?"):
		rec.WriteString(projectsAnswer)
	case strings.HasPrefix(u, "https://cdn.modrinth.com/") && strings.HasSuffix(u, ".mrpack"):
		if m.whole {
			r.Header.Del("Range")
		}
		if m.broken && !strings.HasPrefix(r.Header.Get("Range"), "bytes=-") {
			rec.WriteHeader(http.StatusBadGateway)
			break
		}
		http.ServeContent(rec, r, "", time.Time{}, bytes.NewReader(m.pack))
		m.sent += rec.Body.Len()
	default:
		rec.WriteHeader(http.StatusNotFound)
	}
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

func (m *modrinthStandIn) count(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.asked[key]
}

// contentsOf asks for a server's modpack's files.
func contentsOf(p *panel, who, server string) (int, []packEntry) {
	p.t.Helper()
	rec := p.send(who, "GET", "/orgs/alpha/servers/"+server+"/modpack/contents", nil)
	var out struct{ Files []packEntry }
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			p.t.Fatal(err)
		}
		if out.Files == nil {
			p.t.Errorf("%s: files is not a list: %s", server, rec.Body)
		}
	}
	return rec.Code, out.Files
}

// wantPacked checks the files of packedIndex, named from the catalog.
func wantPacked(t *testing.T, files []packEntry) {
	t.Helper()
	want := []packEntry{
		{Path: "mods/lithium-fabric-0.15.3.jar", Server: true, Project: "gvQqBUqZ", Title: "Lithium", IconURL: "https://cdn.modrinth.com/data/gvQqBUqZ/icon.png"},
		{Path: "mods/sodium-fabric-0.6.13.jar", Server: false, Project: "AANobbMI", Title: "Sodium", IconURL: "https://cdn.modrinth.com/data/AANobbMI/icon.png"},
		{Path: "resourcepacks/faithful.zip", Server: true},
	}
	if len(files) != len(want) {
		t.Fatalf("files %+v, want %+v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("file %d: %+v, want %+v", i, files[i], want[i])
		}
	}
}

// An uploaded pack's files and picture are read from its file on the
// server's volume, once: what is read is kept by the file's SHA-1.
func TestUploadedModpackContents(t *testing.T) {
	p := newPanel(t)
	standIn := &modrinthStandIn{asked: map[string]int{}}
	p.srv.catalog.http.Transport = standIn
	pack := mrpack(t, packedIndex, map[string]string{"overrides/icon.png": pngOf(t, 128), "overrides/config/a.toml": "x"})
	sc, file := packedServer(p, "packed", pack)

	code, files := contentsOf(p, "viewer", "packed")
	if code != 200 {
		t.Fatalf("contents: %d", code)
	}
	wantPacked(t, files)
	rec := p.send("viewer", "GET", "/orgs/alpha/servers/packed/modpack/icon", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("ETag") == "" {
		t.Fatalf("icon: %d %v", rec.Code, rec.Header())
	}
	// Redrawn at the size of a server's picture.
	if cfg, err := png.DecodeConfig(bytes.NewReader(rec.Body.Bytes())); err != nil || cfg.Width != serverIconSide {
		t.Errorf("icon: %+v %v", cfg, err)
	}

	// Read once: the file gone, both are still known.
	if err := sc.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, again := contentsOf(p, "viewer", "packed"); len(again) != 3 {
		t.Errorf("not kept: %+v", again)
	}
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/packed/modpack/icon", nil); rec.Code != 200 {
		t.Errorf("icon not kept: %d", rec.Code)
	}

	// Another server's file of the same name is read for itself: anyone
	// with that server's files can change it.
	other, otherFile := packedServer(p, "other", pack)
	if err := other.Remove(otherFile); err != nil {
		t.Fatal(err)
	}
	changed, err := other.Create(otherFile)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = changed.Write(mrpack(t, strings.Replace(packedIndex, "resourcepacks/faithful.zip", "resourcepacks/other.zip", 1), nil))
	changed.Close()
	if _, files := contentsOf(p, "viewer", "other"); len(files) != 3 || files[2].Path != "resourcepacks/other.zip" {
		t.Errorf("another server's: %+v", files)
	}
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/other/modpack/icon", nil); rec.Code != 404 {
		t.Errorf("another server's icon: %d", rec.Code)
	}

	// Only to those who may see the server.
	if code, _ := contentsOf(p, "outsider", "packed"); code != 404 {
		t.Errorf("an outsider: %d", code)
	}
	if rec := p.send("outsider", "GET", "/orgs/alpha/servers/packed/modpack/icon", nil); rec.Code != 404 {
		t.Errorf("an outsider's icon: %d", rec.Code)
	}
}

// One that cannot be read lists nothing, and has no picture: a pack whose
// file is not a pack, one whose file is not there, or whose server's files
// cannot be reached. Those last two are tried again.
func TestUnreadableModpackContents(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = &modrinthStandIn{asked: map[string]int{}}

	packedServer(p, "broken", []byte("PK not a zip"))
	if code, files := contentsOf(p, "viewer", "broken"); code != 200 || len(files) != 0 {
		t.Errorf("not a pack: %d %+v", code, files)
	}
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/broken/modpack/icon", nil); rec.Code != 404 {
		t.Errorf("not a pack's icon: %d", rec.Code)
	}

	// A pack without a picture.
	packedServer(p, "plain", mrpack(t, packedIndex, nil))
	if rec := p.send("viewer", "GET", "/orgs/alpha/servers/plain/modpack/icon", nil); rec.Code != 404 {
		t.Errorf("no picture: %d", rec.Code)
	}

	pack := mrpack(t, packedIndex, map[string]string{"later": "1"})
	sc, file := packedServer(p, "later", pack)
	if err := sc.Remove(file); err != nil {
		t.Fatal(err)
	}
	if code, files := contentsOf(p, "viewer", "later"); code != 200 || len(files) != 0 {
		t.Errorf("no file: %d %+v", code, files)
	}
	f, err := sc.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(pack)
	f.Close()
	if _, files := contentsOf(p, "viewer", "later"); len(files) != 3 {
		t.Errorf("not tried again once there: %+v", files)
	}

	// Its server's files out of reach.
	packedServer(p, "away", mrpack(t, packedIndex, map[string]string{"x": "y"}))
	p.srv.workspaces.mu.Lock()
	delete(p.srv.workspaces.conns, p.srv.Namespace("alpha")+"/away")
	p.srv.workspaces.mu.Unlock()
	if code, files := contentsOf(p, "viewer", "away"); code != 200 || len(files) != 0 {
		t.Errorf("out of reach: %d %+v", code, files)
	}

	// A modpack not uploaded yet, and a server without one.
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "waiting"},
		Spec: avsysv1.MinecraftServerSpec{EULA: true, State: avsysv1.StateStopped,
			Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "probe.mrpack", GameVersion: "1.21.4", Loader: "fabric"}},
	}
	p.srv.applyDefaults("alpha", "waiting", &mc.Spec)
	if err := p.srv.Client.Create(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	for _, server := range []string{"waiting", "world"} {
		if code, files := contentsOf(p, "viewer", server); code != 200 || len(files) != 0 {
			t.Errorf("%s: %d %+v", server, code, files)
		}
		if rec := p.send("viewer", "GET", "/orgs/alpha/servers/"+server+"/modpack/icon", nil); rec.Code != 404 {
			t.Errorf("%s's icon: %d", server, rec.Code)
		}
	}
}

// modrinthPacked makes a server running a version of a pack from Modrinth.
func modrinthPacked(p *panel, name, version string) {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: name},
		Spec: avsysv1.MinecraftServerSpec{EULA: true, State: avsysv1.StateStopped,
			Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "probe-pack", Version: version, Title: "Probe Pack", GameVersion: "1.21.4", Loader: "fabric"}},
	}
	p.srv.applyDefaults("alpha", name, &mc.Spec)
	if err := p.srv.Client.Create(context.Background(), mc); err != nil {
		p.t.Fatal(err)
	}
}

// A pack from Modrinth is read from the .mrpack of its version, of which
// only its index is fetched, once for each version.
func TestModrinthModpackContents(t *testing.T) {
	// Over a megabyte the index has no need of.
	filler := make([]byte, 2<<20)
	_, _ = rand.Read(filler)
	pack := mrpack(t, packedIndex, map[string]string{"overrides/big.bin": string(filler)})
	for _, whole := range []bool{false, true} {
		p := newPanel(t)
		standIn := &modrinthStandIn{asked: map[string]int{}, pack: pack, whole: whole, versions: map[string]string{
			"VERSION1": `{"id":"VERSION1","files":[{"url":"https://cdn.modrinth.com/data/PACKPROJ/versions/VERSION1/probe.mrpack","filename":"probe.mrpack","primary":true}]}`,
		}}
		p.srv.catalog.http.Transport = standIn
		modrinthPacked(p, "pack", "VERSION1")
		modrinthPacked(p, "copy", "VERSION1")

		code, files := contentsOf(p, "viewer", "pack")
		if code != 200 {
			t.Fatalf("whole %v: %d", whole, code)
		}
		wantPacked(t, files)
		if !whole && standIn.sent > len(pack)/2 {
			t.Errorf("fetched %d bytes of a %d byte pack", standIn.sent, len(pack))
		}
		if whole && standIn.sent != len(pack) {
			t.Errorf("fetched %d bytes of a %d byte pack sent whole", standIn.sent, len(pack))
		}
		// Kept for the version, whichever server runs it.
		fetched := standIn.count("cdn.modrinth.com/data/PACKPROJ/versions/VERSION1/probe.mrpack")
		if _, again := contentsOf(p, "viewer", "copy"); len(again) != 3 {
			t.Errorf("the second: %+v", again)
		}
		if n := standIn.count("cdn.modrinth.com/data/PACKPROJ/versions/VERSION1/probe.mrpack"); n != fetched {
			t.Errorf("fetched again: %d times, then %d", fetched, n)
		}
		// A pack from Modrinth has its picture there.
		if rec := p.send("viewer", "GET", "/orgs/alpha/servers/pack/modpack/icon", nil); rec.Code != 404 {
			t.Errorf("icon: %d", rec.Code)
		}
	}

	// A part of the pack that cannot be fetched lists nothing for now,
	// and is fetched again next time.
	p := newPanel(t)
	standIn := &modrinthStandIn{asked: map[string]int{}, pack: pack, broken: true, versions: map[string]string{
		"VERSION1": `{"id":"VERSION1","files":[{"url":"https://cdn.modrinth.com/data/PACKPROJ/versions/VERSION1/probe.mrpack","primary":true}]}`,
	}}
	p.srv.catalog.http.Transport = standIn
	modrinthPacked(p, "pack", "VERSION1")
	if code, files := contentsOf(p, "viewer", "pack"); code != 200 || len(files) != 0 {
		t.Errorf("broken: %d %+v", code, files)
	}
	standIn.mu.Lock()
	standIn.broken = false
	standIn.mu.Unlock()
	if _, files := contentsOf(p, "viewer", "pack"); len(files) != 3 {
		t.Errorf("not fetched again: %+v", files)
	}

	// A version Modrinth does not have, or whose file is elsewhere, lists
	// nothing.
	p = newPanel(t)
	standIn = &modrinthStandIn{asked: map[string]int{}, pack: pack, versions: map[string]string{
		"ELSEWHER": `{"id":"ELSEWHER","files":[{"url":"https://example.com/probe.mrpack","primary":true}]}`,
	}}
	p.srv.catalog.http.Transport = standIn
	modrinthPacked(p, "gone", "MISSING1")
	modrinthPacked(p, "elsewhere", "ELSEWHER")
	for _, server := range []string{"gone", "elsewhere"} {
		if code, files := contentsOf(p, "viewer", server); code != 200 || len(files) != 0 {
			t.Errorf("%s: %d %+v", server, code, files)
		}
	}
	if n := standIn.count("example.com/probe.mrpack"); n != 0 {
		t.Errorf("fetched from elsewhere %d times", n)
	}
}

// A remote zip is read from the parts asked for, as any zip is.
func TestRemoteZipReadAt(t *testing.T) {
	data := make([]byte, 1<<20)
	_, _ = rand.Read(data)
	standIn := &modrinthStandIn{asked: map[string]int{}, pack: data}
	client := &http.Client{Transport: standIn}
	r, size, done, err := openRemoteZip(context.Background(), client, "test", "https://cdn.modrinth.com/data/x/f.mrpack")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if size != int64(len(data)) {
		t.Fatalf("size %d", size)
	}
	for _, c := range []struct{ off, n int }{{0, 10}, {len(data) - 5, 5}, {len(data) - 5, 10}, {300 << 10, 4096}, {100, 600 << 10}} {
		buf := make([]byte, c.n)
		n, err := r.ReadAt(buf, int64(c.off))
		want := min(c.n, len(data)-c.off)
		if n != want || !bytes.Equal(buf[:n], data[c.off:c.off+want]) {
			t.Errorf("at %d for %d: %d %v", c.off, c.n, n, err)
		}
		if (n < c.n) != (err == io.EOF) {
			t.Errorf("at %d for %d: error %v", c.off, c.n, err)
		}
	}
}
