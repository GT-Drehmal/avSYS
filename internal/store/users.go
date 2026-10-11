package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName"`
	Email        string    `json:"email"`
	AvatarURL    string    `json:"avatarUrl"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"isAdmin"`
	CreatedAt    time.Time `json:"createdAt"`
}

const userColumns = `id, username, display_name, email, avatar_url, password_hash, is_admin, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.AvatarURL, &u.PasswordHash, &u.IsAdmin, &created)
	if err != nil {
		return nil, mapErr(err)
	}
	u.CreatedAt = unix(created)
	return &u, nil
}

// CreateUser inserts u, assigning its ID. The first user ever created becomes
// the instance admin.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	u.ID = NewID(10)
	u.CreatedAt = unix(now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		u.IsAdmin = true
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.DisplayName, u.Email, u.AvatarURL, u.PasswordHash, u.IsAdmin, u.CreatedAt.Unix())
	if err != nil {
		return mapErr(err)
	}
	return tx.Commit()
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) User(ctx context.Context, id string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

func (s *Store) UpdateUserProfile(ctx context.Context, id, displayName string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, displayName, id)
	return err
}

func (s *Store) SetPassword(ctx context.Context, id, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// Identity links a user to an account at an OAuth provider.
type Identity struct {
	Provider string `json:"provider"`
	Subject  string `json:"-"`
	UserID   string `json:"-"`
	Login    string `json:"login"`
}

func (s *Store) UserByIdentity(ctx context.Context, provider, subject string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = (SELECT user_id FROM identities WHERE provider = ? AND subject = ?)`,
		provider, subject))
}

func (s *Store) LinkIdentity(ctx context.Context, id Identity) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO identities (provider, subject, user_id, login) VALUES (?, ?, ?, ?)
		 ON CONFLICT(provider, subject) DO UPDATE SET login = excluded.login`,
		id.Provider, id.Subject, id.UserID, id.Login)
	return mapErr(err)
}

func (s *Store) Identities(ctx context.Context, userID string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, subject, user_id, login FROM identities WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Identity{}
	for rows.Next() {
		var i Identity
		if err := rows.Scan(&i.Provider, &i.Subject, &i.UserID, &i.Login); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Sessions are looked up by a hash so that a leaked database does not leak
// usable cookies.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession returns a new opaque session token for the user.
func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	token := NewID(32)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, now(), time.Now().Add(ttl).Unix())
	return token, err
}

func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = (SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?)`,
		hashToken(token), now()))
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now())
	return err
}

type SSHKey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"-"`
	Name        string     `json:"name"`
	PublicKey   string     `json:"publicKey"`
	Fingerprint string     `json:"fingerprint"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
}

func (s *Store) AddSSHKey(ctx context.Context, k *SSHKey) error {
	k.ID = NewID(8)
	k.CreatedAt = unix(now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ssh_keys (id, user_id, name, public_key, fingerprint, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		k.ID, k.UserID, k.Name, k.PublicKey, k.Fingerprint, k.CreatedAt.Unix())
	return mapErr(err)
}

func (s *Store) SSHKeys(ctx context.Context, userID string) ([]SSHKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, public_key, fingerprint, created_at, last_used_at FROM ssh_keys WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSHKey{}
	for rows.Next() {
		var k SSHKey
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &k.Fingerprint, &created, &used); err != nil {
			return nil, err
		}
		k.CreatedAt, k.LastUsedAt = unix(created), nullTime(used)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSSHKey(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// UserBySSHKey finds the owner of a public key by its SHA256 fingerprint and
// records the use.
func (s *Store) UserBySSHKey(ctx context.Context, fingerprint string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = (SELECT user_id FROM ssh_keys WHERE fingerprint = ?)`, fingerprint))
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE ssh_keys SET last_used_at = ? WHERE fingerprint = ?`, now(), fingerprint)
	return u, nil
}

// APIToken lets scripts act as a user without a browser session.
type APIToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"-"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// APITokenPrefix makes tokens recognisable in logs and secret scanners.
const APITokenPrefix = "avsys_"

// CreateAPIToken stores a new token and returns its secret, which cannot be
// recovered later.
func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken) (secret string, err error) {
	t.ID = NewID(8)
	t.CreatedAt = unix(now())
	secret = APITokenPrefix + NewID(30)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, hashToken(secret), t.CreatedAt.Unix())
	return secret, err
}

func (s *Store) APITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, created_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt, t.LastUsedAt = unix(created), nullTime(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// UserByAPIToken finds the owner of a token secret and records the use.
func (s *Store) UserByAPIToken(ctx context.Context, secret string) (*User, error) {
	hash := hashToken(secret)
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = (SELECT user_id FROM api_tokens WHERE token_hash = ?)`, hash))
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`, now(), hash)
	return u, nil
}
