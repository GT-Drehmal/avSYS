package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// mrpack is a Modrinth pack file with this index and these other files.
func mrpack(t *testing.T, index string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	all := map[string]string{"modrinth.index.json": index}
	for name, body := range files {
		all[name] = body
	}
	for name, body := range all {
		if index == "" && name == "modrinth.index.json" {
			continue
		}
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

// fabricIndex is an index of a pack for Fabric on 1.21.4 with one mod for
// both sides and one for players only.
const fabricIndex = `{"formatVersion":1,"game":"minecraft","versionId":"2.0.1","name":"Probe Pack","files":[
{"path":"mods/lithium.jar","env":{"client":"required","server":"required"},"downloads":["https://cdn.modrinth.com/data/a/lithium.jar"]},
{"path":"mods/sodium.jar","env":{"client":"required","server":"unsupported"},"downloads":["https://cdn.modrinth.com/data/b/sodium.jar"]}
],"dependencies":{"minecraft":"1.21.4","fabric-loader":"0.16.10"}}`

func TestReadMrpack(t *testing.T) {
	read := func(data []byte) (*avsysv1.ModpackSpec, error) {
		return readMrpack(bytes.NewReader(data), int64(len(data)))
	}
	pack, err := read(mrpack(t, fabricIndex, map[string]string{"overrides/config/a.toml": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if pack.Source != avsysv1.SourceUpload || pack.Title != "Probe Pack" || pack.VersionName != "2.0.1" || pack.GameVersion != "1.21.4" || pack.Loader != "fabric" {
		t.Errorf("read %+v", pack)
	}
	for dep, loader := range map[string]string{"quilt-loader": "quilt", "forge": "forge", "neoforge": "neoforge"} {
		index := strings.Replace(fabricIndex, `"fabric-loader"`, `"`+dep+`"`, 1)
		if pack, err := read(mrpack(t, index, nil)); err != nil || pack.Loader != loader {
			t.Errorf("%s: %+v %v", dep, pack, err)
		}
	}
	for name, c := range map[string]struct {
		data []byte
		says string
	}{
		"not a zip":         {[]byte("PK not really"), "not a Modrinth modpack"},
		"no index":          {mrpack(t, "", map[string]string{"overrides/a": "x"}), "not a Modrinth modpack"},
		"an index unread":   {mrpack(t, "{", nil), "cannot be read"},
		"another game":      {mrpack(t, strings.Replace(fabricIndex, `"game":"minecraft"`, `"game":"other"`, 1), nil), "not for Minecraft"},
		"no game version":   {mrpack(t, strings.Replace(fabricIndex, `"minecraft":"1.21.4",`, "", 1), nil), "which Minecraft version"},
		"no loader":         {mrpack(t, strings.Replace(fabricIndex, `,"fabric-loader":"0.16.10"`, "", 1), nil), "Fabric, Quilt, Forge or NeoForge"},
		"a file outside":    {mrpack(t, strings.Replace(fabricIndex, "mods/lithium.jar", "../../etc/lithium.jar", 1), nil), "outside the server's folder"},
		"a file at the top": {mrpack(t, strings.Replace(fabricIndex, "mods/lithium.jar", "/etc/lithium.jar", 1), nil), "outside the server's folder"},
		"no download":       {mrpack(t, strings.Replace(fabricIndex, `"https://cdn.modrinth.com/data/a/lithium.jar"`, "", 1), nil), "lithium.jar without a download"},
	} {
		_, err := read(c.data)
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.says)
		}
	}
}

func TestModpackFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Probe Pack 2.0.mrpack":              "Probe Pack 2.0.mrpack",
		"C:\\Users\\me\\x.MRPACK":            "x.MRPACK",
		"../../etc/x.mrpack":                 "x.mrpack",
		"we\"ird;name$.mrpack":               "we_ird_name_.mrpack",
		"pack.zip":                           "",
		".mrpack":                            "",
		"..mrpack":                           "",
		strings.Repeat("a", 300) + ".mrpack": "",
	} {
		got, err := modpackFilename(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q was taken as %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
}

func TestCheckUploadedModpack(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	for name, c := range map[string]struct {
		pack *avsysv1.ModpackSpec
		ok   bool
	}{
		"none":            {nil, true},
		"from Modrinth":   {&avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "../x"}, true},
		"not uploaded":    {&avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack"}, true},
		"uploaded":        {&avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack", Version: sha}, true},
		"a path":          {&avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "../p.mrpack"}, false},
		"another kind":    {&avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.zip"}, false},
		"not a SHA-1":     {&avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack", Version: "../../x"}, false},
		"upper case SHA1": {&avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack", Version: strings.ToUpper(sha)}, false},
	} {
		if err := checkUploadedModpack(c.pack); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An uploaded modpack is changed only by uploading another: settings can
// keep it, but not name another file, nor change what its file said.
func TestKeepUploadedModpack(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	uploaded := func() *avsysv1.MinecraftServerSpec {
		return &avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack", Version: sha, Title: "P", GameVersion: "1.21.4", Loader: "fabric"}}
	}
	spec := uploaded()
	spec.Modpack.Title, spec.Modpack.GameVersion = "Renamed", "1.20.1"
	if err := keepUploadedModpack(spec, uploaded()); err != nil || spec.Modpack.Title != "P" || spec.Modpack.GameVersion != "1.21.4" {
		t.Errorf("kept: %v %+v", err, spec.Modpack)
	}
	spec = uploaded()
	spec.Modpack.Version = strings.Repeat("cd", 20)
	if err := keepUploadedModpack(spec, uploaded()); err == nil {
		t.Error("another file was named")
	}
	if err := keepUploadedModpack(uploaded(), &avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "p"}}); err == nil {
		t.Error("a Modrinth pack was made an uploaded one")
	}
	// Another kind of modpack, or none, is for the settings to say.
	for _, now := range []*avsysv1.ModpackSpec{nil, {Source: avsysv1.SourceModrinth, Project: "other"}} {
		if err := keepUploadedModpack(&avsysv1.MinecraftServerSpec{Modpack: now}, uploaded()); err != nil {
			t.Errorf("%+v: %v", now, err)
		}
	}
}

// Its file is the server's while it is the server's modpack, and is
// removed once another is, or none.
func TestLeftBehindModpack(t *testing.T) {
	a, b := strings.Repeat("aa", 20), strings.Repeat("bb", 20)
	spec := func(sha string) *avsysv1.MinecraftServerSpec {
		return &avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack", Version: sha}}
	}
	file := func(sha string) string { return path.Join(controller.ContentDir, sha+".mrpack") }
	if !installedFiles(spec(a))[file(a)] {
		t.Error("its file is not among those it has")
	}
	if got := leftBehind(spec(a), spec(a)); len(got) != 0 {
		t.Errorf("kept: %v", got)
	}
	if got := leftBehind(spec(b), spec(a)); len(got) != 1 || got[0] != file(a) {
		t.Errorf("replaced: %v", got)
	}
	if got := leftBehind(&avsysv1.MinecraftServerSpec{}, spec(a)); len(got) != 1 || got[0] != file(a) {
		t.Errorf("taken off: %v", got)
	}
	// One not uploaded yet has no file.
	if got := leftBehind(&avsysv1.MinecraftServerSpec{}, spec("")); len(got) != 0 {
		t.Errorf("not uploaded: %v", got)
	}
}

// withFiles has the server's files be sc, kept in memory.
func (p *panel) withFiles(org, server string) *sftp.Client {
	p.t.Helper()
	sc := memFiles(p.t)
	used := &atomic.Int64{}
	used.Store(time.Now().UnixNano())
	p.srv.workspaces.mu.Lock()
	p.srv.workspaces.conns[p.srv.Namespace(org)+"/"+server] = &pooledSFTP{sftp: sc, lastUsed: used}
	p.srv.workspaces.mu.Unlock()
	return sc
}

// A new server with an uploaded modpack is made stopped and given the
// file, which it is then started with. The file is kept by its SHA-1 on the
// server's volume, and what it is built for is read from it; another
// uploaded in its place takes the place of the first.
func TestPutModpack(t *testing.T) {
	p := newPanel(t)
	server := func() *avsysv1.MinecraftServer {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "packed"}, mc); err != nil {
			t.Fatal(err)
		}
		return mc
	}
	made := `{"name":"packed","spec":{"eula":true,%s"modpack":{"source":"upload","project":"Probe Pack.mrpack","version":"` + strings.Repeat("ab", 20) + `","title":"Probe Pack","gameVersion":"1.21.4","loader":"fabric"}}}`
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", strings.Replace(made, "%s", "", 1)); code != 400 {
		t.Fatalf("made running: %d %s", code, msg)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", strings.Replace(made, "%s", `"state":"Stopped",`, 1)); code != 201 {
		t.Fatalf("made: %d %s", code, msg)
	}
	// What file it is comes with the file.
	if v := server().Spec.Modpack.Version; v != "" {
		t.Errorf("made with version %q before its file", v)
	}
	p.setPhase("alpha", "packed", avsysv1.PhaseStopped)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers/packed/start", ""); code != 409 || msg != "no_modpack" {
		t.Errorf("started without its file: %d %s", code, msg)
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/packed", `{"spec":{"state":"Running"}}`); code != 409 || msg != "no_modpack" {
		t.Errorf("set running without its file: %d %s", code, msg)
	}
	sc := p.withFiles("alpha", "packed")

	// One that is not a pack is turned down, and nothing of it is kept.
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/packed/modpack?name=bad.mrpack", []byte("PK not a zip"), "X-Avsys", "1"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "not a Modrinth modpack") {
		t.Errorf("a broken pack: %d %s", rec.Code, rec.Body)
	}
	if entries, _ := sc.ReadDir(controller.ContentDir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/packed/modpack?name=pack.zip", mrpack(t, fabricIndex, nil), "X-Avsys", "1"); rec.Code != 400 {
		t.Errorf("not named .mrpack: %d %s", rec.Code, rec.Body)
	}

	first := mrpack(t, fabricIndex, map[string]string{"overrides/config/a.toml": "x"})
	rec := p.send("owner", "PUT", "/orgs/alpha/servers/packed/modpack?name=Probe%20Pack.mrpack", first, "X-Avsys", "1")
	if rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var view struct {
		Spec avsysv1.MinecraftServerSpec `json:"spec"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(first)
	sha := hex.EncodeToString(sum[:])
	want := avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "Probe Pack.mrpack", Version: sha, Title: "Probe Pack", VersionName: "2.0.1", GameVersion: "1.21.4", Loader: "fabric"}
	if got := server().Spec.Modpack; got == nil || *got != want || *view.Spec.Modpack != want {
		t.Errorf("modpack %+v, want %+v", got, want)
	}
	kept, err := sc.Open(path.Join(controller.ContentDir, sha+".mrpack"))
	if err != nil {
		t.Fatalf("not kept by its SHA-1: %v", err)
	}
	var got bytes.Buffer
	_, _ = got.ReadFrom(kept)
	kept.Close()
	if !bytes.Equal(got.Bytes(), first) {
		t.Error("the file kept is not the one uploaded")
	}
	// Settings cannot point it at another file.
	if code, _ := p.do("owner", "PATCH", "/orgs/alpha/servers/packed", `{"spec":{"modpack":{"version":"`+strings.Repeat("cd", 20)+`"}}}`); code != 400 {
		t.Errorf("another file named in settings: %d", code)
	}
	// Now it can start.
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers/packed/start", ""); code != 200 {
		t.Errorf("start: %d %s", code, msg)
	}

	// A newer one, for another game version, takes its place.
	second := mrpack(t, strings.NewReplacer(`"1.21.4"`, `"1.21.5"`, `"2.0.1"`, `"2.1.0"`).Replace(fabricIndex), nil)
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/packed/modpack?name=Probe%20Pack%202.1.mrpack", second, "X-Avsys", "1"); rec.Code != 200 {
		t.Fatalf("second upload: %d %s", rec.Code, rec.Body)
	}
	sum = sha1.Sum(second)
	now := server().Spec.Modpack
	if now.Version != hex.EncodeToString(sum[:]) || now.GameVersion != "1.21.5" || now.VersionName != "2.1.0" || now.Project != "Probe Pack 2.1.mrpack" {
		t.Errorf("after the second: %+v", now)
	}
	entries, _ := sc.ReadDir(controller.ContentDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != now.Version+".mrpack" {
		t.Errorf("files kept: %v", names)
	}

	// A server that does not run a modpack is not given one this way.
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/world/modpack?name=p.mrpack", first, "X-Avsys", "1"); rec.Code != 400 {
		t.Errorf("a server without a modpack: %d %s", rec.Code, rec.Body)
	}
}

// An uploaded pack built for an older game than the server's world is put
// in place only when the request says that is meant, as another version of
// a pack from Modrinth is; refused, the settings and the files stay as they
// were.
func TestNewerWorldOnAnUploadedModpack(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	made := `{"name":"packed","spec":{"eula":true,"state":"Stopped","modpack":{"source":"upload","project":"Probe Pack.mrpack","title":"Probe Pack","gameVersion":"1.21.4","loader":"fabric"}}}`
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", made); code != 201 {
		t.Fatalf("made: %d %s", code, msg)
	}
	p.setPhase("alpha", "packed", avsysv1.PhaseStopped)
	sc := p.withFiles("alpha", "packed")
	const at = "/orgs/alpha/servers/packed/modpack?name=Probe%20Pack.mrpack"
	if rec := p.send("owner", "PUT", at, mrpack(t, fabricIndex, nil), "X-Avsys", "1"); rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	modpack := func() avsysv1.ModpackSpec {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "packed"}, mc); err != nil {
			t.Fatal(err)
		}
		return *mc.Spec.Modpack
	}
	was := modpack()

	older := mrpack(t, strings.NewReplacer(`"1.21.4"`, `"1.21"`, `"2.0.1"`, `"1.0.0"`).Replace(fabricIndex), nil)
	rec := p.send("owner", "PUT", at, older, "X-Avsys", "1")
	var e struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != 409 || e.Code != "newer_world" {
		t.Errorf("a pack for 1.21: %d %s, want 409 newer_world", rec.Code, rec.Body)
	}
	if now := modpack(); now != was {
		t.Errorf("refused, it runs %+v", now)
	}
	if entries, _ := sc.ReadDir(controller.ContentDir); len(entries) != 1 || entries[0].Name() != was.Version+".mrpack" {
		t.Errorf("files after the refusal: %v", entries)
	}
	if rec := p.send("owner", "PUT", at+"&newerWorld=true", older, "X-Avsys", "1"); rec.Code != 200 {
		t.Errorf("a pack for 1.21, as meant: %d %s", rec.Code, rec.Body)
	}
	if now := modpack(); now.GameVersion != "1.21" {
		t.Errorf("as meant, it runs %+v", now)
	}
	// A newer one is never asked about.
	newer := mrpack(t, strings.NewReplacer(`"1.21.4"`, `"1.21.5"`, `"2.0.1"`, `"2.1.0"`).Replace(fabricIndex), nil)
	if rec := p.send("owner", "PUT", at, newer, "X-Avsys", "1"); rec.Code != 200 {
		t.Errorf("a pack for 1.21.5: %d %s", rec.Code, rec.Body)
	}
}

// A template of settings alone has nowhere to keep a modpack's file.
func TestTemplateRefusesUploadedModpack(t *testing.T) {
	p := newPanel(t)
	body := `{"name":"Packed","spec":{"modpack":{"source":"upload","project":"p.mrpack","gameVersion":"1.21.4","loader":"fabric"},"resources":{"memory":"2Gi"},"storage":{"size":"2Gi"}}}`
	if code, _ := p.do("owner", "POST", "/orgs/alpha/templates", body); code != 400 {
		t.Errorf("made: %d", code)
	}
}

// packedServer makes a stopped server whose uploaded modpack is pack, its
// file kept in its files, which are in memory.
func packedServer(p *panel, name string, pack []byte) (*sftp.Client, string) {
	p.t.Helper()
	sum := sha1.Sum(pack)
	sha := hex.EncodeToString(sum[:])
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: name},
		Spec: avsysv1.MinecraftServerSpec{
			EULA: true, State: avsysv1.StateStopped,
			Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "probe.mrpack", Version: sha, Title: "Probe Pack", GameVersion: "1.21.4", Loader: "fabric"},
		},
	}
	p.srv.applyDefaults("alpha", name, &mc.Spec)
	if err := p.srv.Client.Create(context.Background(), mc); err != nil {
		p.t.Fatal(err)
	}
	p.setPhase("alpha", name, avsysv1.PhaseStopped)
	sc := p.withFiles("alpha", name)
	if err := sc.MkdirAll(controller.ContentDir); err != nil {
		p.t.Fatal(err)
	}
	file := path.Join(controller.ContentDir, sha+".mrpack")
	f, err := sc.Create(file)
	if err != nil {
		p.t.Fatal(err)
	}
	_, _ = f.Write(pack)
	f.Close()
	return sc, file
}

// While a snapshot of the server is taken, its modpack's file is not taken
// away by a change of settings: the snapshot would record the pack without
// its file. Other changes still go through.
func TestModpackFileStaysDuringASnapshot(t *testing.T) {
	p := newPanel(t)
	sc, file := packedServer(p, "packed", mrpack(t, fabricIndex, nil))
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "packed", kindSnapshot, ""); !ok {
		t.Fatal("could not start a snapshot")
	}
	off := `{"spec":{"modpack":null,"loader":"fabric","version":"1.21.4"}}`
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/packed", off); code != 409 || msg != "busy" {
		t.Errorf("taken off during a snapshot: %d %s", code, msg)
	}
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/packed", `{"spec":{"properties":{"motd":"hi"}}}`); code != 200 {
		t.Errorf("another change during a snapshot: %d %s", code, msg)
	}
	if _, err := sc.Stat(file); err != nil {
		t.Fatalf("the file went during the snapshot: %v", err)
	}
	p.srv.ops.end("mc-alpha", "packed")
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/packed", off); code != 200 {
		t.Fatalf("taken off after: %d %s", code, msg)
	}
	if _, err := sc.Stat(file); err == nil {
		t.Error("the file of a modpack taken off is still there")
	}
}

// A pack whose file has a name the panel changes, as one with letters
// outside ASCII, makes a server under the name its upload will have.
func TestNewServerNamesItsModpackAsItsUploadWill(t *testing.T) {
	p := newPanel(t)
	body := `{"name":"cafe","spec":{"eula":true,"state":"Stopped","modpack":{"source":"upload","project":"Café Pack.mrpack","gameVersion":"1.21.4","loader":"fabric"}}}`
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != 201 {
		t.Fatalf("made: %d %s", code, msg)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "cafe"}, mc); err != nil {
		t.Fatal(err)
	}
	if want, _ := modpackFilename("Café Pack.mrpack"); mc.Spec.Modpack.Project != want || want != "Caf_ Pack.mrpack" {
		t.Errorf("project %q, want %q", mc.Spec.Modpack.Project, want)
	}
	if code, _ := p.do("owner", "POST", "/orgs/alpha/servers", strings.NewReplacer(`"cafe"`, `"other"`, "Café Pack.mrpack", "pack.zip").Replace(body)); code != 400 {
		t.Errorf("not a .mrpack: %d", code)
	}
}

// A server made from another whose modpack file was never uploaded stays
// stopped, whatever it was asked to be: it has nothing to start with.
func TestFinalState(t *testing.T) {
	missing := &avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack"}}
	there := &avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceUpload, Project: "p.mrpack", Version: strings.Repeat("ab", 20)}}
	modrinth := &avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "p"}}
	for name, c := range map[string]struct {
		spec       *avsysv1.MinecraftServerSpec
		asked, got avsysv1.DesiredState
	}{
		"not uploaded":          {missing, avsysv1.StateRunning, avsysv1.StateStopped},
		"not uploaded, stopped": {missing, avsysv1.StateStopped, avsysv1.StateStopped},
		"uploaded":              {there, avsysv1.StateRunning, avsysv1.StateRunning},
		"from Modrinth":         {modrinth, avsysv1.StateRunning, avsysv1.StateRunning},
		"none":                  {&avsysv1.MinecraftServerSpec{}, avsysv1.StateRunning, avsysv1.StateRunning},
	} {
		if got := finalState(c.spec, c.asked); got != c.got {
			t.Errorf("%s: %s, want %s", name, got, c.got)
		}
	}
}

// The same pack uploaded again, when the server cannot then be read again,
// leaves the file its settings say where it is.
func TestModpackFileStaysWhenTheServerCannotBeRead(t *testing.T) {
	p := newPanel(t)
	pack := mrpack(t, fabricIndex, nil)
	sc, file := packedServer(p, "packed", pack)
	var gets atomic.Int32
	p.srv.Client = interceptor.NewClient(p.srv.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			// The request's first read of it goes; the read again, once
			// the file is in place, does not.
			if _, ok := obj.(*avsysv1.MinecraftServer); ok && gets.Add(1) > 1 {
				return errors.New("the cluster does not answer")
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if rec := p.send("owner", "PUT", "/orgs/alpha/servers/packed/modpack?name=probe.mrpack", pack, "X-Avsys", "1"); rec.Code < 500 {
		t.Fatalf("the upload went through: %d %s", rec.Code, rec.Body)
	}
	if gets.Load() < 2 {
		t.Fatal("the server was not read again")
	}
	if _, err := sc.Stat(file); err != nil {
		t.Errorf("the file its settings say is gone: %v", err)
	}
}
