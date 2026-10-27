package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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
	// The version used is set alone: a name given meanwhile stays.
	if err := s.SetTemplateCurrent(ctx, a.ID, lobby.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Template(ctx, a.ID, lobby.ID); err != nil || got.Name != "Hub" || got.Current != "" {
		t.Errorf("version set alone: %+v, %v", got, err)
	}
	if err := s.SetTemplateCurrent(ctx, b.ID, lobby.ID, "", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("set in another organization: %v", err)
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

// A table of templates from before they could be made of settings alone
// gets the column; settings given a version are dropped.
func TestTemplateSettingsAdded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "templates.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	org := &Org{Slug: "old", Name: "Old"}
	if err := s.CreateOrg(ctx, org, user(t, s, "owner").ID); err != nil {
		t.Fatal(err)
	}
	old := &Template{OrgID: org.ID, Name: "Old"}
	if err := s.CreateTemplate(ctx, old); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"settings", "changes", "world"} {
		if _, err := db.Exec(`ALTER TABLE templates DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	for range 2 {
		if s, err = Open(path); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Template(ctx, org.ID, old.ID); err != nil || got.Settings != "" || got.Changes != "" || got.World != "" {
			t.Errorf("after opening: %+v %v", got, err)
		}
		s.Close()
	}
	s, _ = Open(path)
	defer s.Close()
	made := &Template{OrgID: org.ID, Name: "Settings", Settings: `{"loader":"paper"}`}
	if err := s.CreateTemplate(ctx, made); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Template(ctx, org.ID, made.ID); got.Settings != made.Settings {
		t.Errorf("settings not kept: %q", got.Settings)
	}
	if err := s.SetTemplateCurrent(ctx, org.ID, made.ID, "", `{"resources":{"memory":"8Gi"}}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Template(ctx, org.ID, made.ID); got.Settings != "" || got.Changes != `{"resources":{"memory":"8Gi"}}` {
		t.Errorf("with a version: settings %q, changes %q", got.Settings, got.Changes)
	}
}
