package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Who has to use two-factor is set once: admins and owners on a panel just
// installed, and on one from before what its one switch said, on as admins
// and owners, off as nobody. What is set after stays.
func TestTwoFactorLevelMigration(t *testing.T) {
	ctx := context.Background()
	level := func(s *Store) string {
		t.Helper()
		v, err := s.Setting(ctx, SettingTwoFactorLevel)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// As a panel of before left it: no level, and the switch as given.
	before := func(path string, users, switchOn bool) {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if users {
			user(t, s, "someone")
		}
		if err := s.DeleteSetting(ctx, SettingTwoFactorLevel); err != nil {
			t.Fatal(err)
		}
		if switchOn {
			if err := s.SetSetting(ctx, settingAdminTwoFactor, "1"); err != nil {
				t.Fatal(err)
			}
		}
	}
	reopen := func(path string) *Store {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}

	if got := level(open(t)); got != TwoFactorAdmins {
		t.Errorf("a panel just installed: %q, want admins", got)
	}
	for _, c := range []struct {
		name            string
		users, switchOn bool
		want            string
	}{
		{"switch on", true, true, TwoFactorAdmins},
		{"switch off", true, false, TwoFactorNobody},
		{"no one yet", false, false, TwoFactorAdmins},
	} {
		path := filepath.Join(t.TempDir(), "before.db")
		before(path, c.users, c.switchOn)
		s := reopen(path)
		if got := level(s); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if _, err := s.Setting(ctx, settingAdminTwoFactor); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: the old switch is still there: %v", c.name, err)
		}
		// Set since, it stays as set.
		if err := s.SetSetting(ctx, SettingTwoFactorLevel, TwoFactorEveryone); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if got := level(reopen(path)); got != TwoFactorEveryone {
			t.Errorf("%s, set since: %q, want everyone", c.name, got)
		}
	}
}

func TestHighestRole(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	u, other := user(t, s, "u"), user(t, s, "other")
	if r, err := s.HighestRole(ctx, u.ID); err != nil || r != "" {
		t.Errorf("in no organization: %q %v", r, err)
	}
	a, b := &Org{Name: "A", Slug: "a"}, &Org{Name: "B", Slug: "b"}
	if err := s.CreateOrg(ctx, a, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrg(ctx, b, other.ID); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		org  *Org
		role Role
		want Role
	}{
		{a, RoleViewer, RoleViewer},
		{b, RoleAdmin, RoleAdmin},
		{a, RoleOperator, RoleAdmin},
		{b, RoleViewer, RoleOperator},
		{a, RoleOwner, RoleOwner},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO memberships (org_id, user_id, role, created_at) VALUES (?, ?, ?, 0) ON CONFLICT DO UPDATE SET role = excluded.role`, step.org.ID, u.ID, step.role); err != nil {
			t.Fatal(err)
		}
		if r, err := s.HighestRole(ctx, u.ID); err != nil || r != step.want {
			t.Errorf("%s in %s: highest %q %v, want %q", step.role, step.org.Slug, r, err, step.want)
		}
	}
}
