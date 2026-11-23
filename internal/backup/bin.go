package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The bin holds snapshots that someone deleted, for a while, before they are
// removed for good. A snapshot in it is still in the repository, whole and
// under its own ID, and is only left out of every list and lookup: nothing
// can restore it, copy it or be made from it until it is put back. What says
// which are in the bin is a file beside the repository, as the rejected
// ones' is, so that it goes wherever the repository goes and every caller of
// List and Get is kept from them without having to know.
//
// Retention never puts anything in the bin: what the schedule lets go of is
// removed as it was. It does not take anything out of it either: a snapshot
// that someone deleted waits out its time there even if the schedule would
// have let it go.

// binFile notes the snapshots of an organization that are in the bin, by
// full ID, with when they were put there and by whom.
func (m *Manager) binFile(org string) string { return filepath.Join(m.Dir, org+".bin") }

// BinEntry is when a snapshot was put in the bin, and by whom.
type BinEntry struct {
	At time.Time `json:"at"`
	By string    `json:"by,omitempty"`
}

// Binned is a snapshot in the bin.
type Binned struct {
	Snapshot
	DeletedAt time.Time `json:"deletedAt"`
	DeletedBy string    `json:"deletedBy,omitempty"`
}

// ErrNotBinned is returned for a snapshot asked to be put back or removed
// from the bin that is not in it.
var ErrNotBinned = errors.New("that snapshot is not in Recently deleted")

// binned is what is in an organization's bin. Only a file that is not there
// means nothing: one that cannot be read is an error, so that a snapshot in
// the bin is never listed for want of reading it.
func (m *Manager) binned(org string) (map[string]BinEntry, error) {
	m.binMu.Lock()
	defer m.binMu.Unlock()
	return m.readBin(org)
}

func (m *Manager) readBin(org string) (map[string]BinEntry, error) {
	out := map[string]BinEntry{}
	data, err := os.ReadFile(m.binFile(org))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the recently deleted snapshots: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("reading the recently deleted snapshots: %w", err)
	}
	return out, nil
}

// changeBin changes what is in an organization's bin, written whole or not
// at all.
func (m *Manager) changeBin(org string, change func(map[string]BinEntry) error) error {
	m.binMu.Lock()
	defer m.binMu.Unlock()
	entries, err := m.readBin(org)
	if err != nil {
		return err
	}
	if err := change(entries); err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := os.Remove(m.binFile(org)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	tmp := m.binFile(org) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, m.binFile(org))
}

// Bin puts snapshots in the bin, by their full IDs or unambiguous prefixes:
// all of them, or, if any is not there to be deleted, none. by is who
// deleted them; at is when. It returns them as they were found.
func (m *Manager) Bin(ctx context.Context, org string, ids []string, by string, at time.Time) ([]Snapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	lock := m.lock(org)
	// Held alone, as a removal is: nothing that has the snapshot in hand,
	// a restore or a copy of it, is still using it once it is hidden.
	lock.Lock()
	defer lock.Unlock()
	list, err := m.list(ctx, org)
	if err != nil {
		return nil, err
	}
	found := make([]Snapshot, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		snap := byPrefix(list, id)
		if snap == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if !seen[snap.ID] {
			seen[snap.ID] = true
			found = append(found, *snap)
		}
	}
	err = m.changeBin(org, func(entries map[string]BinEntry) error {
		for _, s := range found {
			entries[s.ID] = BinEntry{At: at.UTC(), By: by}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// byPrefix is the one snapshot of list an ID, or the start of one, names.
func byPrefix(list []Snapshot, id string) *Snapshot {
	if !idPattern.MatchString(id) {
		return nil
	}
	var found *Snapshot
	for i := range list {
		if strings.HasPrefix(list[i].ID, id) {
			if found != nil {
				return nil
			}
			found = &list[i]
		}
	}
	return found
}

// BinDue says whether anything in an organization's bin is due to go: read
// from the note alone, without holding or listing the repository.
func (m *Manager) BinDue(org string, due func(deletedAt time.Time) bool) bool {
	entries, err := m.binned(org)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if due(e.At) {
			return true
		}
	}
	return false
}

// BinList lists what is in an organization's bin, the most recently deleted
// first. A note of one that is no longer in the repository, removed some
// other way, is not listed.
func (m *Manager) BinList(ctx context.Context, org string) ([]Binned, error) {
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	return m.binList(ctx, org)
}

func (m *Manager) binList(ctx context.Context, org string) ([]Binned, error) {
	entries, err := m.binned(org)
	if err != nil || len(entries) == 0 {
		return []Binned{}, err
	}
	present, err := m.present(ctx, org)
	if err != nil {
		return nil, err
	}
	out := []Binned{}
	for _, s := range present {
		if e, ok := entries[s.ID]; ok {
			out = append(out, Binned{Snapshot: s, DeletedAt: e.At, DeletedBy: e.By})
		}
	}
	// The present list is newest first, which stays the order within one
	// deletion.
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out, nil
}

// PutBack takes snapshots out of the bin, by their full IDs or unambiguous
// prefixes, back into the lists: all of them, or none if any is not in it.
// It returns them.
func (m *Manager) PutBack(ctx context.Context, org string, ids []string) ([]Binned, error) {
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	which, err := m.binnedByID(ctx, org, ids)
	if err != nil {
		return nil, err
	}
	err = m.changeBin(org, func(entries map[string]BinEntry) error {
		for _, b := range which {
			delete(entries, b.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return which, nil
}

// binnedByID finds the snapshots in the bin that the IDs, or starts of
// them, name: every one of them, or ErrNotBinned. With the lock held.
func (m *Manager) binnedByID(ctx context.Context, org string, ids []string) ([]Binned, error) {
	list, err := m.binList(ctx, org)
	if err != nil {
		return nil, err
	}
	snaps := make([]Snapshot, len(list))
	for i := range list {
		snaps[i] = list[i].Snapshot
	}
	out := make([]Binned, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		snap := byPrefix(snaps, id)
		if snap == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotBinned, id)
		}
		if seen[snap.ID] {
			continue
		}
		seen[snap.ID] = true
		for _, b := range list {
			if b.ID == snap.ID {
				out = append(out, b)
			}
		}
	}
	return out, nil
}

// EmptyBin removes snapshots in the bin for good, with the data only they
// referred to, in one pass over the repository: those the IDs name, or with
// no IDs, every one that should goes. It returns what was removed.
func (m *Manager) EmptyBin(ctx context.Context, org string, ids []string, should func(Binned) bool) ([]Binned, error) {
	if !m.exists(org) {
		return nil, nil
	}
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	var which []Binned
	if len(ids) > 0 {
		var err error
		if which, err = m.binnedByID(ctx, org, ids); err != nil {
			return nil, err
		}
	} else {
		list, err := m.binList(ctx, org)
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			if should == nil || should(b) {
				which = append(which, b)
			}
		}
	}
	// Notes of snapshots removed some other way go too.
	if err := m.dropGoneFromBin(ctx, org); err != nil {
		return nil, err
	}
	if len(which) == 0 {
		return nil, nil
	}
	gone := make([]string, len(which))
	for i, b := range which {
		gone[i] = b.ID
	}
	if err := m.deleteMany(ctx, org, gone); err != nil {
		return nil, err
	}
	// Removed first, then taken off the note: a note left of a snapshot that
	// is gone lists nothing, and goes the next time.
	err := m.changeBin(org, func(entries map[string]BinEntry) error {
		for _, id := range gone {
			delete(entries, id)
		}
		return nil
	})
	if err != nil {
		m.Log.Warn("could not take snapshots removed for good off the recently deleted", "org", org, "error", err)
	}
	return which, nil
}

// dropGoneFromBin takes notes off the bin of snapshots that are not in the
// repository any more. With the lock held alone, so that none is added
// meanwhile.
func (m *Manager) dropGoneFromBin(ctx context.Context, org string) error {
	entries, err := m.binned(org)
	if err != nil || len(entries) == 0 {
		return err
	}
	all, err := m.listAll(ctx, org)
	if err != nil {
		return err
	}
	there := make(map[string]bool, len(all))
	for _, s := range all {
		there[s.ID] = true
	}
	stale := false
	for id := range entries {
		if !there[id] {
			stale = true
		}
	}
	if !stale {
		return nil
	}
	return m.changeBin(org, func(entries map[string]BinEntry) error {
		for id := range entries {
			if !there[id] {
				delete(entries, id)
			}
		}
		return nil
	})
}

// unchangedFrom is what a snapshot of host, taken only if something changed,
// is to be compared with. restic compares it with the host's newest, which
// may be in the bin: then with the newest that is not, given as parent. If
// all of the host's are in the bin, skip is false: one is taken whatever
// changed, or once the bin is emptied there would be none of the files as
// they are. With the lock held.
func (m *Manager) unchangedFrom(ctx context.Context, org, host string) (parent string, skip bool, err error) {
	binned, err := m.binned(org)
	if err != nil || len(binned) == 0 {
		return "", true, err
	}
	present, err := m.present(ctx, org)
	if err != nil {
		return "", false, err
	}
	newestBinned := false
	for _, s := range present {
		if s.Server != host {
			continue
		}
		if _, in := binned[s.ID]; in {
			newestBinned = true
			continue
		}
		if !newestBinned {
			return "", true, nil
		}
		return s.ID, true, nil
	}
	// None of the host's, or none out of the bin.
	return "", !newestBinned, nil
}
