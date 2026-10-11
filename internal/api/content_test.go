package api

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"

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
