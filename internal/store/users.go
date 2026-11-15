package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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
	_, err := s.SignUp(ctx, u, Signup{})
	return err
}

var (
	// ErrNotFirst is what SignUp says when the account was to be the
	// panel's first and another was made before it.
	ErrNotFirst = errors.New("there is an account already")
	// ErrFirst is its answer when the account was to be any but the
	// first, and there is none yet.
	ErrFirst = errors.New("would be the first user")
	// ErrIdentityTaken is its answer when the provider account is linked
	// to a user already.
	ErrIdentityTaken = errors.New("identity already linked")
)

// Signup is what a new account is made on the strength of.
type Signup struct {
	// FirstOnly makes it only as the panel's very first account, who is
	// the administrator.
	FirstOnly bool
	// LaterOnly makes it only as any but the first.
	LaterOnly bool
	// Invite is accepted with it. If it cannot be, no account is made.
	Invite string
	// Identity is a provider account linked to it: both or neither, so
	// that no user is made for a provider account another user has. An
	// empty one links nothing.
	Identity Identity
}

// Which says which user CreateUserWithIdentity may make: any, only the
// first (who is the administrator), or any but the first.
type Which int

const (
	AnyUser Which = iota
	FirstUser
	LaterUser
)

// CreateUserWithIdentity inserts u, as CreateUser does, with a provider
// account linked to it (see Signup.Identity), which limits it to the first
// user or to any but the first.
func (s *Store) CreateUserWithIdentity(ctx context.Context, u *User, id Identity, which Which) error {
	_, err := s.SignUp(ctx, u, Signup{Identity: id, FirstOnly: which == FirstUser, LaterOnly: which == LaterUser})
	return err
}

// SignUp inserts u, as CreateUser does, together with what lets it be made:
// being the first account (or not), or an invite, which it accepts and
// whose organization it returns; and the provider account it signed up
// with, linked to it. The checks and the inserts are one transaction: two
// people setting up an empty panel at once, or taking the last use of an
// invite, cannot both get in.
func (s *Store) SignUp(ctx context.Context, u *User, how Signup) (*Org, error) {
	u.ID = NewID(10)
	u.CreatedAt = unix(now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, err
	}
	switch {
	case count > 0 && how.FirstOnly:
		return nil, ErrNotFirst
	case count == 0 && how.LaterOnly:
		return nil, ErrFirst
	}
	if count == 0 {
		u.IsAdmin = true
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.DisplayName, u.Email, u.AvatarURL, u.PasswordHash, u.IsAdmin, u.CreatedAt.Unix())
	if err != nil {
		return nil, mapErr(err)
	}
	if id := how.Identity; id.Provider != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO identities (provider, subject, user_id, login) VALUES (?, ?, ?, ?)`,
			id.Provider, id.Subject, u.ID, id.Login)
		if err = mapErr(err); errors.Is(err, ErrConflict) {
			return nil, ErrIdentityTaken
		} else if err != nil {
			return nil, err
		}
	}
	var org *Org
	if how.Invite != "" {
		if org, err = acceptInvite(ctx, tx, how.Invite, u.ID); err != nil {
			return nil, err
		}
	}
	return org, tx.Commit()
}

// Admins lists the instance administrators.
func (s *Store) Admins(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE is_admin ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
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

// LinkIdentity links a provider account to a user, or updates the login of
// a link that is there. It is ErrConflict when the provider account is
// linked to another user: a link never moves.
func (s *Store) LinkIdentity(ctx context.Context, id Identity) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO identities (provider, subject, user_id, login) VALUES (?, ?, ?, ?)
		 ON CONFLICT(provider, subject) DO UPDATE SET login = excluded.login WHERE user_id = excluded.user_id`,
		id.Provider, id.Subject, id.UserID, id.Login)
	if err != nil {
		return mapErr(err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrConflict
	}
	return nil
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

// CreateSession returns a new opaque session token for the user. See
// OpenSession for one that says where it is for.
func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	return s.OpenSession(ctx, userID, ttl, SessionInfo{})
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

// DeleteExpiredSessions clears out sessions that have expired, and reset
// links.
func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reset_links WHERE expires_at <= ?`, now()); err != nil {
		return err
	}
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
