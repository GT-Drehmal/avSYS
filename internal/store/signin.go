package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// addSessionDetails gives the sessions table what a list of someone's
// sessions shows: an id to end one by, the browser and address it was last
// used from, when, and how it was signed in. Sessions from before have an
// id made for them.
func addSessionDetails(db *sql.DB) error {
	for _, col := range []struct{ name, def string }{
		{"id", `TEXT NOT NULL DEFAULT ''`},
		{"user_agent", `TEXT NOT NULL DEFAULT ''`},
		{"address", `TEXT NOT NULL DEFAULT ''`},
		{"last_used_at", `INTEGER NOT NULL DEFAULT 0`},
		{"method", `TEXT NOT NULL DEFAULT ''`},
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name = ?`, col.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE sessions SET id = lower(hex(randomblob(10))) WHERE id = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE sessions SET last_used_at = created_at WHERE last_used_at = 0`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id)`)
	return err
}

// How a session was signed in.
const (
	// SignedInWithPassword is a password, and a code if the account has
	// two-factor on.
	SignedInWithPassword = "password"
	// SignedInWithProvider is an identity provider, and a code if the
	// account has two-factor on.
	SignedInWithProvider = "provider"
)

// SessionInfo is what is known of the browser a session is for.
type SessionInfo struct {
	UserAgent string
	Address   string
	// Method is SignedInWithPassword or SignedInWithProvider.
	Method string
}

// Session is one signed-in browser.
type Session struct {
	ID         string    `json:"id"`
	UserID     string    `json:"-"`
	UserAgent  string    `json:"userAgent"`
	Address    string    `json:"address"`
	Method     string    `json:"method"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	// Current is the session the request asking was made with.
	Current bool `json:"current"`
}

// sessionTouch is how stale a session's last use may get before using it
// writes it again, so that a page's many requests are not as many writes.
const sessionTouch = time.Minute

// OpenSession stores a new session for the user and returns its token, which
// is stored only as a hash.
func (s *Store) OpenSession(ctx context.Context, userID string, ttl time.Duration, info SessionInfo) (string, error) {
	token := NewID(32)
	at := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, id, user_id, created_at, expires_at, last_used_at, user_agent, address, method) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), NewID(10), userID, at, time.Now().Add(ttl).Unix(), at, info.UserAgent, info.Address, info.Method)
	return token, err
}

const sessionColumns = `id, user_id, user_agent, address, method, created_at, last_used_at, expires_at`

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var x Session
	var created, used, expires int64
	if err := row.Scan(&x.ID, &x.UserID, &x.UserAgent, &x.Address, &x.Method, &created, &used, &expires); err != nil {
		return nil, mapErr(err)
	}
	x.CreatedAt, x.LastUsedAt, x.ExpiresAt = unix(created), unix(used), unix(expires)
	return &x, nil
}

// SessionByToken finds the session of a token that has not expired, and its
// user, and notes that it was used, from where.
func (s *Store) SessionByToken(ctx context.Context, token string, info SessionInfo) (*User, *Session, error) {
	hash := hashToken(token)
	session, err := scanSession(s.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE token_hash = ? AND expires_at > ?`, hash, now()))
	if err != nil {
		return nil, nil, err
	}
	u, err := s.User(ctx, session.UserID)
	if err != nil {
		return nil, nil, err
	}
	if time.Since(session.LastUsedAt) > sessionTouch || session.Address != info.Address || session.UserAgent != info.UserAgent {
		session.LastUsedAt = unix(now())
		session.Address, session.UserAgent = info.Address, info.UserAgent
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_used_at = ?, address = ?, user_agent = ? WHERE token_hash = ?`,
			session.LastUsedAt.Unix(), info.Address, info.UserAgent, hash)
	}
	session.Current = true
	return u, session, nil
}

// Sessions lists a user's sessions that have not expired, the most recently
// used first. The one of the token current, if any, is marked so.
func (s *Store) Sessions(ctx context.Context, userID, current string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionColumns+`, token_hash = ? FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_used_at DESC, created_at DESC`,
		hashToken(current), userID, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		var created, used, expires int64
		if err := rows.Scan(&x.ID, &x.UserID, &x.UserAgent, &x.Address, &x.Method, &created, &used, &expires, &x.Current); err != nil {
			return nil, err
		}
		x.CreatedAt, x.LastUsedAt, x.ExpiresAt = unix(created), unix(used), unix(expires)
		if current == "" {
			x.Current = false
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// EndSession ends one of a user's sessions, by its id. It is ErrNotFound if
// the user has no such session.
func (s *Store) EndSession(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EndOtherSessions ends all of a user's sessions but the one of the token
// keep; with keep empty, all of them.
func (s *Store) EndOtherSessions(ctx context.Context, userID, keep string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, hashToken(keep))
	return err
}

// TwoFactor is a user's second factor: the secret of their codes, if it is
// on, and how many unused recovery codes they have.
type TwoFactor struct {
	Secret string
	// LastStep is the time step of the last code used. A code of it or of
	// one before is not taken.
	LastStep      int64
	RecoveryCodes int
}

// TwoFactor returns a user's second factor. Secret is empty for a user
// without one.
func (s *Store) TwoFactor(ctx context.Context, userID string) (*TwoFactor, error) {
	var t TwoFactor
	err := s.db.QueryRowContext(ctx, `SELECT secret, last_step FROM two_factor WHERE user_id = ?`, userID).Scan(&t.Secret, &t.LastStep)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ?`, userID).Scan(&t.RecoveryCodes); err != nil {
		return nil, err
	}
	return &t, nil
}

// HasTwoFactor reports whether a user has two-factor on.
func (s *Store) HasTwoFactor(ctx context.Context, userID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM two_factor WHERE user_id = ? AND secret != ''`, userID).Scan(&n)
	return n > 0, err
}

// SetPendingTwoFactor keeps a secret being set up, until it is confirmed with
// a code. It replaces one set up before and not confirmed.
func (s *Store) SetPendingTwoFactor(ctx context.Context, userID, secret string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO two_factor (user_id, pending) VALUES (?, ?) ON CONFLICT(user_id) DO UPDATE SET pending = excluded.pending`,
		userID, secret)
	return err
}

// PendingTwoFactor is the secret being set up, or ErrNotFound.
func (s *Store) PendingTwoFactor(ctx context.Context, userID string) (string, error) {
	var pending string
	err := s.db.QueryRowContext(ctx, `SELECT pending FROM two_factor WHERE user_id = ? AND pending != ''`, userID).Scan(&pending)
	return pending, mapErr(err)
}

// EnableTwoFactor turns on the secret being set up, if it is still pending,
// with step as the one its confirming code was of, and the recovery codes
// given in place of any there were. It is ErrNotFound if that secret is no
// longer the one pending.
func (s *Store) EnableTwoFactor(ctx context.Context, userID, secret string, step int64, recovery []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE two_factor SET secret = pending, pending = '', last_step = ?, enabled_at = ? WHERE user_id = ? AND pending = ? AND pending != ''`,
		step, now(), userID, secret)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	if err := setRecoveryCodes(ctx, tx, userID, recovery); err != nil {
		return err
	}
	return tx.Commit()
}

func setRecoveryCodes(ctx context.Context, tx *sql.Tx, userID string, codes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, code := range codes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, hashToken(code)); err != nil {
			return err
		}
	}
	return nil
}

// SetRecoveryCodes replaces a user's recovery codes, which are stored only
// as hashes.
func (s *Store) SetRecoveryCodes(ctx context.Context, userID string, codes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setRecoveryCodes(ctx, tx, userID, codes); err != nil {
		return err
	}
	return tx.Commit()
}

// UseTwoFactorStep takes a code of the time step given as used, if no code of
// it or of a later one has been: it reports whether it was taken. Of two
// sign-ins with one code, only one gets it.
func (s *Store) UseTwoFactorStep(ctx context.Context, userID string, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE two_factor SET last_step = ? WHERE user_id = ? AND secret != '' AND last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// UseRecoveryCode spends one of a user's recovery codes: it reports whether
// it was one of theirs, unused.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, code string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?`, userID, hashToken(code))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DisableTwoFactor turns a user's second factor off, and takes away their
// recovery codes.
func (s *Store) DisableTwoFactor(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := disableTwoFactor(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func disableTwoFactor(ctx context.Context, tx *sql.Tx, userID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM two_factor WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return err
}

// ResetLink is a link an administrator made for someone to set a new
// password with.
type ResetLink struct {
	UserID    string
	CreatedBy string
	ExpiresAt time.Time
	// TwoFactorOff turns the user's second factor off when the link is used.
	TwoFactorOff bool
}

// CreateResetLink makes a reset link for a user, in place of any they had,
// and returns its token, which is stored only as a hash.
func (s *Store) CreateResetLink(ctx context.Context, l *ResetLink, ttl time.Duration) (string, error) {
	token := NewID(32)
	l.ExpiresAt = unix(time.Now().Add(ttl).Unix())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM reset_links WHERE user_id = ?`, l.UserID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO reset_links (token_hash, user_id, created_by, created_at, expires_at, two_factor_off) VALUES (?, ?, ?, ?, ?, ?)`,
		hashToken(token), l.UserID, l.CreatedBy, now(), l.ExpiresAt.Unix(), l.TwoFactorOff); err != nil {
		return "", mapErr(err)
	}
	return token, tx.Commit()
}

// ResetLinkUser is the user a reset link that has not expired is for, or
// ErrNotFound.
func (s *Store) ResetLinkUser(ctx context.Context, token string) (*User, error) {
	u, _, err := s.ResetLinkOf(ctx, token)
	return u, err
}

// ResetLinkOf is a reset link that has not expired, and the user it is for,
// or ErrNotFound.
func (s *Store) ResetLinkOf(ctx context.Context, token string) (*User, *ResetLink, error) {
	var l ResetLink
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, created_by, expires_at, two_factor_off FROM reset_links WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), now()).Scan(&l.UserID, &l.CreatedBy, &expires, &l.TwoFactorOff)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	l.ExpiresAt = unix(expires)
	u, err := s.User(ctx, l.UserID)
	if err != nil {
		return nil, nil, err
	}
	return u, &l, nil
}

// UseResetLink sets a new password with a reset link that has not expired,
// once: the link is gone after, and so are all the user's sessions, and their
// second factor if the link said so. With passwordHash "", the password is
// left as it is: for a link that only turns two-factor off. It is ErrNotFound
// for a link that is not there, or no longer: of two uses at once, one is.
func (s *Store) UseResetLink(ctx context.Context, token, passwordHash string) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var l ResetLink
	err = tx.QueryRowContext(ctx, `DELETE FROM reset_links WHERE token_hash = ? AND expires_at > ? RETURNING user_id, two_factor_off`,
		hashToken(token), now()).Scan(&l.UserID, &l.TwoFactorOff)
	if err != nil {
		return nil, mapErr(err)
	}
	if passwordHash != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, l.UserID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, l.UserID); err != nil {
		return nil, err
	}
	if l.TwoFactorOff {
		if err := disableTwoFactor(ctx, tx, l.UserID); err != nil {
			return nil, err
		}
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, l.UserID))
	if err != nil {
		return nil, err
	}
	return u, tx.Commit()
}

// Who has to use two-factor: the setting SettingTwoFactorLevel, by the
// highest role people have in any organization. The panel's administrator
// has to whenever it is not TwoFactorNobody.
const (
	SettingTwoFactorLevel = "signin.two-factor"
	TwoFactorNobody       = "nobody"
	TwoFactorAdmins       = "admins"
	TwoFactorOperators    = "operators"
	TwoFactorEveryone     = "everyone"
)

// settingAdminTwoFactor was, as "1", the one switch there was before:
// two-factor for the panel's administrator.
const settingAdminTwoFactor = "signin.admin-two-factor"

// migrateTwoFactorLevel sets who has to use two-factor, once. A panel just
// installed, with nobody in it yet, has admins and owners. One from before
// keeps what it had: admins and owners (and its administrator, as before)
// where the old switch was on, and nobody where it was off.
func migrateTwoFactorLevel(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO settings (key, value) SELECT ?,
		CASE WHEN EXISTS (SELECT 1 FROM settings WHERE key = ? AND value = '1') THEN ?
		     WHEN EXISTS (SELECT 1 FROM users) THEN ?
		     ELSE ? END
		WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = ?)`,
		SettingTwoFactorLevel, settingAdminTwoFactor, TwoFactorAdmins, TwoFactorNobody, TwoFactorAdmins, SettingTwoFactorLevel); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, settingAdminTwoFactor); err != nil {
		return err
	}
	return tx.Commit()
}

// HighestRole is the highest role a user has in any organization, or ""
// for someone in none.
func (s *Store) HighestRole(ctx context.Context, userID string) (Role, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role FROM memberships WHERE user_id = ?`, userID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var highest Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r); err != nil {
			return "", err
		}
		if roleRank[r] > roleRank[highest] {
			highest = r
		}
	}
	return highest, rows.Err()
}
