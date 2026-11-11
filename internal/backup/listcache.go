package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
)

// Listing a repository's snapshots runs restic, which takes a good part of a
// second, and every page that shows a template, a world or a backup asks.
// What restic answers cannot change unless the repository does, and a
// snapshot is a file in it named by the hash of what it holds: any snapshot
// added, removed or given other tags is a change in the names of those files.
// So the answer is kept for as long as they are the same, by whatever changed
// them, and nothing has to remember to say so.

// repoAnswers is what is kept of a repository, for the state it was read in.
type repoAnswers struct {
	state string
	list  []Snapshot
	usage *Usage
}

// repoState names what is in a repository now, in the time it takes to list
// a folder: "" where that cannot be told, as for a repository of another
// kind, for which nothing is kept.
func (m *Manager) repoState(org string) string {
	entries, err := os.ReadDir(filepath.Join(m.repoDir(org), "snapshots"))
	if err != nil {
		return ""
	}
	h := sha256.New()
	for _, e := range entries {
		// A snapshot being written is under a name of its own until it is
		// whole.
		if len(e.Name()) == 64 {
			h.Write([]byte(e.Name()))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// kept is what is kept of a repository in the state given, if that is the
// state it was read in.
func (m *Manager) kept(org, state string) (repoAnswers, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.answers[org]
	return a, ok && state != "" && a.state == state
}

// keep notes what was read of a repository in the state given, which was
// asked for before it was read: if the repository changed meanwhile, its
// state is another by the next time, and this is not used.
func (m *Manager) keep(org, state string, change func(*repoAnswers)) {
	if state == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.answers == nil {
		m.answers = map[string]repoAnswers{}
	}
	a := m.answers[org]
	if a.state != state {
		a = repoAnswers{state: state}
	}
	change(&a)
	m.answers[org] = a
}

// keptList is the snapshots kept for a state, as a list of the caller's own.
func (m *Manager) keptList(org, state string) ([]Snapshot, bool) {
	a, ok := m.kept(org, state)
	if !ok || a.list == nil {
		return nil, false
	}
	return slices.Clone(a.list), true
}

// maxRecords is how many records of snapshots are kept. Each is a few
// kilobytes; past this, reading some again costs little.
const maxRecords = 1000

// keptRecord is the record of a snapshot, if it has been read. A snapshot
// never changes, and its ID says exactly what it holds.
func (m *Manager) keptRecord(org, id string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[org+"/"+id]
	return slices.Clone(record), ok
}

func (m *Manager) keepRecord(org, id string, record []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil || len(m.records) >= maxRecords {
		m.records = map[string][]byte{}
	}
	m.records[org+"/"+id] = slices.Clone(record)
}
