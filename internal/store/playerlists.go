package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

// A player list is a named list of Minecraft players an organization keeps,
// such as its club's members or those signed up for an event, to put on the
// whitelist or operators of its servers. Servers name the lists they use;
// the players are kept here, by UUID, with the name each had when last
// looked up.

// ErrPlayerListNameTaken is returned for a list named as another of the same
// organization is, whatever their case.
var ErrPlayerListNameTaken = errors.New("another list has that name")

// ErrPlayerListFull is returned for players that would take a list past
// MaxListPlayers.
var ErrPlayerListFull = errors.New("the list is full")

// ErrTooManyPlayerLists is returned for a list that would take an
// organization past MaxPlayerLists.
var ErrTooManyPlayerLists = errors.New("too many lists")

const (
	// MaxListPlayers is how many players one list can have: as many as a
	// server's own list of names.
	MaxListPlayers = 1000
	// MaxPlayerLists is how many lists one organization can have.
	MaxPlayerLists = 200
)

// The kinds of player a list can have: one of Java Edition, by their
// Minecraft account, or one of Bedrock Edition, by their Xbox account, who
// joins through Geyser and Floodgate.
const (
	KindJava    = "java"
	KindBedrock = "bedrock"
)

// ListPlayer is a player on a list. A Java player is kept by their
// account's UUID, with its name. A Bedrock player is kept by the UUID
// Floodgate gives them, which is made from their XUID, with their gamertag
// as Name.
type ListPlayer struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	XUID string `json:"xuid,omitempty"`
}

// Bedrock is whether the player is one of Bedrock Edition.
func (p ListPlayer) Bedrock() bool { return p.Kind == KindBedrock }

// kindOf is a player's kind as it is kept: Java unless said otherwise.
func kindOf(p ListPlayer) string {
	if p.Bedrock() {
		return KindBedrock
	}
	return KindJava
}

// PlayerList is a list with its players, by name.
type PlayerList struct {
	ID        string
	OrgID     string
	Name      string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
	Players   []ListPlayer
}

// CreatePlayerList keeps a new list with the players it has, giving it an
// ID and its times.
func (s *Store) CreatePlayerList(ctx context.Context, l *PlayerList) error {
	if len(l.Players) > MaxListPlayers {
		return ErrPlayerListFull
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM player_lists WHERE org_id = ?`, l.OrgID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxPlayerLists {
		return ErrTooManyPlayerLists
	}
	l.ID = NewID(8)
	l.CreatedAt = time.Now().Truncate(time.Second)
	l.UpdatedAt = l.CreatedAt
	if _, err := tx.ExecContext(ctx, `INSERT INTO player_lists (id, org_id, name, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		l.ID, l.OrgID, l.Name, l.CreatedBy, l.CreatedAt.Unix(), l.UpdatedAt.Unix()); err != nil {
		return listNameTaken(err)
	}
	if err := insertListPlayers(ctx, tx, l.ID, l.Players); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	l.Players = uniquePlayers(l.Players)
	return nil
}

func insertListPlayers(ctx context.Context, tx *sql.Tx, id string, players []ListPlayer) error {
	at := time.Now().Unix()
	for _, p := range players {
		if _, err := tx.ExecContext(ctx, `INSERT INTO player_list_entries (list_id, uuid, name, kind, xuid, added_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (list_id, uuid) DO UPDATE SET name = excluded.name, kind = excluded.kind, xuid = excluded.xuid`, id, p.UUID, p.Name, kindOf(p), p.XUID, at); err != nil {
			return err
		}
	}
	return nil
}

// uniquePlayers is players each once, by UUID, sorted by name.
func uniquePlayers(players []ListPlayer) []ListPlayer {
	seen := map[string]int{}
	out := []ListPlayer{}
	for _, p := range players {
		p.Kind = kindOf(p)
		if i, ok := seen[p.UUID]; ok {
			out[i] = p
			continue
		}
		seen[p.UUID] = len(out)
		out = append(out, p)
	}
	sortPlayers(out)
	return out
}

func sortPlayers(players []ListPlayer) {
	slices.SortFunc(players, func(a, b ListPlayer) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
}

// PlayerLists lists an organization's lists by name, each with its players.
func (s *Store) PlayerLists(ctx context.Context, orgID string) ([]PlayerList, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, org_id, name, created_by, created_at, updated_at FROM player_lists WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	out := []PlayerList{}
	at := map[string]int{}
	for rows.Next() {
		var l PlayerList
		var created, updated int64
		if err := rows.Scan(&l.ID, &l.OrgID, &l.Name, &l.CreatedBy, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		l.CreatedAt, l.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		l.Players = []ListPlayer{}
		at[l.ID] = len(out)
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	entries, err := s.db.QueryContext(ctx, `SELECT e.list_id, e.uuid, e.name, e.kind, e.xuid FROM player_list_entries e JOIN player_lists l ON l.id = e.list_id
		WHERE l.org_id = ? ORDER BY e.name COLLATE NOCASE`, orgID)
	if err != nil {
		return nil, err
	}
	defer entries.Close()
	for entries.Next() {
		var id string
		var p ListPlayer
		if err := entries.Scan(&id, &p.UUID, &p.Name, &p.Kind, &p.XUID); err != nil {
			return nil, err
		}
		if i, ok := at[id]; ok {
			out[i].Players = append(out[i].Players, p)
		}
	}
	return out, entries.Err()
}

// PlayerList returns one of an organization's lists with its players, or
// ErrNotFound.
func (s *Store) PlayerList(ctx context.Context, orgID, id string) (*PlayerList, error) {
	var l PlayerList
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, org_id, name, created_by, created_at, updated_at FROM player_lists WHERE org_id = ? AND id = ?`, orgID, id).
		Scan(&l.ID, &l.OrgID, &l.Name, &l.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	l.CreatedAt, l.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	rows, err := s.db.QueryContext(ctx, `SELECT uuid, name, kind, xuid FROM player_list_entries WHERE list_id = ? ORDER BY name COLLATE NOCASE`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	l.Players = []ListPlayer{}
	for rows.Next() {
		var p ListPlayer
		if err := rows.Scan(&p.UUID, &p.Name, &p.Kind, &p.XUID); err != nil {
			return nil, err
		}
		l.Players = append(l.Players, p)
	}
	return &l, rows.Err()
}

// RenamePlayerList gives a list another name.
func (s *Store) RenamePlayerList(ctx context.Context, orgID, id, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE player_lists SET name = ?, updated_at = ? WHERE org_id = ? AND id = ?`, name, time.Now().Unix(), orgID, id)
	if err != nil {
		return listNameTaken(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlayerList forgets a list and its players.
func (s *Store) DeletePlayerList(ctx context.Context, orgID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM player_lists WHERE org_id = ? AND id = ?`, orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddListPlayers puts players on a list. One on it already is kept, with
// the name given now. None are added if that would take it past
// MaxListPlayers.
func (s *Store) AddListPlayers(ctx context.Context, orgID, id string, players []ListPlayer) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if err := touchList(ctx, tx, orgID, id); err != nil {
		return err
	}
	if err := insertListPlayers(ctx, tx, id, players); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM player_list_entries WHERE list_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > MaxListPlayers {
		return ErrPlayerListFull
	}
	return tx.Commit()
}

// RemoveListPlayer takes a player off a list, by UUID.
func (s *Store) RemoveListPlayer(ctx context.Context, orgID, id, uuid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if err := touchList(ctx, tx, orgID, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM player_list_entries WHERE list_id = ? AND uuid = ?`, id, uuid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// RenameListPlayer gives a player the name their account has now, a
// Bedrock player their gamertag, on every list they are on.
func (s *Store) RenameListPlayer(ctx context.Context, uuid, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE player_list_entries SET name = ? WHERE uuid = ?`, name, uuid)
	return err
}

// touchList says a list of the organization was changed now, or that there
// is no such list.
func touchList(ctx context.Context, tx *sql.Tx, orgID, id string) error {
	res, err := tx.ExecContext(ctx, `UPDATE player_lists SET updated_at = ? WHERE org_id = ? AND id = ?`, time.Now().Unix(), orgID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func listNameTaken(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: player_lists.org_id, player_lists.name") {
		return ErrPlayerListNameTaken
	}
	return err
}
