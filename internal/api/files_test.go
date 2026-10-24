package api

import (
	"strings"
	"testing"
)

// A file is known by its contents: the same again is the same tag, and one
// not there, a folder, or too big to edit has none.
func TestTagNow(t *testing.T) {
	sc := memFiles(t)
	write := func(name, data string) {
		t.Helper()
		f, err := sc.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	tag := func(name string) string {
		t.Helper()
		got, err := tagNow(sc, name)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	write("/a.txt", "motd=hello")
	first := tag("/a.txt")
	if first != fileTag([]byte("motd=hello")) {
		t.Errorf("tag is %s, want that of what it holds", first)
	}
	write("/a.txt", "motd=hello")
	if tag("/a.txt") != first {
		t.Error("the same again has another tag")
	}
	write("/a.txt", "motd=bye")
	if tag("/a.txt") == first {
		t.Error("a change has the same tag")
	}
	if got := tag("/gone.txt"); got != "" {
		t.Errorf("one not there: %q", got)
	}
	if err := sc.Mkdir("/dir"); err != nil {
		t.Fatal(err)
	}
	if got := tag("/dir"); got != "" {
		t.Errorf("a folder: %q", got)
	}
	write("/big.txt", strings.Repeat("x", maxEditable+1))
	if got := tag("/big.txt"); got != "" {
		t.Errorf("one too big: %q", got)
	}
}
