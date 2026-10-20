package api

import "testing"

func TestArchiveRoot(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		"MyServer":     "MyServer",
		"MyServer/":    "MyServer",
		"a/b c/server": "a/b c/server",
		"/MyServer":    "MyServer",
	} {
		if got, err := archiveRoot(in); err != nil || got != want {
			t.Errorf("archiveRoot(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"..", "../x", "a/../../b", "a/./b", "a//b", "a\nb"} {
		if got, err := archiveRoot(in); err == nil {
			t.Errorf("archiveRoot(%q) = %q, want it refused", in, got)
		}
	}
}
