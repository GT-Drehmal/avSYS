package api

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestCheckPack(t *testing.T) {
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
	// Made up, or its hash changed by hand: refused.
	if err := keepUploadedPack(uploaded(), &avsysv1.MinecraftServerSpec{}); err == nil {
		t.Error("an uploaded pack made up was taken")
	}
	spec = uploaded()
	spec.ResourcePack.SHA1 = strings.Repeat("cd", 20)
	if err := keepUploadedPack(spec, uploaded()); err == nil {
		t.Error("an uploaded pack's hash changed by hand was taken")
	}
}
