package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Images are the small pictures that stand for an organization or a person.
// They are kept as the panel made them: PNG, square, a fixed size.

// OrgImage and UserImage name whose picture one is.
func OrgImage(orgID string) string   { return "org:" + orgID }
func UserImage(userID string) string { return "user:" + userID }

// Image returns a stored picture and when it was last set, or ErrNotFound.
func (s *Store) Image(ctx context.Context, key string) ([]byte, time.Time, error) {
	var data []byte
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT data, updated_at FROM images WHERE key = ?`, key).Scan(&data, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	return data, unix(updated), nil
}

// SetImage stores a picture in place of any there was.
func (s *Store) SetImage(ctx context.Context, key string, data []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO images (key, data, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, key, data, now())
	return err
}

// DeleteImage removes a picture, if there is one.
func (s *Store) DeleteImage(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM images WHERE key = ?`, key)
	return err
}

// SetAvatarURL sets where a user's picture is fetched from, or with "" that
// they have none.
func (s *Store) SetAvatarURL(ctx context.Context, userID, url string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET avatar_url = ? WHERE id = ?`, url, userID)
	return err
}
