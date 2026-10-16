package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func TestPackLimit(t *testing.T) {
	for version, want := range map[string]int64{"1.21.4": 250, "1.18": 250, "1.17.1": 100, "1.15.2": 100, "1.14.4": 50, "1.8.9": 50, "LATEST": 250, "25w14a": 250, "": 250} {
		if got := packLimit(version) >> 20; got != want {
			t.Errorf("packLimit(%q) = %d MB, want %d", version, got, want)
		}
	}
}

// zipOf writes a zip of these files to a temporary file.
func zipOf(t *testing.T, names ...string) (*os.File, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "pack-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	z := zip.NewWriter(f)
	for _, name := range names {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{"pack":{"pack_format":46,"description":"test"}}`)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	return f, info.Size()
}

// zipWith writes a zip of these files and contents to a temporary file.
func zipWith(t *testing.T, files map[string]string) (*os.File, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "pack-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	z := zip.NewWriter(f)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	return f, info.Size()
}

func TestCheckPack(t *testing.T) {
	// Its metadata has to be something the game can read.
	for what, files := range map[string]map[string]string{
		"a folder of that name": {"pack.mcmeta/": "", "assets/x.json": "{}"},
		"not JSON":              {"pack.mcmeta": "not json"},
		"no pack in it":         {"pack.mcmeta": `{"other":1}`},
		"a pack of null":        {"pack.mcmeta": `{"pack":null}`},
		"a pack of a string":    {"pack.mcmeta": `{"pack":"x"}`},
		"an empty pack":         {"pack.mcmeta": `{"pack":{}}`},
		"no format":             {"pack.mcmeta": `{"pack":{"description":"x"}}`},
	} {
		f, size := zipWith(t, files)
		if err := checkPack(f, size); err == nil || !strings.Contains(err.Error(), "not a pack's metadata") {
			t.Errorf("%s: %v", what, err)
		}
	}
	// From 1.21.9 a pack may say min_format instead.
	newer, size := zipWith(t, map[string]string{"pack.mcmeta": `{"pack":{"description":{"text":"x"},"min_format":[69,0],"max_format":70}}`})
	if err := checkPack(newer, size); err != nil {
		t.Errorf("a pack with min_format: %v", err)
	}
	ok, size := zipOf(t, "pack.mcmeta", "assets/minecraft/lang/en_us.json")
	if err := checkPack(ok, size); err != nil {
		t.Errorf("a pack with pack.mcmeta at its top: %v", err)
	}
	nested, size := zipOf(t, "MyPack/pack.mcmeta", "MyPack/assets/x.json")
	if err := checkPack(nested, size); err == nil || !strings.Contains(err.Error(), "in a folder, MyPack") {
		t.Errorf("a pack a folder down: %v", err)
	}
	none, size := zipOf(t, "assets/x.json")
	if err := checkPack(none, size); err == nil || !strings.Contains(err.Error(), "no pack.mcmeta") {
		t.Errorf("a zip with no pack.mcmeta: %v", err)
	}
	plain, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	plain.WriteString("not a zip at all")
	if err := checkPack(plain, 16); err == nil || !strings.Contains(err.Error(), "not a zip") {
		t.Errorf("not a zip: %v", err)
	}
}

// Packs are served by their hash to anyone, as players' games fetch them,
// with ranges and a long cache life, and nothing else is.
func TestPackHandler(t *testing.T) {
	p := newPanel(t)
	sha := strings.Repeat("ab", 20)
	body := []byte("PK pretend zip contents")
	if err := os.WriteFile(filepath.Join(p.srv.Config.PackDir, sha+".zip"), body, 0o640); err != nil {
		t.Fatal(err)
	}
	h := p.srv.Handler()
	get := func(path string, header ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/packs/" + sha + ".zip")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("a kept pack: %d %q", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control %q", cc)
	}
	if w := get("/packs/"+sha+".zip", "Range", "bytes=0-1"); w.Code != http.StatusPartialContent || w.Body.String() != "PK" {
		t.Errorf("a range: %d %q", w.Code, w.Body.String())
	}
	for _, path := range []string{"/packs/" + strings.Repeat("cd", 20) + ".zip", "/packs/" + sha, "/packs/", "/packs/" + sha + ".zip.bak"} {
		if w := get(path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}
	// Anything climbing out is not served: the router sends it elsewhere.
	if w := get("/packs/../store.db"); w.Code == http.StatusOK {
		t.Errorf("/packs/../store.db served")
	}
}

func TestKeepUploadedPack(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	uploaded := func() *avsysv1.MinecraftServerSpec {
		return &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://p/packs/" + sha + ".zip", SHA1: sha, Uploaded: &avsysv1.UploadedPack{Name: "p.zip", Size: 9}}}
	}
	// Kept as it is, with other settings changed.
	spec := uploaded()
	spec.ResourcePack.Enforce = true
	if err := keepUploadedPack(spec, uploaded()); err != nil || spec.ResourcePack.Uploaded == nil {
		t.Errorf("kept: %v %+v", err, spec.ResourcePack)
	}
	// Pointed elsewhere: no longer the uploaded one.
	spec = uploaded()
	spec.ResourcePack.URL, spec.ResourcePack.SHA1 = "https://elsewhere.example/p.zip", strings.Repeat("cd", 20)
	if err := keepUploadedPack(spec, uploaded()); err != nil || spec.ResourcePack.Uploaded != nil {
		t.Errorf("elsewhere: %v %+v", err, spec.ResourcePack)
	}
	// Settings from before it was uploaded, saying none: it is kept.
	spec = &avsysv1.MinecraftServerSpec{}
	if err := keepUploadedPack(spec, uploaded()); err != nil || spec.ResourcePack == nil || spec.ResourcePack.Uploaded == nil {
		t.Errorf("none said: %v %+v", err, spec.ResourcePack)
	}
	// The same with the version lowered under it: refused, as the pack is
	// too big for it.
	spec = &avsysv1.MinecraftServerSpec{Version: "1.17.1"}
	big := uploaded()
	big.ResourcePack.Uploaded.Size = 200 << 20
	if err := keepUploadedPack(spec, big); err == nil {
		t.Error("a pack too big for the lowered version was kept")
	}
	// Made up, or its hash changed by hand: refused.
	if err := keepUploadedPack(uploaded(), &avsysv1.MinecraftServerSpec{}); err == nil {
		t.Error("an uploaded pack made up was taken")
	}
	spec = uploaded()
	spec.ResourcePack.SHA1 = strings.Repeat("cd", 20)
	if err := keepUploadedPack(spec, uploaded()); err == nil {
		t.Error("an uploaded pack's hash changed by hand was taken")
	}
	// Picked from Modrinth over it, the upload's record left in by the
	// merge: the picked one.
	spec = uploaded()
	spec.ResourcePack.URL, spec.ResourcePack.SHA1 = "https://cdn.modrinth.example/p.zip", strings.Repeat("cd", 20)
	spec.ResourcePack.Project = &avsysv1.PackProject{Source: "modrinth", ID: "faithful"}
	if err := keepUploadedPack(spec, uploaded()); err != nil || spec.ResourcePack.Uploaded != nil || spec.ResourcePack.Project == nil {
		t.Errorf("picked over it: %v %+v", err, spec.ResourcePack)
	}
	// A project said of the uploaded one is not taken.
	spec = uploaded()
	spec.ResourcePack.Project = &avsysv1.PackProject{Source: "modrinth", ID: "faithful"}
	if err := keepUploadedPack(spec, uploaded()); err != nil || spec.ResourcePack.Uploaded == nil || spec.ResourcePack.Project != nil {
		t.Errorf("project on the upload: %v %+v", err, spec.ResourcePack)
	}
}

func TestPackFits(t *testing.T) {
	spec := &avsysv1.MinecraftServerSpec{Version: "1.21.4", ResourcePack: &avsysv1.ResourcePackSpec{Uploaded: &avsysv1.UploadedPack{Size: 200 << 20}}}
	if err := packFits(spec); err != nil {
		t.Errorf("200 MB on 1.21.4: %v", err)
	}
	spec.Version = "1.17.1"
	if err := packFits(spec); err == nil || !strings.Contains(err.Error(), "over 100 MB") {
		t.Errorf("200 MB on 1.17.1: %v", err)
	}
}

// The packs the panel keeps that no server has are removed, once they are
// old enough not to be an upload whose server has not been told yet.
func TestPrunePacks(t *testing.T) {
	p := newPanel(t)
	dir := p.srv.Config.PackDir
	used, unused, fresh, offered := strings.Repeat("aa", 20), strings.Repeat("bb", 20), strings.Repeat("cc", 20), strings.Repeat("dd", 20)
	// A game started before its pack was replaced still offers the old one.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world", Labels: map[string]string{avsysv1.LabelServer: "world"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "minecraft", Env: []corev1.EnvVar{{Name: "RESOURCE_PACK_SHA1", Value: offered}}}}},
	}
	if err := p.srv.Client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{used + ".zip", unused + ".zip", fresh + ".zip", offered + ".zip", "upload-123", "notes.txt"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		if name != fresh+".zip" {
			os.Chtimes(path, old, old)
		}
	}
	server := avsysv1.MinecraftServer{}
	server.Spec.ResourcePack = &avsysv1.ResourcePackSpec{SHA1: used, Uploaded: &avsysv1.UploadedPack{Size: 1}}
	p.srv.prunePacks(context.Background(), []avsysv1.MinecraftServer{server})
	for name, want := range map[string]bool{used + ".zip": true, unused + ".zip": false, fresh + ".zip": true, offered + ".zip": true, "upload-123": false, "notes.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s kept %v, want %v", name, err == nil, want)
		}
	}
}

func TestKeepPackProject(t *testing.T) {
	picked := func() *avsysv1.MinecraftServerSpec {
		return &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://cdn.modrinth.example/v1.zip", SHA1: strings.Repeat("ab", 20), Project: &avsysv1.PackProject{Source: "modrinth", ID: "faithful", Version: "v1"}}}
	}
	// Another address and nothing said of the project, as the merge leaves
	// it: no longer the project's.
	spec := picked()
	spec.ResourcePack.URL = "https://elsewhere.example/p.zip"
	if err := keepUploadedPack(spec, picked()); err != nil || spec.ResourcePack.Project != nil {
		t.Errorf("elsewhere: %v %+v", err, spec.ResourcePack.Project)
	}
	// Another version of it, with its own file: still the project's.
	spec = picked()
	spec.ResourcePack.URL, spec.ResourcePack.Project.Version = "https://cdn.modrinth.example/v2.zip", "v2"
	if err := keepUploadedPack(spec, picked()); err != nil || spec.ResourcePack.Project == nil || spec.ResourcePack.Project.Version != "v2" {
		t.Errorf("another version: %v %+v", err, spec.ResourcePack.Project)
	}
	// Only made required: as it was.
	spec = picked()
	spec.ResourcePack.Enforce = true
	if err := keepUploadedPack(spec, picked()); err != nil || spec.ResourcePack.Project == nil {
		t.Errorf("required: %v %+v", err, spec.ResourcePack.Project)
	}
}

// The uploaded pack a server has is a file it uses: a note that it was
// taken off, from before it was put back, does not remove it.
func TestInstalledFilesHaveThePack(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	spec := &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://p/packs/" + sha + ".zip", SHA1: sha, Uploaded: &avsysv1.UploadedPack{Size: 9}}}
	if !installedFiles(spec)[packFile(sha)] {
		t.Error("its uploaded pack is not among its files")
	}
	spec.ResourcePack.Uploaded = nil
	if installedFiles(spec)[packFile(sha)] {
		t.Error("a pack hosted elsewhere is among its files")
	}
}

func TestPackSHA1IsChecked(t *testing.T) {
	rp := &avsysv1.ResourcePackSpec{URL: "https://example.org/p.zip", SHA1: " " + strings.Repeat("AB", 20) + " "}
	if err := checkPackSHA1(rp, nil); err != nil {
		t.Fatalf("a good SHA-1, in capitals: %v", err)
	}
	if rp.SHA1 != strings.Repeat("ab", 20) {
		t.Errorf("kept as %q, want it in lower case", rp.SHA1)
	}
	rp.SHA1 = "not-a-hash"
	if err := checkPackSHA1(rp, nil); err == nil || !strings.Contains(err.Error(), "40 hexadecimal") {
		t.Errorf("a bad SHA-1: %v", err)
	}
	// One it had before this was checked does not hold up other changes.
	if err := checkPackSHA1(rp, &avsysv1.ResourcePackSpec{SHA1: "not-a-hash"}); err != nil {
		t.Errorf("a bad SHA-1 it had already: %v", err)
	}
	if err := checkPackSHA1(&avsysv1.ResourcePackSpec{URL: "https://example.org/p.zip"}, nil); err != nil {
		t.Errorf("none: %v", err)
	}
}

// memFiles is an SFTP client over files kept in memory.
func memFiles(t *testing.T) *sftp.Client {
	t.Helper()
	c, s := net.Pipe()
	server := sftp.NewRequestServer(s, sftp.InMemHandler())
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(c, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client
}

// Only the panel's own packs, named by their hash, are removed: a file
// someone put beside them is theirs.
func TestRemoveOtherPacks(t *testing.T) {
	sc := memFiles(t)
	keep, old := strings.Repeat("ab", 20), strings.Repeat("cd", 20)
	if err := sc.MkdirAll(packDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path.Base(packFile(keep)), path.Base(packFile(old)), "resource-pack-backup.zip", "notes.txt"} {
		f, err := sc.Create(packDir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := removeOtherPacks(sc, keep); err != nil {
		t.Fatal(err)
	}
	entries, err := sc.ReadDir(packDir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	slices.Sort(left)
	want := []string{"notes.txt", "resource-pack-backup.zip", path.Base(packFile(keep))}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Errorf("left %v, want %v", left, want)
	}
	// None there at all is nothing to do.
	if err := removeOtherPacks(memFiles(t), ""); err != nil {
		t.Errorf("no folder: %v", err)
	}
}
