package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Sessions from before they could be listed are given what a list needs,
// and still sign in.
func TestSessionDetailsAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
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
		`DROP TABLE sessions`,
		`CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`INSERT INTO sessions VALUES ('` + hashToken("old-one") + `', '` + u.ID + `', 1700000000, 4000000000)`,
		`INSERT INTO sessions VALUES ('` + hashToken("old-two") + `', '` + u.ID + `', 1700000001, 4000000000)`,
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
	got, session, err := s.SessionByToken(ctx, "old-one", SessionInfo{UserAgent: "Firefox", Address: "10.0.0.1"})
	if err != nil || got.ID != u.ID {
		t.Fatalf("an old session: %v, %v", got, err)
	}
	list, err := s.Sessions(ctx, u.ID, "old-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID == "" || list[1].ID == "" || list[0].ID == list[1].ID {
		t.Fatalf("old sessions: %+v", list)
	}
	if list[0].ID != session.ID || !list[0].Current || list[0].UserAgent != "Firefox" || list[0].Address != "10.0.0.1" || list[1].Current {
		t.Errorf("the session used: %+v", list)
	}
	if list[1].LastUsedAt.Unix() != 1700000001 {
		t.Errorf("an old session not used since is last used %v, want when it was made", list[1].LastUsedAt)
	}
	if err := s.EndSession(ctx, u.ID, list[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "old-two"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an ended session still signs in: %v", err)
	}
}

// Of two uses of one reset link at once, one sets the password.
func TestResetLinkIsUsedOnce(t *testing.T) {
	s, ctx := open(t), context.Background()
	u := user(t, s, "u")
	token, err := s.CreateResetLink(ctx, &ResetLink{UserID: u.ID, CreatedBy: "admin"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := s.UseResetLink(ctx, token, "hash")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrNotFound):
			t.Errorf("a use: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d uses went through, want 1", ok)
	}
}

// Of two sign-ins with one code at once, one is let in.
func TestTwoFactorStepIsTakenOnce(t *testing.T) {
	s, ctx := open(t), context.Background()
	u := user(t, s, "u")
	if err := s.SetPendingTwoFactor(ctx, u.ID, "SECRET"); err != nil {
		t.Fatal(err)
	}
	// Confirming with a secret other than the one pending does nothing.
	if err := s.EnableTwoFactor(ctx, u.ID, "OTHER", 10, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("enabling another secret: %v", err)
	}
	if err := s.EnableTwoFactor(ctx, u.ID, "SECRET", 10, []string{"aaaaa-aaaaa"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	taken := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			ok, err := s.UseTwoFactorStep(ctx, u.ID, 11)
			if err != nil {
				t.Error(err)
			}
			taken <- ok
		})
	}
	wg.Wait()
	close(taken)
	n := 0
	for ok := range taken {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("step 11 was taken %d times, want 1", n)
	}
	if ok, _ := s.UseTwoFactorStep(ctx, u.ID, 10); ok {
		t.Error("a step before the last one used was taken")
	}
	// With it off, no step is taken at all.
	if err := s.DisableTwoFactor(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.UseTwoFactorStep(ctx, u.ID, 99); ok {
		t.Error("a step was taken with two-factor off")
	}
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, "aaaaa-aaaaa"); ok {
		t.Error("a recovery code outlived two-factor")
	}
}
