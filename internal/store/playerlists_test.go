package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func playerNamesOf(l *PlayerList) string {
	var out []string
	for _, p := range l.Players {
		out = append(out, p.Name)
	}
	return strings.Join(out, " ")
}

func TestPlayerLists(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	club := &PlayerList{OrgID: a.ID, Name: "Club", CreatedBy: "owner", Players: []ListPlayer{{UUID: "u-steve", Name: "Steve"}, {UUID: "u-alex", Name: "alex"}, {UUID: "u-steve", Name: "Steve"}}}
	if err := s.CreatePlayerList(ctx, club); err != nil || club.ID == "" {
		t.Fatalf("create: %v, id %q", err, club.ID)
	}
	// Each player once, by name whatever its case.
	if got := playerNamesOf(club); got != "alex Steve" {
		t.Errorf("players made with: %q", got)
	}
	// A name is the organization's once, whatever its case; another may have it.
	if err := s.CreatePlayerList(ctx, &PlayerList{OrgID: a.ID, Name: "club"}); !errors.Is(err, ErrPlayerListNameTaken) {
		t.Errorf("same name again: %v", err)
	}
	if err := s.CreatePlayerList(ctx, &PlayerList{OrgID: b.ID, Name: "Club"}); err != nil {
		t.Errorf("same name in another organization: %v", err)
	}
	event := &PlayerList{OrgID: a.ID, Name: "Bingo event"}
	if err := s.CreatePlayerList(ctx, event); err != nil {
		t.Fatal(err)
	}
	lists, err := s.PlayerLists(ctx, a.ID)
	if err != nil || len(lists) != 2 || lists[0].Name != "Bingo event" || len(lists[0].Players) != 0 || playerNamesOf(&lists[1]) != "alex Steve" {
		t.Fatalf("lists: %+v, %v", lists, err)
	}
	if _, err := s.PlayerList(ctx, b.ID, club.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization's: %v", err)
	}

	// Added: one on it already keeps their place, with the name given now.
	if err := s.AddListPlayers(ctx, a.ID, club.ID, []ListPlayer{{UUID: "u-notch", Name: "Notch"}, {UUID: "u-alex", Name: "Alex"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PlayerList(ctx, a.ID, club.ID)
	if err != nil || playerNamesOf(got) != "Alex Notch Steve" {
		t.Errorf("after adding: %q, %v", playerNamesOf(got), err)
	}
	if err := s.AddListPlayers(ctx, b.ID, club.ID, []ListPlayer{{UUID: "u-x", Name: "X"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("adding to another organization's: %v", err)
	}
	if err := s.RemoveListPlayer(ctx, a.ID, club.ID, "u-notch"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveListPlayer(ctx, a.ID, club.ID, "u-notch"); !errors.Is(err, ErrNotFound) {
		t.Errorf("taking off one not on it: %v", err)
	}
	// A player renamed is renamed on every list.
	if err := s.AddListPlayers(ctx, a.ID, event.ID, []ListPlayer{{UUID: "u-steve", Name: "Steve"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameListPlayer(ctx, "u-steve", "Stefan"); err != nil {
		t.Fatal(err)
	}
	lists, _ = s.PlayerLists(ctx, a.ID)
	if playerNamesOf(&lists[0]) != "Stefan" || playerNamesOf(&lists[1]) != "Alex Stefan" {
		t.Errorf("after a rename: %q, %q", playerNamesOf(&lists[0]), playerNamesOf(&lists[1]))
	}

	if err := s.RenamePlayerList(ctx, a.ID, event.ID, "Club"); !errors.Is(err, ErrPlayerListNameTaken) {
		t.Errorf("renamed as another: %v", err)
	}
	if err := s.RenamePlayerList(ctx, a.ID, event.ID, "Bingo night"); err != nil {
		t.Fatal(err)
	}

	// Full: none of a batch that would take it past the limit is added.
	var many []ListPlayer
	for i := range MaxListPlayers - 1 {
		many = append(many, ListPlayer{UUID: fmt.Sprintf("u-%d", i), Name: fmt.Sprintf("P%d", i)})
	}
	if err := s.AddListPlayers(ctx, a.ID, club.ID, many); !errors.Is(err, ErrPlayerListFull) {
		t.Errorf("past the limit: %v", err)
	}
	if got, _ := s.PlayerList(ctx, a.ID, club.ID); len(got.Players) != 2 {
		t.Errorf("players after a batch past the limit: %d", len(got.Players))
	}

	// Deleted, with its players.
	if err := s.DeletePlayerList(ctx, a.ID, club.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlayerList(ctx, a.ID, club.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again: %v", err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT count(*) FROM player_list_entries WHERE list_id = ?`, club.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("players of a deleted list left: %d, %v", left, err)
	}
	// And with their organization.
	if err := s.DeleteOrg(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if lists, _ := s.PlayerLists(ctx, a.ID); len(lists) != 0 {
		t.Errorf("lists of a deleted organization: %d", len(lists))
	}
}
