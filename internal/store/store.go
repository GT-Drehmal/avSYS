// Package store persists everything that is about people rather than servers:
// users, their logins and keys, organizations, memberships and invites.
// Servers themselves live in Kubernetes as MinecraftServer resources.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	display_name  TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	avatar_url    TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL DEFAULT '',
	is_admin      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS minecraft_links (
	user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	uuid      TEXT NOT NULL,
	name      TEXT NOT NULL,
	linked_at INTEGER NOT NULL,
	main      INTEGER NOT NULL DEFAULT 0,
	-- When the player proved it is theirs, by logging in as it; 0 for not.
	verified_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, uuid)
);
CREATE TABLE IF NOT EXISTS identities (
	provider TEXT NOT NULL,
	subject  TEXT NOT NULL,
	user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	login    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (provider, subject)
);
CREATE INDEX IF NOT EXISTS identities_user ON identities(user_id);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ssh_keys (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	public_key   TEXT NOT NULL,
	fingerprint  TEXT NOT NULL UNIQUE,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER
);
CREATE TABLE IF NOT EXISTS api_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER
);
CREATE TABLE IF NOT EXISTS orgs (
	id         TEXT PRIMARY KEY,
	slug       TEXT NOT NULL UNIQUE,
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS org_domains (
	domain     TEXT PRIMARY KEY,
	org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS memberships (
	org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (org_id, user_id)
);
CREATE TABLE IF NOT EXISTS invites (
	token      TEXT PRIMARY KEY,
	org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
	role       TEXT NOT NULL,
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER,
	max_uses   INTEGER NOT NULL DEFAULT 0,
	uses       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS images (
	key        TEXT PRIMARY KEY,
	data       BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	user_id TEXT NOT NULL DEFAULT '',
	org_id  TEXT NOT NULL DEFAULT '',
	action  TEXT NOT NULL,
	target  TEXT NOT NULL DEFAULT '',
	detail  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS templates (
	id          TEXT PRIMARY KEY,
	org_id      TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
	name        TEXT NOT NULL COLLATE NOCASE,
	description TEXT NOT NULL DEFAULT '',
	created_by  TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	current     TEXT NOT NULL DEFAULT '',
	settings    TEXT NOT NULL DEFAULT '',
	changes     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS templates_name ON templates(org_id, name);
CREATE INDEX IF NOT EXISTS audit_org ON audit(org_id, id);
CREATE INDEX IF NOT EXISTS audit_target ON audit(org_id, target, id);
`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids lock contention
	// and is plenty for a panel's worth of traffic.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	if err := addVerifiedAt(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	if err := migrateMinecraftAccounts(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	if err := addTemplateSettings(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	if err := addTemplateChanges(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// addVerifiedAt gives a table of linked accounts from before accounts could
// be proven the column for it, with every account in it taken as proven.
func addVerifiedAt(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('minecraft_links') WHERE name = 'verified_at'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE minecraft_links ADD COLUMN verified_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE minecraft_links SET verified_at = linked_at`)
	return err
}

// addTemplateChanges gives a table of templates from before a template's
// settings could be changed from its version's the column for them.
func addTemplateChanges(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('templates') WHERE name = 'changes'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := db.Exec(`ALTER TABLE templates ADD COLUMN changes TEXT NOT NULL DEFAULT ''`)
	return err
}

// addTemplateSettings gives a table of templates from before templates could
// be made of settings alone the column for them.
func addTemplateSettings(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('templates') WHERE name = 'settings'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := db.Exec(`ALTER TABLE templates ADD COLUMN settings TEXT NOT NULL DEFAULT ''`)
	return err
}

// migrateMinecraftAccounts moves the one Minecraft account each user could
// link before into the table that holds several, as their main one, once.
func migrateMinecraftAccounts(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'minecraft_accounts'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Linked before an account could be proven, it is taken as proven:
	// nobody already let in is shut out by the change.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO minecraft_links (user_id, uuid, name, linked_at, main, verified_at) SELECT user_id, uuid, name, linked_at, 1, linked_at FROM minecraft_accounts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE minecraft_accounts`); err != nil {
		return err
	}
	return tx.Commit()
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID returns a random lowercase identifier of n bytes of entropy.
func NewID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return idEncoding.EncodeToString(b)
}

func now() int64 { return time.Now().Unix() }

func unix(v int64) time.Time { return time.Unix(v, 0).UTC() }

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := unix(v.Int64)
	return &t
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		return ErrConflict
	}
	return err
}

// Setting returns a stored value, or ErrNotFound.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, mapErr(err)
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

type AuditEntry struct {
	At     time.Time `json:"at"`
	UserID string    `json:"userId"`
	OrgID  string    `json:"-"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
}

// Activity is something done to a server, and who did it, if they are still
// known.
type Activity struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
	User   *User     `json:"user,omitempty"`
}

// ServerActivity is what was done to one of an organization's servers since
// it was made, newest first: the actions on it, of these kinds. One deleted
// before it with the same name is not it: nothing from before the last
// server of that name was deleted is.
func (s *Store) ServerActivity(ctx context.Context, orgID, server string, since time.Time, kinds []string, limit int) ([]Activity, error) {
	if len(kinds) == 0 {
		return []Activity{}, nil
	}
	like := make([]string, len(kinds))
	args := []any{orgID, server, since.Unix(), orgID, server}
	for i, k := range kinds {
		like[i] = "a.action LIKE ?"
		args = append(args, k+"%")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT a.at, a.action, a.detail, u.id, u.username, u.display_name, u.avatar_url
		FROM audit a LEFT JOIN users u ON u.id = a.user_id
		WHERE a.org_id = ? AND a.target = ? AND a.at >= ?
		AND a.id > COALESCE((SELECT MAX(id) FROM audit WHERE org_id = ? AND target = ? AND action = 'server.delete'), 0)
		AND (`+strings.Join(like, " OR ")+`)
		ORDER BY a.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var at int64
		var a Activity
		var id, username, display, avatar sql.NullString
		if err := rows.Scan(&at, &a.Action, &a.Detail, &id, &username, &display, &avatar); err != nil {
			return nil, err
		}
		a.At = unix(at)
		if id.Valid {
			a.User = &User{ID: id.String, Username: username.String, DisplayName: display.String, AvatarURL: avatar.String}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Audit records who did what. Failures are returned but callers generally
// only log them: an audit write must not undo the action it describes.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (at, user_id, org_id, action, target, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		now(), e.UserID, e.OrgID, e.Action, e.Target, e.Detail)
	return err
}
