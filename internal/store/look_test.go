package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Someone's look is set a part at a time: the part not given stays.
func TestUserLook(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u := user(t, s, "steve")
	if u.Theme != "auto" || u.Accent != "" {
		t.Fatalf("a new account: %q %q", u.Theme, u.Accent)
	}
	light, gold, none := "light", "gold", ""
	for _, step := range []struct {
		theme, accent         *string
		wantTheme, wantAccent string
	}{
		{&light, nil, "light", ""},
		{nil, &gold, "light", "gold"},
		{nil, &none, "light", ""},
	} {
		if err := s.SetUserLook(ctx, u.ID, step.theme, step.accent); err != nil {
			t.Fatal(err)
		}
		got, err := s.User(ctx, u.ID)
		if err != nil || got.Theme != step.wantTheme || got.Accent != step.wantAccent {
			t.Errorf("got %q %q (%v), want %q %q", got.Theme, got.Accent, err, step.wantTheme, step.wantAccent)
		}
	}
}

// A table of users from before the panel's look could be picked is given
// the columns for it, with each account following the system and the
// panel's accent; opening it again changes nothing.
func TestUserLookAdded(t *testing.T) {
	for name, drop := range map[string][]string{"before": {"theme", "accent"}, "half way": {"accent"}} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "look.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			id := user(t, s, "steve").ID
			s.Close()
			db, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			for _, column := range drop {
				if _, err := db.Exec(`ALTER TABLE users DROP COLUMN ` + column); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			for range 2 {
				if s, err = Open(path); err != nil {
					t.Fatal(err)
				}
				got, err := s.User(context.Background(), id)
				if err != nil || got.Username != "steve" || got.Theme != "auto" || got.Accent != "" {
					t.Errorf("after opening: %+v, %v", got, err)
				}
				s.Close()
			}
		})
	}
}
