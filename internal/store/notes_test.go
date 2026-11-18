package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestServerNotes(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	if notes, err := s.ServerNotes(ctx, a.ID, "lobby"); err != nil || notes != "" {
		t.Fatalf("none yet: %q, %v", notes, err)
	}
	if err := s.SetServerNotes(ctx, a.ID, "lobby", "Where everyone starts.\nReset weekly."); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerNotes(ctx, b.ID, "lobby", "Beta's own"); err != nil {
		t.Fatal(err)
	}
	if notes, err := s.ServerNotes(ctx, a.ID, "lobby"); err != nil || notes != "Where everyone starts.\nReset weekly." {
		t.Errorf("kept: %q, %v", notes, err)
	}
	// By the organization's slug, as the server's namespace names it.
	if notes, err := s.ServerNotesIn(ctx, "b", "lobby"); err != nil || notes != "Beta's own" {
		t.Errorf("by slug: %q, %v", notes, err)
	}
	if err := s.SetServerNotes(ctx, a.ID, "lobby", "Changed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerNotes(ctx, a.ID, "arena", "PvP"); err != nil {
		t.Fatal(err)
	}
	if all, err := s.AllServerNotes(ctx, a.ID); err != nil || !reflect.DeepEqual(all, map[string]string{"lobby": "Changed", "arena": "PvP"}) {
		t.Errorf("all: %v, %v", all, err)
	}
	// Nothing is no notes.
	if err := s.SetServerNotes(ctx, a.ID, "lobby", ""); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllServerNotes(ctx, a.ID); !reflect.DeepEqual(all, map[string]string{"arena": "PvP"}) {
		t.Errorf("after clearing: %v", all)
	}
	if err := s.DeleteServerNotes(ctx, a.ID, "arena"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.AllServerNotes(ctx, a.ID); len(all) != 0 {
		t.Errorf("after deleting: %v", all)
	}
	// Gone with the organization.
	if err := s.DeleteOrg(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if notes, _ := s.ServerNotesIn(ctx, "b", "lobby"); notes != "" {
		t.Errorf("an organization's notes outlived it: %q", notes)
	}
}

func TestSnapshotNames(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	owner := user(t, s, "owner")
	a := &Org{Slug: "a", Name: "A"}
	if err := s.CreateOrg(ctx, a, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSnapshotName(ctx, a.ID, "aaaa", SnapshotName{Name: "Before the update", Color: "teal", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSnapshotName(ctx, a.ID, "bbbb", SnapshotName{Color: "rose", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	names, err := s.SnapshotNames(ctx, a.ID)
	if err != nil || len(names) != 2 || names["aaaa"].Name != "Before the update" || names["aaaa"].Color != "teal" || names["bbbb"].Color != "rose" || names["aaaa"].By != "owner" {
		t.Fatalf("names: %+v, %v", names, err)
	}
	// Renamed in place.
	if err := s.SetSnapshotName(ctx, a.ID, "aaaa", SnapshotName{Name: "Old spawn"}); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.SnapshotNames(ctx, a.ID); names["aaaa"].Name != "Old spawn" || names["aaaa"].Color != "" {
		t.Errorf("renamed: %+v", names["aaaa"])
	}
	// No name and no colour is not named.
	if err := s.SetSnapshotName(ctx, a.ID, "bbbb", SnapshotName{}); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.SnapshotNames(ctx, a.ID); len(names) != 1 {
		t.Errorf("unnamed: %+v", names)
	}
	if err := s.DeleteSnapshotNames(ctx, a.ID, "aaaa", "nope"); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.SnapshotNames(ctx, a.ID); len(names) != 0 {
		t.Errorf("deleted: %+v", names)
	}

	// The names of snapshots no longer there go, but not one named since
	// the list of those there was read.
	for _, id := range []string{"kept", "gone"} {
		if err := s.SetSnapshotName(ctx, a.ID, id, SnapshotName{Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ForgetSnapshotNamesBut(ctx, a.ID, map[string]bool{"kept": true}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.SnapshotNames(ctx, a.ID); len(names) != 2 {
		t.Errorf("named after the list was read, and forgotten: %+v", names)
	}
	if err := s.ForgetSnapshotNamesBut(ctx, a.ID, map[string]bool{"kept": true}, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.SnapshotNames(ctx, a.ID); len(names) != 1 || names["kept"].Name != "kept" {
		t.Errorf("after forgetting what is gone: %+v", names)
	}
}
