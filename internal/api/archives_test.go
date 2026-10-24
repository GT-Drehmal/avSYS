package api

import "testing"

func TestArchiveTop(t *testing.T) {
	for _, tc := range []struct{ listing, want string }{
		{"world/\nworld/level.dat\nworld/region/r.0.0.mca\n", "world"},
		{"world/level.dat\nworld/region/\n", "world"},
		{"./world/\n./world/level.dat\n", "world"},
		{"world/\n", "world"},
		// A name's spaces are its own.
		{" world/\n world/level.dat\n", " world"},
		{"world /level.dat\r\n", "world "},
		// What a Mac adds does not count.
		{"world/\nworld/level.dat\n__MACOSX/\n__MACOSX/world/._level.dat\n", "world"},
		// Not all in one folder.
		{"level.dat\nregion/r.0.0.mca\n", ""},
		{"world/level.dat\nworld_nether/level.dat\n", ""},
		{"world/level.dat\nREADME.txt\n", ""},
		{"config.yml\n", ""},
		{"../evil/x\n", ""},
		{"", ""},
	} {
		if got := archiveTop(tc.listing); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.listing, got, tc.want)
		}
	}
}

func TestArchiveName(t *testing.T) {
	for name, stem := range map[string]string{"world.zip": "world", "Pack.TAR.GZ": "Pack", "a.b.tgz": "a.b", "x.7z": "x", "plugin.jar": "", "notes.txt": "", ".zip": ""} {
		ok := archiveName.MatchString(name) && archiveStem(name) != name
		if (stem != "") != ok || (ok && archiveStem(name) != stem) {
			t.Errorf("%s: archive %v, stem %q; want %q", name, ok, archiveStem(name), stem)
		}
	}
}
