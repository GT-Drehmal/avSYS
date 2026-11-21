package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// bound sets one of the bounds on what is taken out of a snapshot for the
// test, and back after it.
func bound[T any](t *testing.T, v *T, to T) {
	was := *v
	*v = to
	t.Cleanup(func() { *v = was })
}

// What is taken out of a world's snapshot to read its datapacks is held to
// the datapacks read, as many as are read, each .zip no larger than a
// datapack's may be, a folder's pack.mcmeta and pack.png no larger than a
// picture may be, and so much in all.
func TestKeptDatapacksPlanBounded(t *testing.T) {
	dir := "/data/world/datapacks"
	file := func(name string, size int64) backup.Entry {
		return backup.Entry{Path: dir + "/" + name, Name: name[strings.LastIndex(name, "/")+1:], Size: size}
	}
	folder := func(name string) backup.Entry {
		return backup.Entry{Path: dir + "/" + name, Name: name, Dir: true}
	}
	top := []backup.Entry{
		file("a.zip", 100),
		folder("b"),
		file("big.zip", maxKeptDatapackZip+1),
		folder("bukkit"),
		folder("c"),
		file(".hidden.zip", 100),
		file("d.zip", 100),
		file("e.zip", 100),
		file("notes.txt", 100),
	}
	inner := []backup.Entry{
		file("b/pack.mcmeta", 10),
		file("b/pack.png", maxPictureBytes+1),
		folder("b/data"),
		file("bukkit/pack.mcmeta", 10),
		folder("c/data"),
	}
	plan := func() string { return strings.Join(keptDatapacksPlan(top, inner).take, " ") }
	all := dir + "/a.zip " + dir + "/b/pack.mcmeta " + dir + "/d.zip " + dir + "/e.zip"
	if got := plan(); got != all {
		t.Errorf("taken out: %s\nwant %s", got, all)
	}
	if got := keptDatapacksPlan(top, inner).bytes; got != 310 {
		t.Errorf("%d bytes in all", got)
	}
	// As many as are read: the .zip too large is one of them, though
	// nothing of it is taken out.
	t.Run("count", func(t *testing.T) {
		bound(t, &maxFound, 3)
		if got, want := plan(), dir+"/a.zip "+dir+"/b/pack.mcmeta"; got != want {
			t.Errorf("taken out: %s\nwant %s", got, want)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		bound(t, &maxKeptDatapackBytes, 150)
		if got, want := plan(), dir+"/a.zip "+dir+"/b/pack.mcmeta"; got != want {
			t.Errorf("taken out: %s\nwant %s", got, want)
		}
	})
	t.Run("zip", func(t *testing.T) {
		bound(t, &maxKeptDatapackZip, 99)
		if got, want := plan(), dir+"/b/pack.mcmeta"; got != want {
			t.Errorf("taken out: %s\nwant %s", got, want)
		}
	})
}

// Read from a snapshot, a datapack whose .zip is too large to take out is
// listed by its name alone, and one whose picture is too large has none;
// and nothing is taken out without room for it, besides the room kept free
// while a world is unpacked.
func TestKeptDatapacksReadWithinBounds(t *testing.T) {
	p, _, _ := libraryPanel(t)
	ctx := context.Background()
	src := t.TempDir()
	big := jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Big"}}`, "filler": randomBytes(t, 4<<10)})
	writeFiles(t, src, map[string]string{
		"data/world/level.dat":                   "the world",
		"data/world/datapacks/big.zip":           string(big),
		"data/world/datapacks/small.zip":         string(jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":"Small"}}`, "pack.png": aPicture(t)})),
		"data/world/datapacks/heavy/pack.mcmeta": `{"pack":{"description":"Heavy"}}`,
		"data/world/datapacks/heavy/pack.png":    aPicture(t) + randomBytes(t, int(maxPictureBytes)),
	})
	snap, err := p.srv.Backups.KeepFolders(ctx, "alpha", src, []string{"data/world"}, "world-bounds", "owner")
	if err != nil {
		t.Fatal(err)
	}
	bound(t, &maxKeptDatapackZip, int64(len(big)-1))
	list, pictures, err := p.srv.readKeptDatapacks(ctx, "alpha", snap, "world")
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"big.zip","title":"big"},{"name":"heavy","title":"heavy","about":"Heavy","folder":true},{"name":"small.zip","title":"small","about":"Small","picture":true}]`
	if got := jsonOf(t, list); got != want {
		t.Errorf("read %s\nwant %s", got, want)
	}
	if len(pictures) != 1 || pictures["small.zip"] == nil {
		t.Errorf("pictures of %v", keysOf(bytesKeys(pictures)))
	}

	// No room for what is to be taken out, the room kept free aside.
	bound(t, &datapackRoom, func(string) (int64, error) { return worldRoomReserve + 10, nil })
	_, _, err = p.srv.readKeptDatapacks(ctx, "alpha", snap, "world")
	var e *Error
	if !errors.As(err, &e) || e.Code != "no_room" {
		t.Errorf("without room: %v", err)
	}
	// A world with no datapacks needs none.
	if list, _, err := p.srv.readKeptDatapacks(ctx, "alpha", snap, "elsewhere"); err != nil || len(list) != 0 {
		t.Errorf("none, without room: %v, %v", list, err)
	}
	if left, _ := os.ReadDir(p.srv.Backups.WorkDir("alpha")); len(left) != 0 {
		t.Errorf("left taken out: %v", left)
	}
}
