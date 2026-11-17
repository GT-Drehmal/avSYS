package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// The snapshots an organization's templates and library are made of (each
// version of a template, a template's own world, each world in the library)
// are noted here as they are made and removed, with what the pages show of
// them, so that listing templates and worlds needs no list of snapshots from
// the repository. The repository is still what they are: an organization's
// are read from it once, the first time they are asked for, and again each
// time the panel starts.

// TemplateSnapshot is what is noted of one of those snapshots.
type TemplateSnapshot struct {
	ID string
	// Host is what it was taken under: which template or world it is of.
	Host                string
	Time                time.Time
	By, Version, Loader string
	Size, Added, Files  int64
	Lineage             string
	// Form is how it keeps what it keeps: "folders" for a world kept as its
	// own files, "" otherwise.
	Form string
}

const templateSnapshotColumns = `id, host, time, by_user, version, loader, size, added, files, lineage, form`

// TemplateSnapshots are an organization's, newest first.
func (s *Store) TemplateSnapshots(ctx context.Context, orgID string) ([]TemplateSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateSnapshotColumns+` FROM template_snapshots WHERE org_id = ?`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TemplateSnapshot{}
	for rows.Next() {
		var t TemplateSnapshot
		var at string
		if err := rows.Scan(&t.ID, &t.Host, &at, &t.By, &t.Version, &t.Loader, &t.Size, &t.Added, &t.Files, &t.Lineage, &t.Form); err != nil {
			return nil, err
		}
		// Kept as the repository says it, zone and all.
		if t.Time, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// As the repository lists them.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// NoteTemplateSnapshot notes one of an organization's, once it is made.
func (s *Store) NoteTemplateSnapshot(ctx context.Context, orgID string, t TemplateSnapshot) error {
	return noteTemplateSnapshot(ctx, s.db, orgID, t)
}

func noteTemplateSnapshot(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, orgID string, t TemplateSnapshot) error {
	_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO template_snapshots (org_id, `+templateSnapshotColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, t.ID, t.Host, t.Time.Format(time.RFC3339Nano), t.By, t.Version, t.Loader, t.Size, t.Added, t.Files, t.Lineage, t.Form)
	return err
}

// ForgetTemplateSnapshots forgets those of an organization's that were
// removed, by ID.
func (s *Store) ForgetTemplateSnapshots(ctx context.Context, orgID string, ids ...string) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM template_snapshots WHERE org_id = ? AND id = ?`, orgID, id); err != nil {
			return err
		}
	}
	return nil
}

// ForgetTemplateSnapshotsOf forgets every one of an organization's taken
// under these hosts.
func (s *Store) ForgetTemplateSnapshotsOf(ctx context.Context, orgID string, hosts ...string) error {
	for _, host := range hosts {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM template_snapshots WHERE org_id = ? AND host = ?`, orgID, host); err != nil {
			return err
		}
	}
	return nil
}

// TemplateSnapshotsRead says whether an organization's have been read from
// its repository: until they are, none are noted here.
func (s *Store) TemplateSnapshotsRead(ctx context.Context, orgID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM template_snapshots_read WHERE org_id = ?`, orgID).Scan(&n)
	return n > 0, err
}

// SetTemplateSnapshots notes an organization's as its repository lists them
// now, in place of what was noted, and says how many were noted that were
// not and forgotten that were.
func (s *Store) SetTemplateSnapshots(ctx context.Context, orgID string, all []TemplateSnapshot) (added, removed int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM template_snapshots WHERE org_id = ?`, orgID)
	if err != nil {
		return 0, 0, err
	}
	was := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		was[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM template_snapshots WHERE org_id = ?`, orgID); err != nil {
		return 0, 0, err
	}
	is := map[string]bool{}
	for _, t := range all {
		if err := noteTemplateSnapshot(ctx, tx, orgID, t); err != nil {
			return 0, 0, err
		}
		if !is[t.ID] && !was[t.ID] {
			added++
		}
		is[t.ID] = true
	}
	for id := range was {
		if !is[id] {
			removed++
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO template_snapshots_read (org_id, at) VALUES (?, ?)`, orgID, now()); err != nil {
		return 0, 0, err
	}
	return added, removed, tx.Commit()
}
