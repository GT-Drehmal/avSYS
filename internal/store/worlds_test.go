package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestWorlds(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	island := &World{OrgID: a.ID, Name: "Island", Description: "A small one", CreatedBy: "owner", Root: "Island", Version: "1.21.4", File: "island.zip", Size: 10, Source: "upload"}
	if err := s.CreateWorld(ctx, island); err != nil || island.ID == "" {
		t.Fatalf("create: %v, id %q", err, island.ID)
	}
	// A name is the organization's once, whatever its case; another may have it.
	if err := s.CreateWorld(ctx, &World{OrgID: a.ID, Name: "island"}); !errors.Is(err, ErrWorldNameTaken) {
		t.Errorf("same name again: %v", err)
	}
	if err := s.CreateWorld(ctx, &World{OrgID: b.ID, Name: "Island"}); err != nil {
		t.Errorf("same name in another organization: %v", err)
	}
	arena := &World{OrgID: a.ID, Name: "Arena", Source: "server:arena"}
	if err := s.CreateWorld(ctx, arena); err != nil {
		t.Fatal(err)
	}
	list, err := s.Worlds(ctx, a.ID)
	if err != nil || len(list) != 2 || list[0].Name != "Arena" || list[1].Root != "Island" || list[1].Size != 10 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if _, err := s.World(ctx, b.ID, island.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization's: %v", err)
	}
	// Renamed, but not to another's name.
	arena.Name = "ISLAND"
	if err := s.UpdateWorld(ctx, arena); !errors.Is(err, ErrWorldNameTaken) {
		t.Errorf("renamed to another's name: %v", err)
	}
	arena.Name, arena.Description = "Arena 2", "Reset each round"
	if err := s.UpdateWorld(ctx, arena); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorldFailed(ctx, a.ID, arena.ID, "no world"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.World(ctx, a.ID, arena.ID); err != nil || got.Failed != "no world" {
		t.Errorf("failed: %+v, %v", got, err)
	}
	if err := s.SetWorldArchive(ctx, a.ID, arena.ID, "world", "1.21.5", 42, 84); err != nil {
		t.Fatal(err)
	}
	if got, err := s.World(ctx, a.ID, arena.ID); err != nil || got.Name != "Arena 2" || got.Description != "Reset each round" || got.Root != "world" || got.Version != "1.21.5" || got.Size != 42 || got.Unpacked != 84 || got.Source != "server:arena" || got.Failed != "" {
		t.Errorf("changed: %+v, %v", got, err)
	}
	// A resource pack named, kept with the rest, and taken off again.
	if err := s.SetWorldPack(ctx, a.ID, arena.ID, `{"url":"https://example.com/p.zip"}`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.World(ctx, a.ID, arena.ID); err != nil || got.Pack != `{"url":"https://example.com/p.zip"}` || got.Name != "Arena 2" || got.Size != 42 {
		t.Errorf("given a pack: %+v, %v", got, err)
	}
	if err := s.SetWorldPack(ctx, b.ID, arena.ID, "{}"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization's given a pack: %v", err)
	}
	if err := s.SetWorldPack(ctx, a.ID, arena.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.World(ctx, a.ID, arena.ID); got.Pack != "" {
		t.Errorf("its pack stayed: %q", got.Pack)
	}
	withPack := &World{OrgID: a.ID, Name: "Map", Pack: `{"url":"https://example.com/m.zip"}`}
	if err := s.CreateWorld(ctx, withPack); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.World(ctx, a.ID, withPack.ID); got.Pack != withPack.Pack {
		t.Errorf("made with a pack: %q", got.Pack)
	}
	// Its picture goes with it.
	if err := s.SetImage(ctx, WorldImage(arena.ID), []byte("png")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorld(ctx, a.ID, arena.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorld(ctx, a.ID, arena.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
	if data, _, err := s.Image(ctx, WorldImage(arena.ID)); data != nil {
		t.Errorf("its picture stayed: %q, %v", data, err)
	}
}

// A table of worlds from before a world could name a resource pack gets the
// column, and the worlds in it have none.
func TestWorldPackAdded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "worlds.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{Slug: "old", Name: "Old"}
	if err := s.CreateOrg(ctx, org, user(t, s, "owner").ID); err != nil {
		t.Fatal(err)
	}
	old := &World{OrgID: org.ID, Name: "Old", Root: "world"}
	if err := s.CreateWorld(ctx, old); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE worlds DROP COLUMN pack`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 {
		if s, err = Open(path); err != nil {
			t.Fatal(err)
		}
		if got, err := s.World(ctx, org.ID, old.ID); err != nil || got.Pack != "" || got.Root != "world" {
			t.Errorf("after opening: %+v %v", got, err)
		}
		s.Close()
	}
	s, _ = Open(path)
	defer s.Close()
	if err := s.SetWorldPack(ctx, org.ID, old.ID, `{"url":"https://example.com/p.zip"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.World(ctx, org.ID, old.ID); got.Pack == "" {
		t.Error("an old world was not given a pack")
	}
}

// What was read of a world's datapacks is kept with it, their pictures by
// name, each time in place of what it had; not another organization's, and
// gone with the world. A table of worlds from before gets the column, its
// worlds' not read yet.
func TestWorldDatapacks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "worlds.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	old := &World{OrgID: a.ID, Name: "Old", Root: "world"}
	other := &World{OrgID: a.ID, Name: "Other", Root: "world"}
	for _, w := range []*World{old, other} {
		if err := s.CreateWorld(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE worlds DROP COLUMN datapacks`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.World(ctx, a.ID, old.ID); err != nil || got.Datapacks != "" || got.Root != "world" {
		t.Fatalf("from before: %+v %v", got, err)
	}

	if err := s.SetWorldDatapacks(ctx, a.ID, old.ID, `[{"name":"a.zip"},{"name":"b"}]`, map[string][]byte{"a.zip": []byte("a"), "b": []byte("b")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorldDatapacks(ctx, a.ID, other.ID, `[{"name":"a.zip"}]`, map[string][]byte{"a.zip": []byte("other a")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorldDatapacks(ctx, b.ID, old.ID, `[]`, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization's: %v", err)
	}
	was, _ := s.World(ctx, a.ID, old.ID)
	if was.Datapacks != `[{"name":"a.zip"},{"name":"b"}]` {
		t.Errorf("kept %q", was.Datapacks)
	}
	// Read again: what it no longer has goes.
	if err := s.SetWorldDatapacks(ctx, a.ID, old.ID, `[{"name":"b"}]`, map[string][]byte{"b": []byte("b2")}); err != nil {
		t.Fatal(err)
	}
	if data, _, err := s.Image(ctx, WorldDatapackImage(old.ID, "a.zip")); !errors.Is(err, ErrNotFound) {
		t.Errorf("a picture it no longer has: %q %v", data, err)
	}
	if data, _, _ := s.Image(ctx, WorldDatapackImage(old.ID, "b")); string(data) != "b2" {
		t.Errorf("its picture: %q", data)
	}
	if got, _ := s.World(ctx, a.ID, old.ID); got.UpdatedAt != was.UpdatedAt {
		t.Error("said to be changed")
	}
	if err := s.DeleteWorld(ctx, a.ID, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Image(ctx, WorldDatapackImage(old.ID, "b")); !errors.Is(err, ErrNotFound) {
		t.Errorf("a picture stayed with its world gone: %v", err)
	}
	if data, _, _ := s.Image(ctx, WorldDatapackImage(other.ID, "a.zip")); string(data) != "other a" {
		t.Errorf("another world's picture: %q", data)
	}
}
