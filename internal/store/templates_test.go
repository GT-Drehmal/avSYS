package store

import (
	"context"
	"errors"
	"testing"
)

func TestTemplates(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	lobby := &Template{OrgID: a.ID, Name: "Lobby", Description: "Spawn", CreatedBy: "owner"}
	if err := s.CreateTemplate(ctx, lobby); err != nil || lobby.ID == "" {
		t.Fatalf("create: %v, id %q", err, lobby.ID)
	}
	// A name is the organization's once, whatever its case; another may have it.
	if err := s.CreateTemplate(ctx, &Template{OrgID: a.ID, Name: "lobby"}); !errors.Is(err, ErrTemplateNameTaken) {
		t.Errorf("same name again: %v", err)
	}
	if err := s.CreateTemplate(ctx, &Template{OrgID: b.ID, Name: "Lobby"}); err != nil {
		t.Errorf("same name in another organization: %v", err)
	}
	arena := &Template{OrgID: a.ID, Name: "Arena"}
	if err := s.CreateTemplate(ctx, arena); err != nil {
		t.Fatal(err)
	}
	list, err := s.Templates(ctx, a.ID)
	if err != nil || len(list) != 2 || list[0].Name != "Arena" || list[1].Description != "Spawn" {
		t.Fatalf("list: %+v, %v", list, err)
	}
	// Not another organization's.
	if _, err := s.Template(ctx, b.ID, lobby.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization's: %v", err)
	}
	lobby.Name, lobby.Current = "Hub", "abc"
	if err := s.UpdateTemplate(ctx, lobby); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Template(ctx, a.ID, lobby.ID); err != nil || got.Name != "Hub" || got.Current != "abc" {
		t.Errorf("updated: %+v, %v", got, err)
	}
	arena.Name = "HUB"
	if err := s.UpdateTemplate(ctx, arena); !errors.Is(err, ErrTemplateNameTaken) {
		t.Errorf("renamed to another's name: %v", err)
	}
	if err := s.DeleteTemplate(ctx, a.ID, lobby.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTemplate(ctx, a.ID, lobby.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
}
