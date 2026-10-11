package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func user(t *testing.T, s *Store, name string) *User {
	t.Helper()
	u := &User{Username: name}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOnlyFirstUserIsAdmin(t *testing.T) {
	s := open(t)
	if first := user(t, s, "first"); !first.IsAdmin {
		t.Error("first user should be the instance admin")
	}
	if second := user(t, s, "second"); second.IsAdmin {
		t.Error("second user should not be an admin")
	}
	if err := s.CreateUser(context.Background(), &User{Username: "FIRST"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate username (different case): err = %v, want ErrConflict", err)
	}
}

func TestSessionLookupAndExpiry(t *testing.T) {
	s, ctx := open(t), context.Background()
	u := user(t, s, "someone")
	token, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, token); err != nil || got.ID != u.ID {
		t.Errorf("SessionUser = %v, %v", got, err)
	}
	expired, _ := s.CreateSession(ctx, u.ID, -time.Minute)
	if _, err := s.SessionUser(ctx, expired); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session: err = %v, want ErrNotFound", err)
	}
	if _, err := s.SessionUser(ctx, "not-a-token"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: err = %v, want ErrNotFound", err)
	}
}

func TestInviteLimitsAndRoles(t *testing.T) {
	s, ctx := open(t), context.Background()
	owner, a, b := user(t, s, "owner"), user(t, s, "a"), user(t, s, "b")
	org := &Org{Slug: "friends", Name: "Friends"}
	if err := s.CreateOrg(ctx, org, owner.ID); err != nil {
		t.Fatal(err)
	}

	inv := &Invite{OrgID: org.ID, Role: RoleOperator, CreatedBy: "owner", MaxUses: 1}
	if err := s.CreateInvite(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, inv.Token, a.ID); err != nil {
		t.Fatal(err)
	}
	if role, _ := s.RoleIn(ctx, org.ID, a.ID); role != RoleOperator {
		t.Errorf("role = %q, want operator", role)
	}
	if _, err := s.AcceptInvite(ctx, inv.Token, b.ID); !errors.Is(err, ErrInviteUnusable) {
		t.Errorf("second use of a single-use invite: err = %v, want ErrInviteUnusable", err)
	}

	// An invite for a lower role must not demote someone, nor be used up by them.
	viewer := &Invite{OrgID: org.ID, Role: RoleViewer, CreatedBy: "owner", MaxUses: 1}
	if err := s.CreateInvite(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, viewer.Token, owner.ID); err != nil {
		t.Fatal(err)
	}
	if role, _ := s.RoleIn(ctx, org.ID, owner.ID); role != RoleOwner {
		t.Errorf("owner was demoted to %q", role)
	}
	if got, _ := s.Invite(ctx, viewer.Token); got.Uses != 0 {
		t.Errorf("uses = %d, want 0", got.Uses)
	}

	past := time.Now().Add(-time.Hour)
	expired := &Invite{OrgID: org.ID, Role: RoleViewer, CreatedBy: "owner", ExpiresAt: &past}
	if err := s.CreateInvite(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvite(ctx, expired.Token, b.ID); !errors.Is(err, ErrInviteUnusable) {
		t.Errorf("expired invite: err = %v, want ErrInviteUnusable", err)
	}
}

func TestAdminsSeeEveryOrgAsOwner(t *testing.T) {
	s, ctx := open(t), context.Background()
	admin, member := user(t, s, "admin"), user(t, s, "member")
	org := &Org{Slug: "gt-drehmal", Name: "GT Drehmal"}
	if err := s.CreateOrg(ctx, org, member.ID); err != nil {
		t.Fatal(err)
	}
	orgs, err := s.OrgsForUser(ctx, admin)
	if err != nil || len(orgs) != 1 || orgs[0].Role != RoleOwner {
		t.Errorf("admin sees %+v, err %v", orgs, err)
	}
	other := user(t, s, "outsider")
	if orgs, _ := s.OrgsForUser(ctx, other); len(orgs) != 0 {
		t.Errorf("outsider sees %+v", orgs)
	}
}

func TestAPIToken(t *testing.T) {
	s, ctx := open(t), context.Background()
	u := user(t, s, "scripter")
	secret, err := s.CreateAPIToken(ctx, &APIToken{UserID: u.ID, Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.UserByAPIToken(ctx, secret); err != nil || got.ID != u.ID {
		t.Errorf("UserByAPIToken = %v, %v", got, err)
	}
	if _, err := s.UserByAPIToken(ctx, secret+"x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong token: err = %v, want ErrNotFound", err)
	}
}
