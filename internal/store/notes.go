package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Notes on servers and names of snapshots are the panel's alone: what
// admins keep to tell things apart. Nothing in them reaches a server or
// the game.

// ServerNotes returns what is noted of an organization's server, "" for
// nothing.
func (s *Store) ServerNotes(ctx context.Context, orgID, server string) (string, error) {
	var notes string
	err := s.db.QueryRowContext(ctx, `SELECT notes FROM server_notes WHERE org_id = ? AND server = ?`, orgID, server).Scan(&notes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return notes, err
}

// ServerNotesIn is ServerNotes of a server by its organization's slug.
func (s *Store) ServerNotesIn(ctx context.Context, orgSlug, server string) (string, error) {
	var notes string
	err := s.db.QueryRowContext(ctx, `SELECT n.notes FROM server_notes n JOIN orgs o ON o.id = n.org_id WHERE o.slug = ? AND n.server = ?`, orgSlug, server).Scan(&notes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return notes, err
}

// AllServerNotes returns what is noted of each of an organization's
// servers that has notes, by name.
func (s *Store) AllServerNotes(ctx context.Context, orgID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server, notes FROM server_notes WHERE org_id = ?`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var server, notes string
		if err := rows.Scan(&server, &notes); err != nil {
			return nil, err
		}
		out[server] = notes
	}
	return out, rows.Err()
}

// SetServerNotes keeps what is noted of a server in place of what was;
// "" keeps nothing.
func (s *Store) SetServerNotes(ctx context.Context, orgID, server, notes string) error {
	if notes == "" {
		return s.DeleteServerNotes(ctx, orgID, server)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_notes (org_id, server, notes, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(org_id, server) DO UPDATE SET notes = excluded.notes, updated_at = excluded.updated_at`,
		orgID, server, notes, now())
	return err
}

// DeleteServerNotes forgets what is noted of a server, if anything is.
func (s *Store) DeleteServerNotes(ctx context.Context, orgID, server string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM server_notes WHERE org_id = ? AND server = ?`, orgID, server)
	return err
}

// SnapshotName is the name and colour a snapshot was given, and who gave
// them when.
type SnapshotName struct {
	Name  string
	Color string
	By    string
	At    time.Time
}

// SnapshotNames returns the names of an organization's snapshots, by
// snapshot ID.
func (s *Store) SnapshotNames(ctx context.Context, orgID string) (map[string]SnapshotName, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, color, by_user, updated_at FROM snapshot_names WHERE org_id = ?`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SnapshotName{}
	for rows.Next() {
		var id string
		var n SnapshotName
		var at int64
		if err := rows.Scan(&id, &n.Name, &n.Color, &n.By, &at); err != nil {
			return nil, err
		}
		n.At = unix(at)
		out[id] = n
	}
	return out, rows.Err()
}

// SetSnapshotName names a snapshot, by its full ID, in place of any name it
// had. A name and colour both "" take its name away.
func (s *Store) SetSnapshotName(ctx context.Context, orgID, id string, n SnapshotName) error {
	if n.Name == "" && n.Color == "" {
		return s.DeleteSnapshotNames(ctx, orgID, id)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snapshot_names (org_id, id, name, color, by_user, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(org_id, id) DO UPDATE SET name = excluded.name, color = excluded.color, by_user = excluded.by_user, updated_at = excluded.updated_at`,
		orgID, id, n.Name, n.Color, n.By, now())
	return err
}

// DeleteSnapshotNames forgets the names of snapshots that are gone.
func (s *Store) DeleteSnapshotNames(ctx context.Context, orgID string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot_names WHERE org_id = ? AND id = ?`, orgID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ForgetSnapshotNamesBut forgets the names of an organization's snapshots
// that are not among those it has, of those named before since: one named
// after the list of what it has was read may be missing from it only
// because it is newer than the list.
func (s *Store) ForgetSnapshotNamesBut(ctx context.Context, orgID string, has map[string]bool, since time.Time) error {
	names, err := s.SnapshotNames(ctx, orgID)
	if err != nil {
		return err
	}
	var gone []string
	for id, n := range names {
		if !has[id] && n.At.Unix() < since.Unix() {
			gone = append(gone, id)
		}
	}
	return s.DeleteSnapshotNames(ctx, orgID, gone...)
}
