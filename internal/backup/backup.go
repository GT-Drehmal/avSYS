// Package backup keeps one restic repository per organization and takes and
// restores snapshots of servers through their workspaces.
//
// The repositories live on the panel's own storage. A snapshot runs where the
// files are, in the server's workspace: the panel serves the organization's
// repository over the SSH connection it already has to that workspace, on a
// socket only that pod can see. What is served there can be added to but not
// deleted from, so a server cannot destroy its own history, let alone anyone
// else's. Expiring and deleting snapshots is done by the panel alone, working
// on the repository directly.
package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/restic/rest-server/repo"
	"golang.org/x/crypto/ssh"
)

const (
	// dataDir is where a server's files are mounted in its workspace.
	dataDir = "/data"
	// RecordPath is a file written into every snapshot that describes the
	// server it was taken from, so that a repository explains itself without
	// the panel's database.
	RecordPath = dataDir + "/.avsys/snapshot.json"
	// socketDir is private to the workspace's user.
	socketDir = "/tmp/avsys"
)

// excluded are left out of snapshots and left alone by restores: the
// workspace's own home directory (shell history, editor servers, restic's
// cache) and download caches. Paths are relative to the server directory.
var excluded = []string{"/.workspace", "/.cache"}

// notSaved are left out of snapshots but not left alone by restores: an
// import's archive, what it was unpacked into and the parts of one being
// sent, there only while one runs. A restore over an import that was cut
// short takes them away.
var notSaved = []string{"/.avsys/import", "/.avsys/import.archive", "/.avsys/import.upload", "/.avsys/import.upload.id"}

// excludedFromWhole are left out of a whole snapshot, and of putting one
// back: download caches, restic's own among them.
var excludedFromWhole = []string{"/.workspace/.cache", "/.cache"}

func exclusions(whole bool) []string {
	if whole {
		return excludedFromWhole
	}
	return excluded
}

// Triggers say why a snapshot was taken.
const (
	TriggerManual     = "manual"
	TriggerScheduled  = "scheduled"
	TriggerPreRestore = "pre-restore"
	// TriggerPreReplace is the snapshot taken of a server before its world
	// is replaced with one uploaded.
	TriggerPreReplace = "pre-replace"
	// TriggerFinal is the snapshot taken of a server as it is deleted.
	TriggerFinal = "final"
	// TriggerCopy is the snapshot a copy of a server is made from.
	TriggerCopy = "copy"
	// TriggerMove is the snapshot that carries a server's files to the
	// node it is moved to.
	TriggerMove = "move"
	// TriggerTemplate is a version of a template, taken of a server under
	// the template's own name.
	TriggerTemplate = "template"
)

var (
	// ErrNotFound is returned for a snapshot the repository does not hold.
	ErrNotFound = errors.New("no such snapshot")
	// ErrUnavailable is returned when the panel has no restic to run.
	ErrUnavailable = errors.New("restic is not installed where the panel runs")
	// ErrIncomplete is returned when some of the server's files could not be
	// read. Nothing is kept: restoring such a snapshot would delete whatever
	// it had missed.
	ErrIncomplete = errors.New("some of the server's files could not be read, so no snapshot was kept")
	// ErrUnchanged is returned for a snapshot asked for with SkipUnchanged
	// when the server's files are exactly as its newest snapshot has them.
	ErrUnchanged = errors.New("nothing has changed since the last snapshot")
	// ErrOldWorkspace is returned when the server's workspace has no restic:
	// its pod was started by a panel from before snapshots existed.
	ErrOldWorkspace = errors.New("this server's workspace is from before snapshots existed; restart the server to update it, then try again")

	idPattern  = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	tagPattern = regexp.MustCompile(`^[A-Za-z0-9._=@+-]+$`)
)

// Manager owns the repositories under Dir.
type Manager struct {
	// Dir holds one repository per organization, named by its slug, next to
	// the file with its password.
	Dir string
	// Restic is the restic binary the panel itself runs.
	Restic string
	Log    *slog.Logger
	// Offsite is where every repository is copied to, if anywhere.
	Offsite *Offsite

	mu    sync.Mutex
	locks map[string]*sync.RWMutex
	// rejectedMu holds the files of rejected snapshots to one change at a
	// time; see reject.
	rejectedMu sync.Mutex
	// changed is when an organization's snapshots last changed, as far as
	// this process has seen.
	changed map[string]time.Time
}

// Snapshot is one saved state of a server.
type Snapshot struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Server  string    `json:"server"`
	Trigger string    `json:"trigger"`
	// By is the user who asked for it, empty for scheduled ones.
	By string `json:"by,omitempty"`
	// Version and Loader describe what the server ran, for display.
	Version string `json:"version,omitempty"`
	Loader  string `json:"loader,omitempty"`
	// Size is how large the server's files were. Added is how much this
	// snapshot added to the repository once deduplicated and compressed.
	Size  int64 `json:"size"`
	Added int64 `json:"added"`
	Files int64 `json:"files"`
	// Lineage is the same for a snapshot and for every copy of it in another
	// repository, which each have an ID of their own.
	Lineage string `json:"-"`
}

// Meta describes the server a snapshot is being taken of.
type Meta struct {
	Server  string
	Trigger string
	By      string
	Version string
	Loader  string
	// Record is stored in the snapshot at RecordPath. It has to come out the
	// same for the same server settings, or no two snapshots would ever be
	// alike.
	Record any
	// SkipUnchanged takes no snapshot, and returns ErrUnchanged, when the
	// files are as the server's newest snapshot has them.
	SkipUnchanged bool
	// Whole takes the workspace's home too, all but restic's own cache in
	// it: for a snapshot that is all there will be of the server's files,
	// as one that carries them to another node.
	Whole bool
}

// Keep says how many scheduled snapshots of a server outlive their day: the
// newest of each of the last Daily days that have one, and likewise for
// weeks, months and years.
type Keep struct {
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
	Yearly  int `json:"yearly"`
}

// Progress is reported while a snapshot or restore runs.
type Progress struct {
	Percent float64 `json:"percent"`
	Bytes   int64   `json:"bytes"`
	Total   int64   `json:"total"`
}

func (m *Manager) repoDir(org string) string { return filepath.Join(m.Dir, org) }
func (m *Manager) keyFile(org string) string { return filepath.Join(m.Dir, org+".key") }

// rejectedFile lists, one to a line, the snapshots of an organization that
// were found incomplete and are to be removed: until they are, they are not
// listed, so that one whose removal failed is never taken for a good one.
func (m *Manager) rejectedFile(org string) string { return filepath.Join(m.Dir, org+".rejected") }

// ErrRejectedLeft is returned by Forget while a rejected snapshot could not be
// removed yet: retention waits for it.
var ErrRejectedLeft = errors.New("an incomplete snapshot could not be removed yet, so none are let go of until it is")

// rejected is the set of an organization's rejected snapshots. Only a file
// that is not there means none: one that cannot be read is an error, so that
// a rejected snapshot is never listed for want of reading it.
func (m *Manager) rejected(org string) (map[string]bool, error) {
	m.rejectedMu.Lock()
	defer m.rejectedMu.Unlock()
	return m.readRejected(org)
}

func (m *Manager) readRejected(org string) (map[string]bool, error) {
	out := map[string]bool{}
	data, err := os.ReadFile(m.rejectedFile(org))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the rejected snapshots: %w", err)
	}
	for _, id := range strings.Fields(string(data)) {
		out[id] = true
	}
	return out, nil
}

// reject adds a snapshot to its organization's rejected ones, or with gone
// takes it off once it has been removed.
func (m *Manager) reject(org, id string, gone bool) error {
	m.rejectedMu.Lock()
	defer m.rejectedMu.Unlock()
	ids, err := m.readRejected(org)
	if err != nil {
		return err
	}
	if gone {
		delete(ids, id)
	} else {
		ids[id] = true
	}
	if len(ids) == 0 {
		if err := os.Remove(m.rejectedFile(org)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	lines := make([]string, 0, len(ids))
	for id := range ids {
		lines = append(lines, id)
	}
	sort.Strings(lines)
	tmp := m.rejectedFile(org) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.rejectedFile(org))
}

// removeRejected tries again to remove the snapshots of an organization that
// were rejected and are still there.
func (m *Manager) removeRejected(ctx context.Context, org string) {
	ids, err := m.rejected(org)
	if err != nil {
		m.Log.Warn("could not read the rejected snapshots", "org", org, "error", err)
		return
	}
	for id := range ids {
		err := m.Delete(ctx, org, id)
		if err == nil || errors.Is(err, ErrNotFound) {
			if err := m.reject(org, id, true); err != nil {
				m.Log.Warn("could not note a rejected snapshot as removed", "org", org, "snapshot", id, "error", err)
			}
			continue
		}
		m.Log.Warn("could not remove a rejected snapshot; trying again after the next", "org", org, "snapshot", id, "error", err)
	}
}

// Available reports whether the panel can run restic at all.
func (m *Manager) Available() bool {
	_, err := exec.LookPath(m.Restic)
	return err == nil
}

// lock returns the lock for an organization's repository. Snapshots and
// restores share it; anything that removes data holds it alone.
func (m *Manager) lock(org string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[string]*sync.RWMutex{}
	}
	if m.locks[org] == nil {
		m.locks[org] = &sync.RWMutex{}
	}
	return m.locks[org]
}

// Holds reports how many snapshots an organization has. known is false when
// that cannot be said: there is something where its repository would be that
// the panel cannot read, a repository it has no restic for, or one that has
// lost its configuration. Callers deciding whether it is safe to delete must
// treat that as "some".
func (m *Manager) Holds(ctx context.Context, org string) (count int, known bool) {
	if m.exists(org) {
		list, err := m.List(ctx, org)
		return len(list), err == nil
	}
	// No usable repository. That is only "none" if nothing is there at all.
	entries, err := os.ReadDir(m.repoDir(org))
	switch {
	case errors.Is(err, os.ErrNotExist):
		_, err := os.Stat(m.keyFile(org))
		return 0, errors.Is(err, os.ErrNotExist)
	case err != nil:
		return 0, false
	}
	return 0, len(entries) == 0
}

// exists reports whether the organization has a repository yet.
func (m *Manager) exists(org string) bool {
	_, err := os.Stat(filepath.Join(m.repoDir(org), "config"))
	return err == nil
}

// password returns the repository password, creating it on first use. It is
// kept in a file beside the repository rather than in the panel's database,
// so that the two cannot be separated: whoever has the repository directory
// can restore from it with plain restic.
func (m *Manager) password(org string) (string, error) {
	if data, err := os.ReadFile(m.keyFile(org)); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(raw)
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(m.keyFile(org), []byte(secret+"\n"), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

// restic runs the panel's own restic against an organization's repository.
func (m *Manager) restic(ctx context.Context, org string, args ...string) ([]byte, error) {
	return m.resticEnv(ctx, org, nil, args...)
}

// resticEnv is restic with more in its environment.
func (m *Manager) resticEnv(ctx context.Context, org string, env []string, args ...string) ([]byte, error) {
	if !m.Available() {
		return nil, ErrUnavailable
	}
	if _, err := m.password(org); err != nil {
		return nil, err
	}
	full := append([]string{"--repo", m.repoDir(org), "--password-file", m.keyFile(org), "--no-cache"}, args...)
	cmd := exec.CommandContext(ctx, m.Restic, full...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("restic %s: %w: %s", args[0], err, lastLine(stderr.String()))
	}
	return out, nil
}

// ensure creates the organization's repository if it has none.
func (m *Manager) ensure(ctx context.Context, org string) error {
	if m.exists(org) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exists(org) {
		return nil
	}
	if err := os.MkdirAll(m.repoDir(org), 0o700); err != nil {
		return err
	}
	if _, err := m.restic(ctx, org, "init"); err != nil {
		return err
	}
	m.Log.Info("created snapshot repository", "org", org)
	return nil
}

type resticSnapshot struct {
	ID string `json:"id"`
	// Original is set on a snapshot that was copied from another repository,
	// to the ID of the first one it descends from.
	Original string    `json:"original"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Summary  *struct {
		TotalBytes int64 `json:"total_bytes_processed"`
		TotalFiles int64 `json:"total_files_processed"`
		AddedPack  int64 `json:"data_added_packed"`
	} `json:"summary"`
}

func (r resticSnapshot) snapshot() Snapshot {
	s := Snapshot{ID: r.ID, Time: r.Time, Server: r.Hostname, Trigger: TriggerManual, Lineage: r.Original}
	if s.Lineage == "" {
		s.Lineage = r.ID
	}
	for _, tag := range r.Tags {
		key, value, ok := strings.Cut(tag, "=")
		if !ok {
			continue
		}
		switch key {
		case "trigger":
			s.Trigger = value
		case "by":
			s.By = value
		case "version":
			s.Version = value
		case "loader":
			s.Loader = value
		case "lineage":
			// A copy made here under another host says whose it is: it
			// remembers the one it was made from as its original too.
			s.Lineage = value
		}
	}
	if r.Summary != nil {
		s.Size, s.Files, s.Added = r.Summary.TotalBytes, r.Summary.TotalFiles, r.Summary.AddedPack
	}
	return s
}

// List returns an organization's snapshots, newest first. They come from the
// repository itself, so servers that no longer exist are still listed.
func (m *Manager) List(ctx context.Context, org string) ([]Snapshot, error) {
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	return m.list(ctx, org)
}

// list is List for callers that already hold the repository's lock. Taking a
// read lock twice deadlocks as soon as a writer is waiting in between.
// Rejected snapshots are left out.
func (m *Manager) list(ctx context.Context, org string) ([]Snapshot, error) {
	all, err := m.listAll(ctx, org)
	if err != nil {
		return nil, err
	}
	rejected, err := m.rejected(org)
	if err != nil {
		return nil, err
	}
	if len(rejected) == 0 {
		return all, nil
	}
	out := all[:0:0]
	for _, s := range all {
		if !rejected[s.ID] {
			out = append(out, s)
		}
	}
	return out, nil
}

// listAll is list with the rejected snapshots too.
func (m *Manager) listAll(ctx context.Context, org string) ([]Snapshot, error) {
	if !m.exists(org) {
		return []Snapshot{}, nil
	}
	out, err := m.restic(ctx, org, "snapshots", "--json", "--no-lock")
	if err != nil {
		return nil, err
	}
	var raw []resticSnapshot
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("reading the snapshot list: %w", err)
	}
	list := make([]Snapshot, 0, len(raw))
	for _, r := range raw {
		list = append(list, r.snapshot())
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time.After(list[j].Time) })
	return list, nil
}

// Get returns one snapshot by its full ID or an unambiguous prefix of it.
func (m *Manager) Get(ctx context.Context, org, id string) (*Snapshot, error) {
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	return m.get(ctx, org, id)
}

// get is Get for callers that already hold the repository's lock.
func (m *Manager) get(ctx context.Context, org, id string) (*Snapshot, error) {
	if !idPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	list, err := m.list(ctx, org)
	if err != nil {
		return nil, err
	}
	var found *Snapshot
	for i := range list {
		if strings.HasPrefix(list[i].ID, id) {
			if found != nil {
				return nil, ErrNotFound
			}
			found = &list[i]
		}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	return found, nil
}

// CopyAs makes a snapshot of what id holds under host, as a version of a
// template that by saved: nothing is copied, as the new one refers to the
// same files. It is dated now, and has a lineage of its own, so that
// removing either one never takes the other's copy offsite with it.
func (m *Manager) CopyAs(ctx context.Context, org, id, host, by string) (*Snapshot, error) {
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	src, err := m.get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	// As in DeleteMany: a lock left in the repository by a snapshot that was
	// cut short would only make this fail.
	if _, err := m.restic(ctx, org, "unlock", "--remove-all"); err != nil {
		return nil, err
	}
	// What is there already, so that the one made is told apart from them:
	// src itself may be under host, and so may a copy of it left by one
	// that was cut short.
	before, err := m.list(ctx, org)
	if err != nil {
		return nil, err
	}
	was := map[string]bool{}
	for _, s := range before {
		was[s.ID] = true
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := m.restic(ctx, org, "rewrite", "--new-host", host, "--new-time", now.Format("2006-01-02 15:04:05"), src.ID); err != nil {
		return nil, err
	}
	// The one just made. Held as this is, there is no other.
	made, err := m.find(ctx, org, func(s Snapshot) bool { return s.Server == host && !was[s.ID] })
	if err != nil {
		return nil, err
	}
	// restic removes after it adds: a tag the copy keeps is not removed. A
	// copy of a copy brought its original's lineage along with its tags.
	args := []string{"tag", "--remove", "lineage=" + src.Lineage, "--add", "trigger=" + TriggerTemplate, "--add", "lineage=" + made.ID}
	if src.Trigger != TriggerTemplate {
		args = append(args, "--remove", "trigger="+src.Trigger)
	}
	if src.By != "" && src.By != by {
		args = append(args, "--remove", "by="+src.By)
	}
	if by != "" {
		args = append(args, "--add", "by="+by)
	}
	if _, err := m.restic(ctx, org, append(args, made.ID)...); err != nil {
		return nil, err
	}
	m.touch(org)
	return m.find(ctx, org, func(s Snapshot) bool { return s.Lineage == made.ID })
}

// find is the one snapshot that is as asked.
func (m *Manager) find(ctx context.Context, org string, is func(Snapshot) bool) (*Snapshot, error) {
	list, err := m.list(ctx, org)
	if err != nil {
		return nil, err
	}
	var found *Snapshot
	for i := range list {
		if is(list[i]) {
			if found != nil {
				return nil, fmt.Errorf("more than one snapshot is %s's", list[i].Server)
			}
			found = &list[i]
		}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	return found, nil
}

// Usage is how much storage an organization's repository takes.
type Usage struct {
	Bytes     int64 `json:"bytes"`
	Snapshots int   `json:"snapshots"`
}

// Usage measures the repository on disk.
func (m *Manager) Usage(ctx context.Context, org string) (Usage, error) {
	if !m.exists(org) {
		return Usage{}, nil
	}
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	out, err := m.restic(ctx, org, "stats", "--mode", "raw-data", "--json", "--no-lock")
	if err != nil {
		return Usage{}, err
	}
	var stats struct {
		TotalSize int64 `json:"total_size"`
		Snapshots int   `json:"snapshots_count"`
	}
	if err := json.Unmarshal(out, &stats); err != nil {
		return Usage{}, err
	}
	return Usage{Bytes: stats.TotalSize, Snapshots: stats.Snapshots}, nil
}

// Record returns the description of the server stored inside a snapshot.
func (m *Manager) Record(ctx context.Context, org, id string) ([]byte, error) {
	// Held from finding the snapshot to reading out of it, so that it cannot
	// be deleted in between.
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	snap, err := m.get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	return m.restic(ctx, org, "dump", "--no-lock", snap.ID, RecordPath)
}

// ErrTooLarge is returned for a file in a snapshot larger than was asked for.
var ErrTooLarge = errors.New("the file is larger than allowed")

// File returns one file out of a snapshot, if it is no larger than limit.
// It is nil, with no error, when the snapshot does not have that file. The
// files are the server's own, and anything at all can be in them: what is
// read is bounded, and is for the caller to check.
func (m *Manager) File(ctx context.Context, org, id, path string, limit int64) ([]byte, error) {
	// Held from finding the snapshot to reading out of it, so that it cannot
	// be deleted in between.
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	snap, err := m.get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, m.Restic, "--repo", m.repoDir(org), "--password-file", m.keyFile(org), "--no-cache", "dump", "--no-lock", snap.ID, path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(out, limit+1))
	if err == nil && int64(len(data)) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ErrTooLarge
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		if strings.Contains(stderr.String(), "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("restic dump: %w: %s", waitErr, lastLine(stderr.String()))
	}
	return data, err
}

// KeepFile saves what r gives as a snapshot of one file called name, under
// host, as a template's that by gave it: a world of its own, kept as it was
// sent, to be unpacked into each server made from the template.
func (m *Manager) KeepFile(ctx context.Context, org, host, name, by string, r io.Reader) (*Snapshot, error) {
	if !m.Available() {
		return nil, ErrUnavailable
	}
	if err := m.ensure(ctx, org); err != nil {
		return nil, err
	}
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	args := []string{"--repo", m.repoDir(org), "--password-file", m.keyFile(org), "--no-cache",
		"backup", "--stdin", "--stdin-filename", name, "--host", host, "--json", "--tag", "avsys", "--tag", "trigger=" + TriggerTemplate}
	if by != "" {
		args = append(args, "--tag", "by="+by)
	}
	cmd := exec.CommandContext(ctx, m.Restic, args...)
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("restic backup: %w: %s", err, lastLine(stderr.String()))
	}
	var id string
	for _, line := range bytes.Split(out, []byte("\n")) {
		var msg struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.Type == "summary" {
			id = msg.ID
		}
	}
	if id == "" {
		return nil, errors.New("restic backup: no snapshot was saved")
	}
	m.touch(org)
	return m.get(ctx, org, id)
}

// Stream writes one file of a snapshot to w, as it is read out.
func (m *Manager) Stream(ctx context.Context, org, id, path string, w io.Writer) error {
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	snap, err := m.get(ctx, org, id)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, m.Restic, "--repo", m.repoDir(org), "--password-file", m.keyFile(org), "--no-cache", "dump", "--no-lock", snap.ID, path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "not found") {
			return ErrNotFound
		}
		return fmt.Errorf("restic dump: %w: %s", err, lastLine(stderr.String()))
	}
	return nil
}

// Delete removes one snapshot and the data only it referred to.
func (m *Manager) Delete(ctx context.Context, org, id string) error {
	return m.DeleteMany(ctx, org, []string{id})
}

// DeleteMany removes several snapshots, and the data only they referred to,
// in one pass over the repository: taking them one at a time would go
// through all of it once for each. Either all of them are there to be
// removed or none is.
func (m *Manager) DeleteMany(ctx context.Context, org string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	// Rejected ones too, which are only ever removed.
	list, err := m.listAll(ctx, org)
	if err != nil {
		return err
	}
	byID := make(map[string]*Snapshot, len(list))
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	args := []string{"forget", "--prune"}
	lineages := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		snap := byID[id]
		if snap == nil {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		args = append(args, snap.ID)
		lineages = append(lineages, snap.Lineage)
	}
	// Holding the lock alone means nothing the panel started is using the
	// repository, so any lock still in it was left by a snapshot that was cut
	// short, and would only make this fail.
	if _, err := m.restic(ctx, org, "unlock", "--remove-all"); err != nil {
		return err
	}
	// Noted first: once a snapshot is gone, so is the only record of which
	// copy offsite was its.
	if err := m.removing(org, lineages...); err != nil {
		return err
	}
	_, err = m.restic(ctx, org, args...)
	return err
}

// Forget removes the scheduled snapshots of one server that the policy no
// longer keeps, and returns how many that was. Snapshots taken any other way
// are not considered, and neither are other servers'. The space is not given
// back until Prune.
func (m *Manager) Forget(ctx context.Context, org, server string, keep Keep) (int, error) {
	args, err := forgetArgs(server, keep)
	if err != nil {
		return 0, err
	}
	if !m.exists(org) {
		return 0, nil
	}
	// A rejected snapshot still in the repository would be weighed with the
	// rest, and could be kept in place of a good one: nothing is let go of
	// until it is gone.
	m.removeRejected(ctx, org)
	if left, err := m.rejected(org); err != nil {
		return 0, err
	} else if len(left) > 0 {
		return 0, ErrRejectedLeft
	}
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	if _, err := m.restic(ctx, org, "unlock", "--remove-all"); err != nil {
		return 0, err
	}
	// Asked first what it would remove, so that it can be noted before it
	// is gone. Nothing else touches the repository in between: the lock is
	// held alone.
	going, err := m.forgetting(ctx, org, append(args, "--dry-run")...)
	if err != nil || len(going) == 0 {
		return 0, err
	}
	if err := m.removing(org, going...); err != nil {
		return 0, err
	}
	gone, err := m.forgetting(ctx, org, args...)
	return len(gone), err
}

// forgetArgs is the forget that applies a policy to one server's scheduled
// snapshots, and to nothing else.
func forgetArgs(server string, keep Keep) ([]string, error) {
	if !tagPattern.MatchString(server) {
		return nil, fmt.Errorf("%q cannot name a snapshot", server)
	}
	// With nothing to keep restic would be asked to forget all of them.
	if keep.Daily+keep.Weekly+keep.Monthly+keep.Yearly <= 0 {
		return nil, errors.New("a retention policy has to keep something")
	}
	args := []string{"forget", "--json", "--host", server, "--tag", "trigger=" + TriggerScheduled, "--group-by", "host"}
	for flag, n := range map[string]int{"--keep-daily": keep.Daily, "--keep-weekly": keep.Weekly, "--keep-monthly": keep.Monthly, "--keep-yearly": keep.Yearly} {
		if n > 0 {
			args = append(args, flag, fmt.Sprint(n))
		}
	}
	return args, nil
}

// Kept says, for each of a server's scheduled snapshots that the policy
// keeps, which of its rules keep it: "daily", "weekly", "monthly", "yearly".
// One that is not in the answer would be removed the next time the policy is
// applied. Nothing is changed.
func (m *Manager) Kept(ctx context.Context, org, server string, keep Keep) (map[string][]string, error) {
	args, err := forgetArgs(server, keep)
	if err != nil {
		return nil, err
	}
	kept := map[string][]string{}
	if !m.exists(org) {
		return kept, nil
	}
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()
	// Only asked what it would do, which needs no lock on the repository
	// and so does not wait for, or hold up, a snapshot being taken.
	out, err := m.restic(ctx, org, append(args, "--dry-run", "--no-lock")...)
	if err != nil {
		return nil, err
	}
	// restic prints nothing at all when no snapshot matched.
	if len(bytes.TrimSpace(out)) == 0 {
		return kept, nil
	}
	var groups []struct {
		Reasons []struct {
			Snapshot resticSnapshot `json:"snapshot"`
			Matches  []string       `json:"matches"`
		} `json:"reasons"`
	}
	if err := json.Unmarshal(out, &groups); err != nil {
		return nil, fmt.Errorf("reading what the policy keeps: %w", err)
	}
	for _, g := range groups {
		for _, r := range g.Reasons {
			rules := []string{}
			for _, match := range r.Matches {
				// "daily snapshot", "weekly snapshot", ...
				rules = append(rules, strings.TrimSuffix(match, " snapshot"))
			}
			kept[r.Snapshot.ID] = rules
		}
	}
	return kept, nil
}

// forgetting runs a forget and returns the lineages of what it removed, or
// with --dry-run would remove.
func (m *Manager) forgetting(ctx context.Context, org string, args ...string) ([]string, error) {
	out, err := m.restic(ctx, org, args...)
	if err != nil {
		return nil, err
	}
	var groups []struct {
		Remove []resticSnapshot `json:"remove"`
	}
	// restic prints nothing at all when no snapshot matched.
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(out, &groups); err != nil {
		return nil, fmt.Errorf("reading what was forgotten: %w", err)
	}
	var lineages []string
	for _, g := range groups {
		for _, r := range g.Remove {
			lineages = append(lineages, r.snapshot().Lineage)
		}
	}
	return lineages, nil
}

// Prune gives back the space of snapshots that have been forgotten.
func (m *Manager) Prune(ctx context.Context, org string) error {
	if !m.exists(org) {
		return nil
	}
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	if _, err := m.restic(ctx, org, "unlock", "--remove-all"); err != nil {
		return err
	}
	_, err := m.restic(ctx, org, "prune")
	return err
}

// Destroy removes an organization's repository and everything in it.
func (m *Manager) Destroy(org string) error {
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	if err := os.RemoveAll(m.repoDir(org)); err != nil {
		return err
	}
	if err := os.Remove(m.keyFile(org)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The offsite copy is left as it is: nothing here says to remove it.
	for _, file := range []string{m.removedFile(org), m.statusFile(org)} {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// serve makes the organization's repository reachable inside a workspace, on
// a socket that exists only for as long as the returned stop function has not
// been called. What is served can be read and added to, never deleted from.
func (m *Manager) serve(ws *ssh.Client, org string) (socket string, stop func(), err error) {
	handler, err := repo.New(m.repoDir(org), repo.Options{AppendOnly: true})
	if err != nil {
		return "", nil, err
	}
	name := make([]byte, 8)
	if _, err := rand.Read(name); err != nil {
		return "", nil, err
	}
	socket = fmt.Sprintf("%s/repository-%s.sock", socketDir, hex.EncodeToString(name))
	listener, err := ws.ListenUnix(socket)
	if err != nil {
		return "", nil, fmt.Errorf("opening a channel into the workspace: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return socket, func() {
		_ = server.Close()
		_ = listener.Close()
		// sshd leaves the socket file behind when the forward is cancelled.
		_, _ = run(ws, "rm -f "+quote(socket), nil, nil)
	}, nil
}

// run executes a command in the workspace, feeding it stdin and passing each
// line it prints to onLine. It returns what the command wrote to stderr.
func run(ws *ssh.Client, command string, stdin io.Reader, onLine func([]byte)) (string, error) {
	return runCtx(context.Background(), ws, command, stdin, onLine)
}

// CommandSession opens a session for one of the panel's own commands in a
// workspace. It is marked as the panel's, so the workspace runs it in a
// plain sh that reads nothing from the home directory.
func CommandSession(ws *ssh.Client) (*ssh.Session, error) {
	session, err := ws.NewSession()
	if err != nil {
		return nil, err
	}
	// A workspace from before the mark refuses it, and runs the command as
	// it always has.
	_ = session.Setenv("AVSYS_PANEL", "1")
	return session, nil
}

// resticIn builds the command line for restic inside a workspace. The
// password arrives on stdin so that it is in no argument list or file.
func resticIn(socket string, args ...string) string {
	parts := []string{
		"RESTIC_REPOSITORY=" + quote("rest:http+unix://"+socket+":/"),
		"RESTIC_PASSWORD_FILE=/dev/stdin",
		"restic",
	}
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}

// quote makes a string safe as one word of a shell command.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// message is one line of restic's --json output, across the commands used here.
type message struct {
	Type        string  `json:"message_type"`
	PercentDone float64 `json:"percent_done"`
	BytesDone   int64   `json:"bytes_done"`
	TotalBytes  int64   `json:"total_bytes"`
	// restore reports these instead.
	BytesRestored int64 `json:"bytes_restored"`

	SnapshotID string `json:"snapshot_id"`
	Message    string `json:"message"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item string `json:"item"`
}

// follow turns restic's JSON lines into progress reports and collects what
// is needed afterwards.
type follow struct {
	progress   func(Progress)
	snapshotID string
	// finished is set by restic's closing summary, which it prints whether
	// or not it saved a snapshot.
	finished bool
	errors   []string
}

func (f *follow) line(raw []byte) {
	var msg message
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	switch msg.Type {
	case "status":
		if f.progress != nil {
			done := msg.BytesDone
			if done == 0 {
				done = msg.BytesRestored
			}
			f.progress(Progress{Percent: msg.PercentDone * 100, Bytes: done, Total: msg.TotalBytes})
		}
	case "summary":
		f.snapshotID, f.finished = msg.SnapshotID, true
	case "error", "exit_error":
		text := msg.Message
		if msg.Error != nil && msg.Error.Message != "" {
			text = msg.Error.Message
		}
		if msg.Item != "" {
			text = msg.Item + ": " + text
		}
		if text != "" {
			f.errors = append(f.errors, text)
		}
	}
}

func (f *follow) failure(stderr string, err error) error {
	// 127 is the shell saying there is no such command.
	var exit *ssh.ExitError
	if errors.As(err, &exit) && exit.ExitStatus() == 127 {
		return ErrOldWorkspace
	}
	detail := lastLine(stderr)
	if len(f.errors) > 0 {
		detail = f.errors[len(f.errors)-1]
	}
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

// Snapshot saves the server's files as they are now. Whoever calls it is
// responsible for having the game stop writing first.
func (m *Manager) Snapshot(ctx context.Context, org string, ws *ssh.Client, meta Meta, progress func(Progress)) (*Snapshot, error) {
	tags := []string{"avsys", "trigger=" + meta.Trigger}
	for key, value := range map[string]string{"by": meta.By, "version": meta.Version, "loader": meta.Loader} {
		if value != "" && tagPattern.MatchString(value) {
			tags = append(tags, key+"="+value)
		}
	}
	sort.Strings(tags)
	if !tagPattern.MatchString(meta.Server) {
		return nil, fmt.Errorf("%q cannot name a snapshot", meta.Server)
	}
	if err := m.ensure(ctx, org); err != nil {
		return nil, err
	}
	secret, err := m.password(org)
	if err != nil {
		return nil, err
	}
	snap, incomplete, err := m.snapshot(ctx, org, ws, meta, tags, secret, progress)
	if err != nil {
		return nil, err
	}
	if incomplete != nil {
		// Not listed from here, whatever happens next: one whose removal
		// fails is not taken for a good one.
		if err := m.reject(org, snap.ID, false); err != nil {
			m.Log.Error("could not note an incomplete snapshot as rejected", "org", org, "server", meta.Server, "snapshot", snap.ID, "error", err)
		}
		// Removed with the lock released: removal needs it alone.
		if err := m.Delete(context.WithoutCancel(ctx), org, snap.ID); err != nil {
			m.Log.Error("could not remove an incomplete snapshot; it is not listed, and is removed after the next snapshot", "org", org, "server", meta.Server, "snapshot", snap.ID, "error", err)
		} else if err := m.reject(org, snap.ID, true); err != nil {
			m.Log.Warn("could not note a rejected snapshot as removed", "org", org, "snapshot", snap.ID, "error", err)
		}
		return nil, incomplete
	}
	m.touch(org)
	// One rejected before whose removal failed is tried again now that the
	// repository has been written to.
	m.removeRejected(context.WithoutCancel(ctx), org)
	return snap, nil
}

// snapshot runs the backup holding the repository's read lock. incomplete is
// set when restic saved a snapshot but could not read every file.
func (m *Manager) snapshot(ctx context.Context, org string, ws *ssh.Client, meta Meta, tags []string, secret string, progress func(Progress)) (snap *Snapshot, incomplete, err error) {
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()

	record, err := json.MarshalIndent(meta.Record, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	// Written only when it says something new, and compared outside the
	// server's directory. Writing it again unchanged, or so much as creating
	// a file next to it, would change a modification time, which is a change
	// to the files.
	fresh := quote(socketDir + "/record.new")
	write := "mkdir -p " + quote(socketDir) + " && cat > " + fresh +
		" && { cmp -s " + fresh + " " + quote(RecordPath) + " || { mkdir -p " + quote(filepath.Dir(RecordPath)) + " && cp " + fresh + " " + quote(RecordPath) + "; }; }" +
		" && rm -f " + fresh
	if stderr, err := run(ws, write, bytes.NewReader(record), nil); err != nil {
		return nil, nil, fmt.Errorf("recording the server's settings: %w: %s", err, lastLine(stderr))
	}

	socket, stop, err := m.serve(ws, org)
	if err != nil {
		return nil, nil, err
	}
	defer stop()

	args := []string{"backup", dataDir, "--json", "--host", meta.Server}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}
	for _, path := range append(exclusions(meta.Whole), notSaved...) {
		args = append(args, "--exclude", dataDir+path)
	}
	if meta.SkipUnchanged {
		args = append(args, "--skip-if-unchanged")
	}
	f := &follow{progress: progress}
	stderr, err := runCtx(ctx, ws, resticIn(socket, args...), strings.NewReader(secret+"\n"), f.line)
	// What went wrong with individual files is reported on stderr, in the
	// same form as the rest.
	for _, line := range strings.Split(stderr, "\n") {
		f.line([]byte(line))
	}
	// restic exits with 3 when it could not read some files, and saves a
	// snapshot of the rest all the same.
	var exit *ssh.ExitError
	partial := errors.As(err, &exit) && exit.ExitStatus() == 3 && f.snapshotID != ""
	if err != nil && !partial {
		return nil, nil, f.failure(stderr, err)
	}
	if f.snapshotID == "" && f.finished && meta.SkipUnchanged && err == nil {
		return nil, nil, ErrUnchanged
	}
	if f.snapshotID == "" {
		return nil, nil, errors.New("restic finished without reporting a snapshot")
	}
	snap, err = m.get(ctx, org, f.snapshotID)
	if err != nil {
		return nil, nil, err
	}
	if partial {
		detail := lastLine(stderr)
		if len(f.errors) > 0 {
			detail = strings.Join(f.errors, "; ")
		}
		incomplete = fmt.Errorf("%w: %s", ErrIncomplete, detail)
	}
	return snap, incomplete, nil
}

// Restore puts the server's files back to how a snapshot has them. Files
// that are not in the snapshot are removed, except the workspace's own. The
// game must not be running.
func (m *Manager) Restore(ctx context.Context, org string, ws *ssh.Client, id string, progress func(Progress)) error {
	return m.restore(ctx, org, ws, id, false, nil, nil, progress)
}

// RestoreWithout is Restore leaving out paths, relative to the server
// directory, and then putting back only, from among those, what is in them:
// a world left out, and the datapacks in it kept.
func (m *Manager) RestoreWithout(ctx context.Context, org string, ws *ssh.Client, id string, leave, only []string, progress func(Progress)) error {
	if err := m.restore(ctx, org, ws, id, false, leave, nil, progress); err != nil {
		return err
	}
	if len(only) == 0 {
		return nil
	}
	return m.restore(ctx, org, ws, id, false, nil, only, nil)
}

// RestoreWhole puts back a whole snapshot, the workspace's home with it, onto
// a server's new, empty volume.
func (m *Manager) RestoreWhole(ctx context.Context, org string, ws *ssh.Client, id string, progress func(Progress)) error {
	return m.restore(ctx, org, ws, id, true, nil, nil, progress)
}

// restore puts back a snapshot, less what leave names. With only, it puts
// back that and nothing else, and leaves the rest as it is.
func (m *Manager) restore(ctx context.Context, org string, ws *ssh.Client, id string, whole bool, leave, only []string, progress func(Progress)) error {
	snap, err := m.Get(ctx, org, id)
	if err != nil {
		return err
	}
	secret, err := m.password(org)
	if err != nil {
		return err
	}
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()

	socket, stop, err := m.serve(ws, org)
	if err != nil {
		return err
	}
	defer stop()

	// The snapshot is addressed from its server directory and restored onto
	// that directory. Restoring the whole snapshot onto / with --delete would
	// remove everything outside the server directory as "not in the snapshot".
	args := []string{"restore", snap.ID + ":" + dataDir, "--target", dataDir, "--json"}
	if len(only) > 0 {
		// restic takes includes or excludes, not both: what is included is
		// never among what is excluded anyway.
		for _, path := range only {
			args = append(args, "--include", path)
		}
	} else {
		args = append(args, "--delete")
		for _, path := range append(exclusions(whole), leave...) {
			args = append(args, "--exclude", path)
		}
	}
	if progress == nil {
		progress = func(Progress) {}
	}
	f := &follow{progress: progress}
	if stderr, err := runCtx(ctx, ws, resticIn(socket, args...), strings.NewReader(secret+"\n"), f.line); err != nil {
		return f.failure(stderr, err)
	}
	return nil
}

// runCtx is run, with the command stopped when ctx ends.
func runCtx(ctx context.Context, ws *ssh.Client, command string, stdin io.Reader, onLine func([]byte)) (string, error) {
	type result struct {
		stderr string
		err    error
	}
	done := make(chan result, 1)
	session, err := CommandSession(ws)
	if err != nil {
		return "", err
	}
	go func() {
		defer session.Close()
		session.Stdin = stdin
		var stderr bytes.Buffer
		session.Stderr = &stderr
		stdout, err := session.StdoutPipe()
		if err != nil {
			done <- result{"", err}
			return
		}
		if err := session.Start(command); err != nil {
			done <- result{"", err}
			return
		}
		lines := bufio.NewScanner(stdout)
		lines.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for lines.Scan() {
			if onLine != nil {
				onLine(lines.Bytes())
			}
		}
		done <- result{stderr.String(), session.Wait()}
	}()
	select {
	case r := <-done:
		return r.stderr, r.err
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		return "", ctx.Err()
	}
}
