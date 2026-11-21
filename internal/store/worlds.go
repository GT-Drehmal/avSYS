package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A world in an organization's library is a map kept to start servers
// from: its files are kept as a snapshot in the organization's repository,
// and what it is called is kept here.

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
	// Root is the world's folder: in its archive, for one kept as an
	// archive, and under /data in its snapshot, for one kept as its own
	// files.
	Root string
	// Version is the game version it was last opened in, where known.
	Version string
	// File is the name of the archive it came in, for one uploaded.
	File string
	// Size is how large what is kept of it is, and Unpacked how large the
	// world is, where known: the two are the same for one kept as its own
	// files.
	Size, Unpacked int64
	// Source says where it was saved from: "upload", "server:<name>" or
	// "snapshot:<server>".
	Source string
	// Failed says why its archive could not be made, for one that was not.
	Failed string
	// Pack is the resource pack it names, as JSON, or "" for none.
	Pack string
	// UnpackFailed says why one kept as an archive, as the library kept
	// worlds before, could not be kept as its own files instead. It is
	// still used as it is.
	UnpackFailed string
	// Datapacks are those in its datapacks folder, as JSON, or "" while
	// they have not been read.
	Datapacks string
}

const worldColumns = `id, org_id, name, description, created_by, created_at, updated_at, root, version, file, size, unpacked, source, failed, pack, unpack_failed, datapacks`

func scanWorld(row interface{ Scan(...any) error }) (*World, error) {
	var w World
	var created, updated int64
	if err := row.Scan(&w.ID, &w.OrgID, &w.Name, &w.Description, &w.CreatedBy, &created, &updated, &w.Root, &w.Version, &w.File, &w.Size, &w.Unpacked, &w.Source, &w.Failed, &w.Pack, &w.UnpackFailed, &w.Datapacks); err != nil {
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO worlds (`+worldColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.OrgID, w.Name, w.Description, w.CreatedBy, w.CreatedAt.Unix(), w.UpdatedAt.Unix(), w.Root, w.Version, w.File, w.Size, w.Unpacked, w.Source, w.Failed, w.Pack, w.UnpackFailed, w.Datapacks)
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

// SetWorldUnpacked keeps what is known of a world once it is kept as its own
// files in place of an archive: its folder, how large it is, and that it
// did not fail to be. It is not a change to the world, and does not say it
// was changed.
func (s *Store) SetWorldUnpacked(ctx context.Context, orgID, id, root string, size int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worlds SET root = ?, size = ?, unpacked = ?, unpack_failed = '' WHERE org_id = ? AND id = ?`, root, size, size, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorldUnpackFailed says why a world kept as an archive could not be
// kept as its own files instead.
func (s *Store) SetWorldUnpackFailed(ctx context.Context, orgID, id, why string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worlds SET unpack_failed = ? WHERE org_id = ? AND id = ?`, why, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorldPack keeps the resource pack a world names, as JSON, or "" for
// none, and says it was changed now.
func (s *Store) SetWorldPack(ctx context.Context, orgID, id, pack string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE worlds SET pack = ?, updated_at = ? WHERE org_id = ? AND id = ?`, pack, time.Now().Unix(), orgID, id)
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

// SetWorldDatapacks keeps what was read of a world's datapacks, as JSON,
// and their pictures, by each one's name, in place of any it had. It is not
// a change to the world, and does not say it was changed.
func (s *Store) SetWorldDatapacks(ctx context.Context, orgID, id, list string, pictures map[string][]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE worlds SET datapacks = ? WHERE org_id = ? AND id = ?`, list, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := deleteDatapackImages(ctx, tx, id); err != nil {
		return err
	}
	for name, data := range pictures {
		if _, err := tx.ExecContext(ctx, `INSERT INTO images (key, data, updated_at) VALUES (?, ?, ?)`, WorldDatapackImage(id, name), data, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// deleteDatapackImages removes the pictures of a world's datapacks.
func deleteDatapackImages(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id string) error {
	prefix := WorldDatapackImage(id, "")
	_, err := tx.ExecContext(ctx, `DELETE FROM images WHERE substr(key, 1, ?) = ?`, len(prefix), prefix)
	return err
}

// DeleteWorld forgets a world, its picture and those of its datapacks. Its
// snapshot is the caller's to delete.
func (s *Store) DeleteWorld(ctx context.Context, orgID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM worlds WHERE org_id = ? AND id = ?`, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := deleteDatapackImages(ctx, s.db, id); err != nil {
		return err
	}
	return s.DeleteImage(ctx, WorldImage(id))
}

func worldNameTaken(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: worlds.org_id, worlds.name") {
		return ErrWorldNameTaken
	}
	return err
}
