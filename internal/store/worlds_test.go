package store

import (
	"context"
	"errors"
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
