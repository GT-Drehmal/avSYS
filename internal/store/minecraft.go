package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MinecraftAccount is the player a user says they are in game. Nothing
// proves it is theirs, so it is only ever used to give them in game what
// their role lets them give any player.
type MinecraftAccount struct {
	// UUID is the account, which keeps its UUID when its name changes.
	UUID string `json:"uuid"`
	// Name is what the account was called when it was linked.
	Name     string    `json:"name"`
	LinkedAt time.Time `json:"linkedAt"`
}

// MinecraftAccount returns the account a user has linked, or ErrNotFound.
func (s *Store) MinecraftAccount(ctx context.Context, userID string) (*MinecraftAccount, error) {
	var a MinecraftAccount
	var linked int64
	err := s.db.QueryRowContext(ctx, `SELECT uuid, name, linked_at FROM minecraft_accounts WHERE user_id = ?`, userID).Scan(&a.UUID, &a.Name, &linked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.LinkedAt = unix(linked)
	return &a, nil
}

// SetMinecraftAccount links an account to a user, in place of any they had.
func (s *Store) SetMinecraftAccount(ctx context.Context, userID string, a *MinecraftAccount) error {
	a.LinkedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO minecraft_accounts (user_id, uuid, name, linked_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET uuid = excluded.uuid, name = excluded.name, linked_at = excluded.linked_at`,
		userID, a.UUID, a.Name, a.LinkedAt.Unix())
	return err
}

// ClearMinecraftAccount unlinks a user's account, if they have one.
func (s *Store) ClearMinecraftAccount(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM minecraft_accounts WHERE user_id = ?`, userID)
	return err
}
