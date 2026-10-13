package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Offsite is a second place every organization's snapshots are copied to, so
// that losing the panel's own storage does not lose them.
//
// It is the install's, set by whoever runs the panel, not something an
// organization chooses: it decides where the panel connects to and with what
// credentials. Those are the panel's environment, which restic reads for
// itself (AWS_ACCESS_KEY_ID, B2_ACCOUNT_KEY and so on).
type Offsite struct {
	// Repository is a restic repository location, such as
	// s3:https://s3.example.com/bucket/avsys. Each organization gets its own
	// repository under it: "{org}" in the location is replaced by the
	// organization's name, and without one the name is added as a last path
	// element.
	Repository string
	// Password opens the offsite repositories. It is for the operator to
	// keep somewhere else: when the panel's storage is gone, so are the
	// passwords it keeps beside its own repositories.
	Password string
}

// ErrNoOffsite is returned when no offsite repository has been set up.
var ErrNoOffsite = errors.New("no offsite copy is set up")

// offsiteListTimeout bounds asking the far end what it holds.
const offsiteListTimeout = 2 * time.Minute

// exitNoRepository is what restic exits with when there is no repository at
// the location it was given.
const exitNoRepository = 10

// HasOffsite reports whether snapshots are copied anywhere else.
func (m *Manager) HasOffsite() bool {
	return m.Offsite != nil && m.Offsite.Repository != "" && m.Offsite.Password != ""
}

func (m *Manager) offsiteRepo(org string) string {
	base := m.Offsite.Repository
	if strings.Contains(base, "{org}") {
		return strings.ReplaceAll(base, "{org}", org)
	}
	return strings.TrimRight(base, "/") + "/" + org
}

func (m *Manager) removedFile(org string) string { return filepath.Join(m.Dir, org+".removed") }
func (m *Manager) statusFile(org string) string  { return filepath.Join(m.Dir, org+".offsite") }

// HasRepository reports whether an organization has a repository here.
func (m *Manager) HasRepository(org string) bool { return m.exists(org) }

// Organizations names the organizations that have a repository here.
func (m *Manager) Organizations() []string {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && m.exists(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// OffsiteStatus is how an organization's offsite copy stands.
type OffsiteStatus struct {
	// LastAttempt is when a sync last began, and LastSuccess when the last
	// one that went through began: everything that was here by then is there.
	LastAttempt *time.Time `json:"lastAttempt,omitempty"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	// Error is why the last attempt failed, empty if it did not.
	Error string `json:"error,omitempty"`
	// Snapshots is how many the offsite repository held after the last sync
	// that went through.
	Snapshots int `json:"snapshots"`
}

// OffsiteStatus reads how an organization's offsite copy stands. It is kept
// beside the repository, and is empty before the first attempt.
func (m *Manager) OffsiteStatus(org string) OffsiteStatus {
	var st OffsiteStatus
	if data, err := os.ReadFile(m.statusFile(org)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func (m *Manager) noteOffsite(org string, began time.Time, res OffsiteResult, failure error) {
	st := m.OffsiteStatus(org)
	st.LastAttempt, st.Error = &began, ""
	if failure != nil {
		st.Error = offsiteReason(failure)
	} else {
		st.LastSuccess, st.Snapshots = &began, res.Snapshots
	}
	data, err := json.Marshal(st)
	if err == nil {
		err = os.WriteFile(m.statusFile(org), data, 0o600)
	}
	if err != nil {
		m.Log.Error("could not note how the offsite copy stands", "org", org, "error", err)
	}
}

// exitWrongPassword is what restic exits with when the password does not
// open the repository.
const exitWrongPassword = 12

// offsiteReason says why a sync failed in a form fit to show. restic's own
// last line is kept: it is what names an unreachable host or a refused key.
func offsiteReason(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == exitWrongPassword {
		return "The offsite password does not open the repository that is there."
	}
	return err.Error()
}

// touch notes that an organization's snapshots have just changed.
func (m *Manager) touch(org string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.changed == nil {
		m.changed = map[string]time.Time{}
	}
	m.changed[org] = time.Now()
}

// Changed is when an organization's snapshots last changed, as far as this
// process has seen: zero if they have not since it started.
func (m *Manager) Changed(org string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changed[org]
}

// removing notes snapshots that are about to be removed here on purpose, so
// that their copies offsite are removed with the next sync. Only what is
// noted is ever removed there: a repository that is simply missing
// snapshots, because it is new or was lost, takes nothing with it.
//
// It is called before the removal and fails it if the note cannot be kept:
// afterwards nothing would say which copy to remove. A note for a snapshot
// that then was not removed after all does no harm; the sync finds it still
// here and drops the note.
func (m *Manager) removing(org string, lineages ...string) error {
	if len(lineages) == 0 {
		return nil
	}
	m.touch(org)
	if !m.HasOffsite() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.OpenFile(m.removedFile(org), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.WriteString(strings.Join(lineages, "\n") + "\n")
		if serr := f.Sync(); err == nil {
			err = serr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return fmt.Errorf("nothing was removed: its offsite copy could not be marked for removal: %w", err)
	}
	return nil
}

// pendingRemovals reads what removing has noted.
func (m *Manager) pendingRemovals(org string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.removedFile(org))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range strings.Fields(string(data)) {
		out[line] = true
	}
	return out, nil
}

// settleRemovals drops the notes that have been acted on, and keeps any
// made since they were read.
func (m *Manager) settleRemovals(org string, done map[string]bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.removedFile(org))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var left []string
	for _, line := range strings.Fields(string(data)) {
		if !done[line] {
			left = append(left, line)
		}
	}
	if len(left) == 0 {
		return os.Remove(m.removedFile(org))
	}
	return os.WriteFile(m.removedFile(org), []byte(strings.Join(left, "\n")+"\n"), 0o600)
}

// offsite runs the panel's restic against an organization's offsite
// repository. Its password travels in the environment, not on the command
// line, where other processes could read it.
func (m *Manager) offsite(ctx context.Context, org string, env []string, args ...string) ([]byte, error) {
	if !m.Available() {
		return nil, ErrUnavailable
	}
	if !m.HasOffsite() {
		return nil, ErrNoOffsite
	}
	// Unlike the panel's own repositories this one is far away, and a cache
	// of its index saves fetching it every time.
	full := append([]string{"--repo", m.offsiteRepo(org), "--cache-dir", filepath.Join(m.Dir, ".cache")}, args...)
	cmd := exec.CommandContext(ctx, m.Restic, full...)
	cmd.Env = append(append(os.Environ(), "RESTIC_PASSWORD="+m.Offsite.Password), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		why := retrying.ReplaceAllString(lastLine(stderr.String()), "")
		if ctx.Err() != nil {
			// Cut short while restic was still trying: what it was last told
			// is the reason.
			return out, fmt.Errorf("the offsite repository gave no usable answer in time: %s", why)
		}
		return out, fmt.Errorf("restic %s (offsite): %w: %s", args[0], err, why)
	}
	return out, nil
}

// retrying matches how restic introduces a failure it is about to try
// again, ahead of the failure itself.
var retrying = regexp.MustCompile(`^.* returned error, retrying after [^:]+: `)

// noRepository reports whether restic failed because there is no repository
// where it looked. Any other failure says nothing about what is there.
func noRepository(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == exitNoRepository
}

// offsiteList lists an organization's offsite repository. exists is false,
// with no error, when there is none.
func (m *Manager) offsiteList(ctx context.Context, org string) (list []Snapshot, exists bool, err error) {
	// Listing is quick when the far end answers at all. When it does not,
	// or answers that the bucket is not there, restic goes on trying for a
	// quarter of an hour; this says so sooner, with the reason it was given.
	ctx, cancel := context.WithTimeout(ctx, offsiteListTimeout)
	defer cancel()
	out, err := m.offsite(ctx, org, nil, "snapshots", "--json", "--no-lock")
	if noRepository(err) {
		return []Snapshot{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var raw []resticSnapshot
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, true, fmt.Errorf("reading the offsite snapshot list: %w", err)
	}
	list = make([]Snapshot, 0, len(raw))
	for _, r := range raw {
		list = append(list, r.snapshot())
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time.After(list[j].Time) })
	return list, true, nil
}

// OffsiteSnapshots lists what the offsite repository holds of an
// organization, newest first. It is empty when there is no repository.
func (m *Manager) OffsiteSnapshots(ctx context.Context, org string) ([]Snapshot, error) {
	list, _, err := m.offsiteList(ctx, org)
	return list, err
}

// OffsiteResult is what one sync did.
type OffsiteResult struct {
	// Copied is how many snapshots were not offsite yet and now are.
	Copied int `json:"copied"`
	// Removed is how many were removed offsite because they were removed here.
	Removed int `json:"removed"`
	// Snapshots is how many the offsite repository holds afterwards.
	Snapshots int `json:"snapshots"`
}

// SyncOffsite brings the offsite repository up to date with the panel's own:
// every snapshot that is here and not there is copied, and the copy of every
// snapshot that was deliberately removed here is removed there. Running it
// again when nothing has changed does nothing.
//
// Nothing is removed offsite merely for being absent here. A panel that has
// lost its repository, or was installed afresh, leaves the offsite one whole.
func (m *Manager) SyncOffsite(ctx context.Context, org string) (OffsiteResult, error) {
	if !m.HasOffsite() {
		return OffsiteResult{}, ErrNoOffsite
	}
	if !m.exists(org) {
		return OffsiteResult{}, nil
	}
	began := time.Now()
	res, err := m.syncOffsite(ctx, org)
	m.noteOffsite(org, began, res, err)
	return res, err
}

func (m *Manager) syncOffsite(ctx context.Context, org string) (OffsiteResult, error) {
	var res OffsiteResult
	// Shared with snapshots, and kept from anything that removes data here:
	// that would take away what is being read.
	lock := m.lock(org)
	lock.RLock()
	defer lock.RUnlock()

	before, exists, err := m.offsiteList(ctx, org)
	if err != nil {
		return res, err
	}
	from := []string{"--from-repo", m.repoDir(org), "--from-password-file", m.keyFile(org)}
	if !exists {
		// With the same chunking as the repository here, what is copied is
		// deduplicated there the way it is here.
		if _, err := m.offsite(ctx, org, nil, append([]string{"init", "--copy-chunker-params"}, from...)...); err != nil {
			return res, err
		}
		m.Log.Info("created offsite repository", "org", org)
	}
	if _, err := m.offsite(ctx, org, nil, append([]string{"copy"}, from...)...); err != nil {
		return res, err
	}
	after, _, err := m.offsiteList(ctx, org)
	if err != nil {
		return res, err
	}
	res.Copied, res.Snapshots = len(after)-len(before), len(after)

	gone, err := m.pendingRemovals(org)
	if err != nil {
		return res, err
	}
	var ids []string
	if len(gone) > 0 {
		// A note is made before its removal, which may then not have gone
		// through. What is still here keeps its copy.
		here, err := m.list(ctx, org)
		if err != nil {
			return res, err
		}
		still := map[string]bool{}
		for _, s := range here {
			still[s.Lineage] = true
		}
		for _, s := range after {
			if gone[s.Lineage] && !still[s.Lineage] {
				ids = append(ids, s.ID)
			}
		}
	}
	if len(ids) > 0 {
		if _, err := m.offsite(ctx, org, nil, append([]string{"forget", "--prune"}, ids...)...); err != nil {
			return res, err
		}
		res.Removed, res.Snapshots = len(ids), len(after)-len(ids)
	}
	if err := m.settleRemovals(org, gone); err != nil {
		return res, err
	}
	return res, nil
}

// ErrNotEmpty is returned when snapshots would be brought back into an
// organization that has some.
var ErrNotEmpty = errors.New("this organization has snapshots here already")

// Recoverable lists what could be brought back from the offsite repository:
// what it holds of an organization, less the copies of snapshots that were
// removed here on purpose and are only waiting to be removed there.
func (m *Manager) Recoverable(ctx context.Context, org string) ([]Snapshot, error) {
	there, _, err := m.offsiteList(ctx, org)
	if err != nil {
		return nil, err
	}
	gone, err := m.pendingRemovals(org)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(there))
	for _, s := range there {
		if !gone[s.Lineage] {
			out = append(out, s)
		}
	}
	return out, nil
}

// FetchOffsite copies an organization's snapshots back from the offsite
// repository into the panel's own, which is made if it is not there. It is
// how an organization gets its history back after the panel's storage was
// lost. It returns how many snapshots were brought back.
//
// It only fills an organization that has none, which is checked with the
// repository held: one taken in the meantime is not mixed with what comes
// back. And it leaves behind what was removed here on purpose.
func (m *Manager) FetchOffsite(ctx context.Context, org string) (int, error) {
	if !m.HasOffsite() {
		return 0, ErrNoOffsite
	}
	began := time.Now()
	there, _, err := m.offsiteList(ctx, org)
	if err != nil {
		return 0, err
	}
	back, err := m.Recoverable(ctx, org)
	if err != nil {
		return 0, err
	}
	lock := m.lock(org)
	lock.Lock()
	defer lock.Unlock()
	if m.exists(org) {
		here, err := m.list(ctx, org)
		if err != nil {
			return 0, err
		}
		if len(here) > 0 {
			return 0, ErrNotEmpty
		}
	}
	if len(back) == 0 {
		return 0, nil
	}

	from := []string{"--from-repo", m.offsiteRepo(org)}
	env := []string{"RESTIC_FROM_PASSWORD=" + m.Offsite.Password}
	if !m.exists(org) {
		if err := os.MkdirAll(m.repoDir(org), 0o700); err != nil {
			return 0, err
		}
		if _, err := m.resticEnv(ctx, org, env, append([]string{"init", "--copy-chunker-params"}, from...)...); err != nil {
			return 0, err
		}
	}
	ids := make([]string, len(back))
	for i, s := range back {
		ids[i] = s.ID
	}
	if _, err := m.resticEnv(ctx, org, env, append(append([]string{"copy"}, from...), ids...)...); err != nil {
		return 0, err
	}
	after, err := m.list(ctx, org)
	if err != nil {
		return 0, err
	}
	if len(back) == len(there) {
		// Everything here has just come from there, so the two are level.
		m.noteOffsite(org, began, OffsiteResult{Snapshots: len(there)}, nil)
	} else {
		// What was left behind is still to be removed there.
		m.touch(org)
	}
	return len(after), nil
}
