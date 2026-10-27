package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A template is a server kept to make others from: its files and settings
// are snapshots in the organization's repository, one for each version, and
// what it is called is kept here. One made from settings alone has no
// snapshots: its settings are kept here too.

// ErrTemplateNameTaken is returned for a template named as another of the
// same organization is, whatever their case.
var ErrTemplateNameTaken = errors.New("another template has that name")

// Template is what is kept of a template here.
type Template struct {
	ID          string
	OrgID       string
	Name        string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Current is the version servers are made from, by its snapshot's
	// lineage; "" for the newest.
	Current string
	// Settings are, for a template of settings alone, what a server made
	// from it is, as JSON; "" for one saved from a server.
	Settings string
	// Changes are, for one saved from a server, what servers made from it
	// have other than its version does, as a copy's request asks for it:
	// JSON, "" for none.
	Changes string
}

const templateColumns = `id, org_id, name, description, created_by, created_at, updated_at, current, settings, changes`

func scanTemplate(row interface{ Scan(...any) error }) (*Template, error) {
	var t Template
	var created, updated int64
	if err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Description, &t.CreatedBy, &created, &updated, &t.Current, &t.Settings, &t.Changes); err != nil {
		return nil, err
	}
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &t, nil
}

// CreateTemplate keeps a new template, giving it an ID and its times.
func (s *Store) CreateTemplate(ctx context.Context, t *Template) error {
	t.ID = NewID(8)
	t.CreatedAt = time.Now().Truncate(time.Second)
	t.UpdatedAt = t.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO templates (`+templateColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.OrgID, t.Name, t.Description, t.CreatedBy, t.CreatedAt.Unix(), t.UpdatedAt.Unix(), t.Current, t.Settings, t.Changes)
	return nameTaken(err)
}

// Templates lists an organization's templates by name.
func (s *Store) Templates(ctx context.Context, orgID string) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateColumns+` FROM templates WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Template returns one of an organization's templates, or ErrNotFound.
func (s *Store) Template(ctx context.Context, orgID, id string) (*Template, error) {
	t, err := scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM templates WHERE org_id = ? AND id = ?`, orgID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// UpdateTemplate saves a template's name, description, current version,
// settings and changes, and says it was changed now.
func (s *Store) UpdateTemplate(ctx context.Context, t *Template) error {
	t.UpdatedAt = time.Now().Truncate(time.Second)
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET name = ?, description = ?, current = ?, settings = ?, changes = ?, updated_at = ? WHERE org_id = ? AND id = ?`,
		t.Name, t.Description, t.Current, t.Settings, t.Changes, t.UpdatedAt.Unix(), t.OrgID, t.ID)
	if err != nil {
		return nameTaken(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTemplateCurrent says which version servers are made from, with the
// changes made to it, leaving the rest of the template as it is now. A
// template of settings alone given a version is made from its versions from
// then on: its settings are dropped.
func (s *Store) SetTemplateCurrent(ctx context.Context, orgID, id, current, changes string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET current = ?, settings = '', changes = ?, updated_at = ? WHERE org_id = ? AND id = ?`, current, changes, time.Now().Unix(), orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTemplate forgets a template. Its snapshots are the caller's to
// delete.
func (s *Store) DeleteTemplate(ctx context.Context, orgID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE org_id = ? AND id = ?`, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func nameTaken(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: templates.org_id, templates.name") {
		return ErrTemplateNameTaken
	}
	return err
}
