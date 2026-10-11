package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Role is a user's standing within one organization.
type Role string

const (
	// RoleViewer can see servers, their status and console output.
	RoleViewer Role = "viewer"
	// RoleOperator can also start and stop servers, run commands, and open
	// a shell or the file browser.
	RoleOperator Role = "operator"
	// RoleAdmin can also create, configure and delete servers, and manage
	// members and invites.
	RoleAdmin Role = "admin"
	// RoleOwner can also change other owners and delete the organization.
	RoleOwner Role = "owner"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3, RoleOwner: 4}

func (r Role) Valid() bool { return roleRank[r] > 0 }

// AtLeast reports whether r grants everything min does.
func (r Role) AtLeast(min Role) bool { return roleRank[r] >= roleRank[min] }

var ErrInviteUnusable = errors.New("invite is expired or used up")

type Org struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateOrg creates an organization owned by ownerID.
func (s *Store) CreateOrg(ctx context.Context, o *Org, ownerID string) error {
	o.ID = NewID(10)
	o.CreatedAt = unix(now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO orgs (id, slug, name, created_at) VALUES (?, ?, ?, ?)`,
		o.ID, o.Slug, o.Name, o.CreatedAt.Unix()); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		o.ID, ownerID, RoleOwner, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func scanOrg(row interface{ Scan(...any) error }) (*Org, error) {
	var o Org
	var created int64
	if err := row.Scan(&o.ID, &o.Slug, &o.Name, &created); err != nil {
		return nil, mapErr(err)
	}
	o.CreatedAt = unix(created)
	return &o, nil
}

func (s *Store) OrgBySlug(ctx context.Context, slug string) (*Org, error) {
	return scanOrg(s.db.QueryRowContext(ctx, `SELECT id, slug, name, created_at FROM orgs WHERE slug = ?`, slug))
}

func (s *Store) OrgByID(ctx context.Context, id string) (*Org, error) {
	return scanOrg(s.db.QueryRowContext(ctx, `SELECT id, slug, name, created_at FROM orgs WHERE id = ?`, id))
}

func (s *Store) RenameOrg(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE orgs SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *Store) DeleteOrg(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM orgs WHERE id = ?`, id)
	return err
}

// OrgMembership is an organization together with the caller's role in it.
type OrgMembership struct {
	Org
	Role Role `json:"role"`
}

// OrgsForUser lists the organizations a user belongs to. Instance admins see
// every organization, as owner.
func (s *Store) OrgsForUser(ctx context.Context, u *User) ([]OrgMembership, error) {
	q := `SELECT o.id, o.slug, o.name, o.created_at, m.role FROM orgs o
	      JOIN memberships m ON m.org_id = o.id AND m.user_id = ? ORDER BY o.name`
	if u.IsAdmin {
		q = `SELECT o.id, o.slug, o.name, o.created_at, COALESCE(m.role, 'owner') FROM orgs o
		     LEFT JOIN memberships m ON m.org_id = o.id AND m.user_id = ? ORDER BY o.name`
	}
	rows, err := s.db.QueryContext(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgMembership{}
	for rows.Next() {
		var m OrgMembership
		var created int64
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &created, &m.Role); err != nil {
			return nil, err
		}
		m.CreatedAt = unix(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// RoleIn returns the user's role in an organization, or ErrNotFound if they
// are not a member.
func (s *Store) RoleIn(ctx context.Context, orgID, userID string) (Role, error) {
	var r Role
	err := s.db.QueryRowContext(ctx, `SELECT role FROM memberships WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&r)
	return r, mapErr(err)
}

type Member struct {
	UserID      string    `json:"userId"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	AvatarURL   string    `json:"avatarUrl"`
	Role        Role      `json:"role"`
	JoinedAt    time.Time `json:"joinedAt"`
}

func (s *Store) Members(ctx context.Context, orgID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.username, u.display_name, u.avatar_url, m.role, m.created_at
		 FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = ? ORDER BY m.created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var joined int64
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.AvatarURL, &m.Role, &joined); err != nil {
			return nil, err
		}
		m.JoinedAt = unix(joined)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SetRole(ctx context.Context, orgID, userID string, role Role) error {
	res, err := s.db.ExecContext(ctx, `UPDATE memberships SET role = ? WHERE org_id = ? AND user_id = ?`, role, orgID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveMember(ctx context.Context, orgID, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memberships WHERE org_id = ? AND user_id = ?`, orgID, userID)
	return err
}

func (s *Store) CountOwners(ctx context.Context, orgID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE org_id = ? AND role = ?`, orgID, RoleOwner).Scan(&n)
	return n, err
}

type Invite struct {
	Token     string     `json:"token"`
	OrgID     string     `json:"-"`
	Role      Role       `json:"role"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt"`
	// MaxUses of zero means unlimited.
	MaxUses int `json:"maxUses"`
	Uses    int `json:"uses"`
}

// Usable reports whether the invite can still be accepted.
func (i *Invite) Usable() bool {
	if i.ExpiresAt != nil && time.Now().After(*i.ExpiresAt) {
		return false
	}
	return i.MaxUses == 0 || i.Uses < i.MaxUses
}

func (s *Store) CreateInvite(ctx context.Context, i *Invite) error {
	i.Token = NewID(20)
	i.CreatedAt = unix(now())
	var expires sql.NullInt64
	if i.ExpiresAt != nil {
		expires = sql.NullInt64{Int64: i.ExpiresAt.Unix(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invites (token, org_id, role, created_by, created_at, expires_at, max_uses) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		i.Token, i.OrgID, i.Role, i.CreatedBy, i.CreatedAt.Unix(), expires, i.MaxUses)
	return err
}

const inviteColumns = `token, org_id, role, created_by, created_at, expires_at, max_uses, uses`

func scanInvite(row interface{ Scan(...any) error }) (*Invite, error) {
	var i Invite
	var created int64
	var expires sql.NullInt64
	if err := row.Scan(&i.Token, &i.OrgID, &i.Role, &i.CreatedBy, &created, &expires, &i.MaxUses, &i.Uses); err != nil {
		return nil, mapErr(err)
	}
	i.CreatedAt, i.ExpiresAt = unix(created), nullTime(expires)
	return &i, nil
}

func (s *Store) Invite(ctx context.Context, token string) (*Invite, error) {
	return scanInvite(s.db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE token = ?`, token))
}

func (s *Store) Invites(ctx context.Context, orgID string) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE org_id = ? ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInvite(ctx context.Context, orgID, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE token = ? AND org_id = ?`, token, orgID)
	return err
}

// AcceptInvite adds the user to the invite's organization. Accepting an
// invite never lowers a role the user already holds, and only consumes a use
// when it actually changes something.
func (s *Store) AcceptInvite(ctx context.Context, token, userID string) (*Org, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	inv, err := scanInvite(tx.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites WHERE token = ?`, token))
	if err != nil {
		return nil, err
	}
	if !inv.Usable() {
		return nil, ErrInviteUnusable
	}
	org, err := scanOrg(tx.QueryRowContext(ctx, `SELECT id, slug, name, created_at FROM orgs WHERE id = ?`, inv.OrgID))
	if err != nil {
		return nil, err
	}

	var current Role
	err = tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE org_id = ? AND user_id = ?`, inv.OrgID, userID).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO memberships (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			inv.OrgID, userID, inv.Role, now())
	case err != nil:
		return nil, err
	case current.AtLeast(inv.Role):
		return org, tx.Commit()
	default:
		_, err = tx.ExecContext(ctx, `UPDATE memberships SET role = ? WHERE org_id = ? AND user_id = ?`, inv.Role, inv.OrgID, userID)
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE token = ?`, token); err != nil {
		return nil, err
	}
	return org, tx.Commit()
}
