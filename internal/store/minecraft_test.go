package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func names(t *testing.T, s *Store, u *User) string {
	t.Helper()
	all, err := s.MinecraftAccounts(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for _, a := range all {
		if out != "" {
			out += " "
		}
		out += a.Name
		if a.Main {
			out += "*"
		}
	}
	return out
}

// A person links several accounts. The first is their main one until they
// say another is; their main one gone, the one they linked first of the
// rest is.
func TestMinecraftAccounts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	steve := user(t, s, "steve")
	add := func(uuid, name string, main bool) {
		t.Helper()
		if err := s.AddMinecraftAccount(ctx, steve.ID, &MinecraftAccount{UUID: uuid, Name: name}, main); err != nil {
			t.Fatal(err)
		}
	}
	add("u1", "Steve", false)
	add("u2", "SteveAlt", false)
	add("u3", "SteveBedrock", false)
	if got := names(t, s, steve); got != "Steve* SteveAlt SteveBedrock" {
		t.Errorf("three linked: %q", got)
	}
	// Linked again, it keeps its place, under the name it has now.
	add("u2", "SteveTwo", false)
	if got := names(t, s, steve); got != "Steve* SteveTwo SteveBedrock" {
		t.Errorf("one linked again: %q", got)
	}
	if err := s.SetMainMinecraftAccount(ctx, steve.ID, "u3"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, steve); got != "SteveBedrock* Steve SteveTwo" {
		t.Errorf("another made main: %q", got)
	}
	if err := s.RemoveMinecraftAccount(ctx, steve.ID, "u3"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, steve); got != "Steve* SteveTwo" {
		t.Errorf("the main one unlinked: %q", got)
	}
	// Linked as main, it is.
	add("u4", "Notch", true)
	if got := names(t, s, steve); got != "Notch* Steve SteveTwo" {
		t.Errorf("one linked as main: %q", got)
	}
	main, err := s.MinecraftAccount(ctx, steve.ID)
	if err != nil || main.Name != "Notch" {
		t.Errorf("the main one: %+v, %v", main, err)
	}
	// What is not theirs is not found.
	if err := s.SetMainMinecraftAccount(ctx, steve.ID, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("making main one not linked: %v", err)
	}
	if err := s.RemoveMinecraftAccount(ctx, steve.ID, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unlinking one not linked: %v", err)
	}
	if got := names(t, s, steve); got != "Notch* Steve SteveTwo" {
		t.Errorf("after those: %q", got)
	}
	// Unlinking the main one, as the one-account route does, leaves the rest.
	if err := s.ClearMinecraftAccount(ctx, steve.ID); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, steve); got != "Steve* SteveTwo" {
		t.Errorf("the main one cleared: %q", got)
	}
}

func TestMinecraftAccountsLimit(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	u := user(t, s, "many")
	for i := range MaxMinecraftAccounts {
		if err := s.AddMinecraftAccount(ctx, u.ID, &MinecraftAccount{UUID: fmt.Sprint(i), Name: fmt.Sprint("p", i)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddMinecraftAccount(ctx, u.ID, &MinecraftAccount{UUID: "one-more", Name: "OneMore"}, false); !errors.Is(err, ErrTooManyAccounts) {
		t.Errorf("one past the limit: %v", err)
	}
	// One linked already is not one more.
	if err := s.AddMinecraftAccount(ctx, u.ID, &MinecraftAccount{UUID: "0", Name: "p0"}, true); err != nil {
		t.Errorf("one linked already, again: %v", err)
	}
}

// A rename is of the account, for everyone who linked it; members list all
// of each other's accounts, the main one as their name.
func TestMinecraftAccountsOfMembers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, b := user(t, s, "a"), user(t, s, "b")
	org := &Org{Name: "Org", Slug: "org"}
	if err := s.CreateOrg(ctx, org, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO memberships (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`, org.ID, b.ID, RoleViewer, 1); err != nil {
		t.Fatal(err)
	}
	for _, l := range []struct {
		u    *User
		uuid string
		name string
	}{{a, "x", "Alpha"}, {a, "y", "AlphaAlt"}, {b, "x", "Alpha"}} {
		if err := s.AddMinecraftAccount(ctx, l.u.ID, &MinecraftAccount{UUID: l.uuid, Name: l.name}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RenameMinecraftAccount(ctx, "x", "Renamed"); err != nil {
		t.Fatal(err)
	}
	members, err := s.Members(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range members {
		list := m.MinecraftName + ":"
		for _, acc := range m.Minecraft {
			list += " " + acc.Name
		}
		got[m.Username] = list
	}
	if got["a"] != "Renamed: Renamed AlphaAlt" || got["b"] != "Renamed: Renamed" {
		t.Errorf("members: %v", got)
	}
}

// A panel from before several accounts could be linked has each user's one
// as their main one, once.
func TestMinecraftAccountsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u := user(t, s, "old")
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE minecraft_accounts (user_id TEXT PRIMARY KEY, uuid TEXT NOT NULL, name TEXT NOT NULL, linked_at INTEGER NOT NULL)`,
		`INSERT INTO minecraft_accounts VALUES ('` + u.ID + `', 'uuid-1', 'OldName', 1700000000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	for range 2 {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(t, s, u); got != "OldName*" {
			t.Errorf("after opening: %q", got)
		}
		// Linked before accounts could be proven: taken as proven.
		if all, _ := s.MinecraftAccounts(context.Background(), u.ID); len(all) != 1 || !all[0].Verified {
			t.Errorf("an account from before is not taken as proven: %+v", all)
		}
		s.Close()
	}
}

// A table of several accounts from before they could be proven gets the
// column, with every account in it taken as proven; a new one is not.
func TestVerifiedAtAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u := user(t, s, "u")
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE minecraft_links`,
		`CREATE TABLE minecraft_links (user_id TEXT NOT NULL, uuid TEXT NOT NULL, name TEXT NOT NULL, linked_at INTEGER NOT NULL, main INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (user_id, uuid))`,
		`INSERT INTO minecraft_links VALUES ('` + u.ID + `', 'a', 'Before', 1700000000, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.AddMinecraftAccount(ctx, u.ID, &MinecraftAccount{UUID: "b", Name: "After"}, false); err != nil {
		t.Fatal(err)
	}
	all, _ := s.MinecraftAccounts(ctx, u.ID)
	if len(all) != 2 || !all[0].Verified || all[1].Verified {
		t.Errorf("%+v", all)
	}
	if err := s.VerifyMinecraftAccount(ctx, u.ID, "b"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.MinecraftAccounts(ctx, u.ID); !all[1].Verified {
		t.Errorf("proven, not recorded: %+v", all)
	}
	if err := s.VerifyMinecraftAccount(ctx, u.ID, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("proving one not linked: %v", err)
	}
}
