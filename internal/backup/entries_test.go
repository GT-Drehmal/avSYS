package backup

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What is right in a folder of a snapshot is listed, its folders' insides
// not; and files of it are taken out where they are in it, those asked for
// alone, names restic would take for patterns as they are.
func TestEntriesAndExtract(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := &Manager{Dir: t.TempDir(), Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"data/world/level.dat":                      "a world",
		"data/world/datapacks/a.zip":                "a zip",
		"data/world/datapacks/we[ird]*.zip":         "a weird one",
		"data/world/datapacks/b/pack.mcmeta":        "b's",
		"data/world/datapacks/b/data/x/f.json":      "inside b",
		"data/world/datapacks/c/data/x/f.json":      "inside c",
		"data/world/datapacks/weirder[/pack.mcmeta": "weirder's",
	})
	kept, err := m.KeepFolders(ctx, "alpha", dir, []string{"data/world"}, "world-w1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := m.Entries(ctx, "alpha", kept.ID, "/data/world/datapacks")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, fmt.Sprintf("%s %v %d", e.Name, e.Dir, e.Size))
	}
	if strings.Join(got, ", ") != "a.zip false 5, b true 0, c true 0, we[ird]*.zip false 11, weirder[ true 0" {
		t.Errorf("listed %q", got)
	}
	// Several folders in one go, each entry where it is.
	entries, err = m.Entries(ctx, "alpha", kept.ID, "/data/world/datapacks/b", "/data/world/datapacks/weirder[")
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, e := range entries {
		got = append(got, fmt.Sprintf("%s %v %d", e.Path, e.Dir, e.Size))
	}
	if strings.Join(got, ", ") != "/data/world/datapacks/b/data true 0, /data/world/datapacks/b/pack.mcmeta false 3, /data/world/datapacks/weirder[/pack.mcmeta false 9" {
		t.Errorf("listed %q", got)
	}
	if entries, err := m.Entries(ctx, "alpha", kept.ID); err != nil || len(entries) != 0 {
		t.Errorf("no folders: %v, %v", entries, err)
	}
	if entries, err := m.Entries(ctx, "alpha", kept.ID, "/data/elsewhere"); err != nil || len(entries) != 0 {
		t.Errorf("a folder it does not have: %v, %v", entries, err)
	}
	if _, err := m.Entries(ctx, "alpha", kept.ID, "data/world/../x"); err == nil {
		t.Error("listed a folder that is not one")
	}
	out := t.TempDir()
	if err := m.Extract(ctx, "alpha", kept.ID, []string{"/data/world/datapacks/we[ird]*.zip", "/data/world/datapacks/b/pack.mcmeta", "/data/world/datapacks/b/pack.png", "/data/world/datapacks/weirder[/pack.mcmeta", "/data/world/datapacks/c/pack.mcmeta"}, out); err != nil {
		t.Fatal(err)
	}
	var taken []string
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(out, p)
			taken = append(taken, filepath.ToSlash(rel))
		}
		return nil
	})
	if strings.Join(taken, ", ") != "data/world/datapacks/b/pack.mcmeta, data/world/datapacks/we[ird]*.zip, data/world/datapacks/weirder[/pack.mcmeta" {
		t.Errorf("taken out: %q", taken)
	}
}
