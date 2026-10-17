package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MinecraftAccount is a player a user says they are in game. A user may
// have several, as a main and an alt; one is their main one, the name shown
// beside them. Nothing proves an account is theirs, so it is only ever used
// to give them in game what their role lets them give any player.
type MinecraftAccount struct {
	// UUID is the account, which keeps its UUID when its name changes.
	UUID string `json:"uuid"`
	// Name is what the account is called, as last seen.
	Name     string    `json:"name"`
	LinkedAt time.Time `json:"linkedAt"`
	// Main is set on the one shown beside them.
	Main bool `json:"main"`
}

// MaxMinecraftAccounts is the most accounts one user links.
const MaxMinecraftAccounts = 10

// MinecraftAccounts returns the accounts a user has linked, their main one
// first, then in the order they were linked.
func (s *Store) MinecraftAccounts(ctx context.Context, userID string) ([]MinecraftAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uuid, name, linked_at, main FROM minecraft_links WHERE user_id = ? ORDER BY main DESC, linked_at, rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MinecraftAccount{}
	for rows.Next() {
		var a MinecraftAccount
		var linked int64
		if err := rows.Scan(&a.UUID, &a.Name, &linked, &a.Main); err != nil {
			return nil, err
		}
		a.LinkedAt = unix(linked)
		out = append(out, a)
	}
	return out, rows.Err()
}

// MinecraftAccount returns a user's main account, or ErrNotFound.
func (s *Store) MinecraftAccount(ctx context.Context, userID string) (*MinecraftAccount, error) {
	all, err := s.MinecraftAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNotFound
	}
	return &all[0], nil
}

// ErrTooManyAccounts is returned for a link past MaxMinecraftAccounts.
var ErrTooManyAccounts = errors.New("too many Minecraft accounts")

// AddMinecraftAccount links an account to a user, beside any they have. One
// linked already keeps its place and takes the name given. The first one a
// user links is their main one, and so is one linked with main.
func (s *Store) AddMinecraftAccount(ctx context.Context, userID string, a *MinecraftAccount, main bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count, has int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(uuid = ?), 0) FROM minecraft_links WHERE user_id = ?`, a.UUID, userID).Scan(&count, &has); err != nil {
		return err
	}
	if has == 0 && count >= MaxMinecraftAccounts {
		return ErrTooManyAccounts
	}
	a.Main = main || count == 0 || (count == 1 && has == 1)
	if a.Main {
		if _, err := tx.ExecContext(ctx, `UPDATE minecraft_links SET main = 0 WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	a.LinkedAt = time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO minecraft_links (user_id, uuid, name, linked_at, main) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, uuid) DO UPDATE SET name = excluded.name, main = max(minecraft_links.main, excluded.main)`,
		userID, a.UUID, a.Name, a.LinkedAt.Unix(), a.Main); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMainMinecraftAccount makes one of a user's accounts their main one.
func (s *Store) SetMainMinecraftAccount(ctx context.Context, userID, uuid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE minecraft_links SET main = (uuid = ?) WHERE user_id = ?`, uuid, userID)
	if err != nil {
		return err
	}
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM minecraft_links WHERE user_id = ? AND uuid = ?`, userID, uuid).Scan(&found); err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 || found == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// RemoveMinecraftAccount unlinks one of a user's accounts. Their main one
// gone, the one they linked first of the rest is their main one.
func (s *Store) RemoveMinecraftAccount(ctx context.Context, userID, uuid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM minecraft_links WHERE user_id = ? AND uuid = ?`, userID, uuid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := promoteMain(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// promoteMain makes the first linked of a user's accounts their main one if
// none is.
func promoteMain(ctx context.Context, tx *sql.Tx, userID string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE minecraft_links SET main = 1 WHERE user_id = ?1 AND NOT EXISTS (SELECT 1 FROM minecraft_links WHERE user_id = ?1 AND main = 1)
		 AND uuid = (SELECT uuid FROM minecraft_links WHERE user_id = ?1 ORDER BY linked_at, rowid LIMIT 1)`, userID)
	return err
}

// RenameMinecraftAccount records that an account is now called something
// else, for every user who linked it. It is still the same account.
func (s *Store) RenameMinecraftAccount(ctx context.Context, uuid, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE minecraft_links SET name = ? WHERE uuid = ?`, name, uuid)
	return err
}

// ClearMinecraftAccount unlinks a user's main account, if they have one.
func (s *Store) ClearMinecraftAccount(ctx context.Context, userID string) error {
	main, err := s.MinecraftAccount(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.RemoveMinecraftAccount(ctx, userID, main.UUID)
}

// SetMinecraftAccount links an account as a user's main one.
func (s *Store) SetMinecraftAccount(ctx context.Context, userID string, a *MinecraftAccount) error {
	return s.AddMinecraftAccount(ctx, userID, a, true)
}
