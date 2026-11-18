package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// worldZip is a zip of the files given, by their paths in it, as a world
// uploaded to the library is.
func worldZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// islandZip is a world uploaded as a single-player save, Island in saves.
func islandZip(t *testing.T) []byte {
	return worldZip(t, map[string]string{"saves/Island/level.dat": "the island", "saves/Island/region/r.0.0.mca": "sand"})
}

// filesUnder are the files under dir, by their paths in it, with what each
// has.
func filesUnder(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			out[filepath.ToSlash(strings.TrimPrefix(p, dir+"/"))] = "LINK"
		}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(strings.TrimPrefix(p, dir+"/"))] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorldEntry(t *testing.T) {
	for _, c := range []struct {
		entry, root, want string
		ok                bool
	}{
		{"saves/Island/level.dat", "saves/Island", "data/world/level.dat", true},
		{"./saves/Island/region/r.0.0.mca", "saves/Island", "data/world/region/r.0.0.mca", true},
		{"saves/Island/", "saves/Island", "data/world", true},
		{"saves/Island_nether/DIM-1/r", "saves/Island", "data/world_nether/DIM-1/r", true},
		{"saves/Island_the_end/DIM1/r", "saves/Island", "data/world_the_end/DIM1/r", true},
		{"Island/level.dat", "Island", "data/world/level.dat", true},
		{"Island_nether/DIM-1/r", "Island", "data/world_nether/DIM-1/r", true},
		{"level.dat", "", "data/world/level.dat", true},
		{"region/r.0.0.mca", "", "data/world/region/r.0.0.mca", true},
		// Not of the world, or outside where it goes.
		{"saves/Other/level.dat", "saves/Island", "", false},
		{"saves/Islander/level.dat", "saves/Island", "", false},
		{"saves/Island/../../etc/passwd", "saves/Island", "", false},
		{"../level.dat", "", "", false},
		{"/etc/passwd", "", "", false},
		{"/saves/Island/level.dat", "saves/Island", "", false},
		{"", "", "", false},
		{"./", "", "", false},
	} {
		got, ok := worldEntry(c.entry, c.root)
		if got != c.want || ok != c.ok {
			t.Errorf("worldEntry(%q, %q) = %q, %v; want %q, %v", c.entry, c.root, got, ok, c.want, c.ok)
		}
	}
}

// What is unpacked of an uploaded world: its folder, as data/world, and its
// Nether and End beside it, as an import into a server takes them; never a
// link, a lock, or anything outside; and only as much as there is room for.
func TestUnpackWorld(t *testing.T) {
	unpack := func(t *testing.T, archive []byte, root string, room int64) (*unpackedWorld, map[string]string, error) {
		t.Helper()
		dir := t.TempDir()
		file := filepath.Join(t.TempDir(), "archive")
		if err := os.WriteFile(file, archive, 0o600); err != nil {
			t.Fatal(err)
		}
		u, err := unpackWorld(file, root, dir, room, room)
		return u, filesUnder(t, dir), err
	}
	want := map[string]string{
		"data/world/level.dat":              "the island",
		"data/world/region/r.0.0.mca":       "sand",
		"data/world_nether/DIM-1/r.0.0.mca": "fire",
	}

	// A zip, with what is not of the world in it too.
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"saves/Island/level.dat":              "the island",
		"saves/Island/region/r.0.0.mca":       "sand",
		"saves/Island/session.lock":           "locked",
		"saves/Island_nether/DIM-1/r.0.0.mca": "fire",
		"saves/Other/level.dat":               "another",
		"saves/Island/../../../escaped":       "out",
		"options.txt":                         "fov:1",
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(content))
	}
	link := &zip.FileHeader{Name: "saves/Island/link"}
	link.SetMode(fs.ModeSymlink | 0o777)
	w, err := z.CreateHeader(link)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("/etc/passwd"))
	if _, err := z.Create("saves/Island/data/"); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	u, got, err := unpack(t, buf.Bytes(), "saves/Island", 1<<20)
	if err != nil {
		t.Fatalf("a zip: %v", err)
	}
	if !maps.Equal(got, want) {
		t.Errorf("a zip unpacked to %v", got)
	}
	if u.Files != 3 || u.Bytes != int64(len("the island")+len("sand")+len("fire")) || strings.Join(u.Folders, " ") != "data/world data/world_nether" {
		t.Errorf("a zip: %+v", u)
	}

	// A tar.gz, a link and a device in it, its world at its top.
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for _, h := range []*tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./level.dat", Typeflag: tar.TypeReg, Size: 10, Mode: 0o600},
		{Name: "./region/", Typeflag: tar.TypeDir, Mode: 0o700},
		{Name: "./region/r.0.0.mca", Typeflag: tar.TypeReg, Size: 4, Mode: 0o644},
		{Name: "./DIM-1/r.0.0.mca", Typeflag: tar.TypeReg, Size: 4, Mode: 0o644},
		{Name: "./link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "./hard", Typeflag: tar.TypeLink, Linkname: "level.dat"},
		{Name: "./dev", Typeflag: tar.TypeChar},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		switch h.Name {
		case "./level.dat":
			_, _ = tw.Write([]byte("the island"))
		case "./region/r.0.0.mca":
			_, _ = tw.Write([]byte("sand"))
		case "./DIM-1/r.0.0.mca":
			_, _ = tw.Write([]byte("fire"))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	u, got, err = unpack(t, tgz.Bytes(), "", 1<<20)
	if err != nil {
		t.Fatalf("a tar.gz: %v", err)
	}
	if want := map[string]string{"data/world/level.dat": "the island", "data/world/region/r.0.0.mca": "sand", "data/world/DIM-1/r.0.0.mca": "fire"}; !maps.Equal(got, want) {
		t.Errorf("a tar.gz unpacked to %v", got)
	}
	if strings.Join(u.Folders, " ") != "data/world" || u.Files != 3 {
		t.Errorf("a tar.gz: %+v", u)
	}
	// What is not a world, or not there, or too large for the room, is
	// turned down, and said why.
	for _, c := range []struct {
		name    string
		archive []byte
		root    string
		room    int64
		says    string
	}{
		{"no folder", islandZip(t), "saves/Elsewhere", 1 << 20, "no folder saves/Elsewhere"},
		{"no level.dat", worldZip(t, map[string]string{"saves/Island/region/r.0.0.mca": "sand"}), "saves/Island", 1 << 20, "no level.dat"},
		{"not an archive", []byte("a world, they said"), "", 1 << 20, "Use a .zip or .tar.gz file"},
		{"a zip cut short", islandZip(t)[:40], "saves/Island", 1 << 20, "damaged"},
		{"no room", islandZip(t), "saves/Island", 8, "larger than"},
		{"empty", worldZip(t, map[string]string{}), "", 1 << 20, "empty"},
	} {
		_, _, err := unpack(t, c.archive, c.root, c.room)
		var said *Error
		if !errors.As(err, &said) || !strings.Contains(said.Message, c.says) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A tar that says less than it has, for room, is held to the room.
	big := make([]byte, 4096)
	_, _ = rand.Read(big)
	var plain bytes.Buffer
	tw = tar.NewWriter(&plain)
	for _, name := range []string{"w/level.dat", "w/region/r.0.0.mca"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(big)), Mode: 0o644, ModTime: time.Now()})
		_, _ = tw.Write(big)
	}
	_ = tw.Close()
	if _, _, err := unpack(t, plain.Bytes(), "w", 6000); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a tar larger than the room: %v", err)
	}
	if u, _, err := unpack(t, plain.Bytes(), "w", 1<<20); err != nil || u.Files != 2 {
		t.Errorf("a plain tar: %+v, %v", u, err)
	}
}
