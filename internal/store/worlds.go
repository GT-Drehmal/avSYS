package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A world in an organization's library is a map kept to start servers
// from: its files are an archive kept as a snapshot in the organization's
// repository, and what it is called is kept here.

// ErrWorldNameTaken is returned for a world named as another of the same
// organization is, whatever their case.
var ErrWorldNameTaken = errors.New("another world has that name")

// World is what is kept of a world here.
type World struct {
	ID          string
	OrgID       string
	Name        string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Root is the world's folder in its archive.
	Root string
	// Version is the game version it was last opened in, where known.
	Version string
	// File is the name of the archive it came in, for one uploaded.
	File string
	// Size is how large its archive is, and Unpacked how large the world
	// is out of it, where known.
	Size, Unpacked int64
	// Source says where it was saved from: "upload", "server:<name>" or
	// "snapshot:<server>".
	Source string
	// Failed says why its archive could not be made, for one that was not.
	Failed string
}

const worldColumns = `id, org_id, name, description, created_by, created_at, updated_at, root, version, file, size, unpacked, source, failed`

func scanWorld(row interface{ Scan(...any) error }) (*World, error) {
	var w World
	var created, updated int64
	if err := row.Scan(&w.ID, &w.OrgID, &w.Name, &w.Description, &w.CreatedBy, &created, &updated, &w.Root, &w.Version, &w.File, &w.Size, &w.Unpacked, &w.Source, &w.Failed); err != nil {
		return nil, err
	}
	w.CreatedAt, w.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &w, nil
}

// CreateWorld keeps a new world, giving it an ID and its times.
func (s *Store) CreateWorld(ctx context.Context, w *World) error {
	w.ID = NewID(8)
	w.CreatedAt = time.Now().Truncate(time.Second)
	w.UpdatedAt = w.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO worlds (`+worldColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.OrgID, w.Name, w.Description, w.CreatedBy, w.CreatedAt.Unix(), w.UpdatedAt.Unix(), w.Root, w.Version, w.File, w.Size, w.Unpacked, w.Source, w.Failed)
	return worldNameTaken(err)
}

// Worlds lists an organization's worlds by name.
func (s *Store) Worlds(ctx context.Context, orgID string) ([]World, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+worldColumns+` FROM worlds WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []World{}
	for rows.Next() {
		w, err := scanWorld(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// World returns one of an organization's worlds, or ErrNotFound.
func (s *Store) World(ctx context.Context, orgID, id string) (*World, error) {
	w, err := scanWorld(s.db.QueryRowContext(ctx, `SELECT `+worldColumns+` FROM worlds WHERE org_id = ? AND id = ?`, orgID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// UpdateWorld saves a world's name and description, and says it was
// changed now.
func (s *Store) UpdateWorld(ctx context.Context, w *World) error {
	w.UpdatedAt = time.Now().Truncate(time.Second)
	res, err := s.db.ExecContext(ctx, `UPDATE worlds SET name = ?, description = ?, updated_at = ? WHERE org_id = ? AND id = ?`,
		w.Name, w.Description, w.UpdatedAt.Unix(), w.OrgID, w.ID)
	if err != nil {
		return worldNameTaken(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorldArchive keeps what is known of a world's archive once it is
// saved, leaving the rest of it as it is now.
func (s *Store) SetWorldArchive(ctx context.Context, orgID, id, root, version string, size, unpacked int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worlds SET root = ?, version = ?, size = ?, unpacked = ?, failed = '', updated_at = ? WHERE org_id = ? AND id = ?`, root, version, size, unpacked, time.Now().Unix(), orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorldFailed says why a world's archive could not be made.
func (s *Store) SetWorldFailed(ctx context.Context, orgID, id, why string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worlds SET failed = ?, updated_at = ? WHERE org_id = ? AND id = ?`, why, time.Now().Unix(), orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWorld forgets a world, and its picture. Its snapshot is the
// caller's to delete.
func (s *Store) DeleteWorld(ctx context.Context, orgID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM worlds WHERE org_id = ? AND id = ?`, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return s.DeleteImage(ctx, WorldImage(id))
}

func worldNameTaken(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: worlds.org_id, worlds.name") {
		return ErrWorldNameTaken
	}
	return err
}
