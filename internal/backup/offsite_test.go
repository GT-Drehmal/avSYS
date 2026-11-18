package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOffsiteRepositoryPerOrganization(t *testing.T) {
	for location, want := range map[string]string{
		"s3:https://s3.example.com/bucket/avsys":  "s3:https://s3.example.com/bucket/avsys/alpha",
		"s3:https://s3.example.com/bucket/avsys/": "s3:https://s3.example.com/bucket/avsys/alpha",
		"b2:bucket:avsys/{org}":                   "b2:bucket:avsys/alpha",
		"rest:https://backup.example.com/{org}/":  "rest:https://backup.example.com/alpha/",
		"/mnt/offsite":                            "/mnt/offsite/alpha",
	} {
		m := &Manager{Offsite: &Offsite{Repository: location, Password: "x"}}
		if got := m.offsiteRepo("alpha"); got != want {
			t.Errorf("%s for alpha: %s, want %s", location, got, want)
		}
	}
	// Without a password there would be no opening what was copied.
	if (&Manager{Offsite: &Offsite{Repository: "/mnt/offsite"}}).HasOffsite() {
		t.Error("an offsite repository with no password counts as set up")
	}
}

// offsiteFixture is a manager with a directory standing in for the far end,
// and a way to take snapshots without a workspace.
func offsiteFixture(t *testing.T) (m *Manager, take func(server, trigger, content string), names func(list []Snapshot, err error) string) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	m = &Manager{
		Dir: t.TempDir(), Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Offsite: &Offsite{Repository: filepath.Join(t.TempDir(), "far"), Password: "kept-by-the-operator"},
	}
	files := t.TempDir()
	n := 0
	take = func(server, trigger, content string) {
		t.Helper()
		if err := m.ensure(context.Background(), "alpha"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(files, "level.dat"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		n++
		when := time.Date(2026, 9, n, 4, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")
		out, err := exec.Command("restic", "--repo", m.repoDir("alpha"), "--password-file", m.keyFile("alpha"),
			"backup", files, "--host", server, "--tag", "avsys", "--tag", "trigger="+trigger, "--time", when).CombinedOutput()
		if err != nil {
			t.Fatalf("taking a snapshot: %v: %s", err, out)
		}
	}
	names = func(list []Snapshot, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range list {
			out = append(out, s.Server+" "+s.Trigger+" "+s.Time.UTC().Format("01-02"))
		}
		return strings.Join(out, ", ")
	}
	return m, take, names
}

// Everything here is copied there; only what was deliberately removed here
// is removed there.
func TestSyncOffsite(t *testing.T) {
	m, take, names := offsiteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// An organization with nothing yet is nothing to do, and makes nothing.
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{}) {
		t.Fatalf("sync with no repository here: %+v, %v", res, err)
	}
	if list, err := m.OffsiteSnapshots(ctx, "alpha"); err != nil || len(list) != 0 {
		t.Fatalf("offsite before anything was copied: %v, %v", list, err)
	}

	take("world", TriggerManual, "one")
	take("world", TriggerScheduled, "two")
	take("other", TriggerScheduled, "three")
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{Copied: 3, Snapshots: 3}) {
		t.Fatalf("the first sync: %+v, %v; want 3 copied", res, err)
	}
	if got, want := names(m.OffsiteSnapshots(ctx, "alpha")), names(m.List(ctx, "alpha")); got != want {
		t.Errorf("offsite holds %q, here is %q", got, want)
	}
	// The far end opens with the operator's password, not the one kept here.
	if out, err := exec.Command("restic", "--repo", m.offsiteRepo("alpha"), "--password-file", m.keyFile("alpha"), "snapshots").CombinedOutput(); err == nil {
		t.Errorf("the offsite repository opened with the local password: %s", out)
	}
	// Again with nothing new: nothing happens.
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{Snapshots: 3}) {
		t.Errorf("a second sync: %+v, %v; want nothing done", res, err)
	}

	// One more here, and one deleted here.
	take("world", TriggerScheduled, "four")
	list, err := m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "alpha", list[len(list)-1].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{Copied: 1, Removed: 1, Snapshots: 3}) {
		t.Fatalf("after one more and one deleted: %+v, %v; want 1 copied, 1 removed", res, err)
	}
	if got, want := names(m.OffsiteSnapshots(ctx, "alpha")), names(m.List(ctx, "alpha")); got != want {
		t.Errorf("offsite holds %q, here is %q", got, want)
	}

	// Retention here is carried out there too.
	if removed, err := m.Forget(ctx, "alpha", "world", Keep{Daily: 1}, nil); err != nil || removed != 1 {
		t.Fatalf("forget: %d, %v; want 1", removed, err)
	}
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res.Removed != 1 || res.Snapshots != 2 {
		t.Fatalf("after retention: %+v, %v; want 1 removed, 2 left", res, err)
	}
	if _, err := os.Stat(m.removedFile("alpha")); !os.IsNotExist(err) {
		t.Errorf("what was removed is still noted after being acted on: %v", err)
	}
	if !m.Changed("alpha").After(time.Time{}) || !m.Changed("beta").IsZero() {
		t.Errorf("changes noted: alpha %v, beta %v", m.Changed("alpha"), m.Changed("beta"))
	}
}

// The repository here being lost, or new, removes nothing offsite, and what
// is offsite can be brought back whole.
func TestOffsiteSurvivesLosingTheRepositoryHere(t *testing.T) {
	m, take, names := offsiteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	take("world", TriggerManual, "one")
	take("world", TriggerScheduled, "two")
	if _, err := m.SyncOffsite(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	before := names(m.List(ctx, "alpha"))

	// The organization is deleted, or the disk is: either way it is gone here.
	if err := m.Destroy("alpha"); err != nil {
		t.Fatal(err)
	}
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{}) {
		t.Errorf("sync with the repository here gone: %+v, %v", res, err)
	}
	// A new one in its place, with one snapshot of its own, still takes
	// nothing away from what was there.
	take("fresh", TriggerManual, "new")
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{Copied: 1, Snapshots: 3}) {
		t.Fatalf("sync from a new repository: %+v, %v; want 1 copied and the 2 old ones kept", res, err)
	}

	// Lost again, and brought back.
	if err := m.Destroy("alpha"); err != nil {
		t.Fatal(err)
	}
	fetched, err := m.FetchOffsite(ctx, "alpha")
	if err != nil || fetched != 3 {
		t.Fatalf("fetch: %d, %v; want 3", fetched, err)
	}
	list, err := m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(list, nil); !strings.Contains(got, before) || len(list) != 3 {
		t.Errorf("after the fetch here is %q, want it to include %q", got, before)
	}
	for _, s := range list {
		if s.Size == 0 || s.Files == 0 {
			t.Errorf("a snapshot came back without its size: %+v", s)
		}
	}
	if st := m.OffsiteStatus("alpha"); st.LastSuccess == nil || st.Snapshots != 3 || st.Error != "" {
		t.Errorf("after the fetch the copy stands as %+v, want level with 3 snapshots", st)
	}
	// What came back is what is there: nothing to copy, nothing doubled.
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res != (OffsiteResult{Snapshots: 3}) {
		t.Errorf("sync after the fetch: %+v, %v; want nothing done", res, err)
	}
	// And it is restic's own to read: a file comes out of it.
	if out, err := m.restic(ctx, "alpha", "ls", list[0].ID); err != nil || !strings.Contains(string(out), "level.dat") {
		t.Errorf("listing a fetched snapshot: %v: %s", err, out)
	}
	// Fetching with nothing offsite is nothing to do.
	if n, err := m.FetchOffsite(ctx, "beta"); err != nil || n != 0 {
		t.Errorf("fetch for an organization with nothing offsite: %d, %v", n, err)
	}
}

func TestOffsiteNeedsSettingUp(t *testing.T) {
	m := &Manager{Dir: t.TempDir(), Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := m.SyncOffsite(context.Background(), "alpha"); err != ErrNoOffsite {
		t.Errorf("sync with no offsite: %v", err)
	}
	if _, err := m.FetchOffsite(context.Background(), "alpha"); err != ErrNoOffsite {
		t.Errorf("fetch with no offsite: %v", err)
	}
	// Nothing is noted for an offsite copy that does not exist.
	if err := m.removing("alpha", "abc"); err != nil {
		t.Errorf("noting a removal with no offsite copy: %v", err)
	}
	if _, err := os.Stat(m.removedFile("alpha")); !os.IsNotExist(err) {
		t.Errorf("a removal was noted with no offsite copy to act on it: %v", err)
	}
}

// A wrong password or an unreachable place is an error, never "nothing there".
func TestOffsiteFailuresAreNotEmptiness(t *testing.T) {
	m, take, _ := offsiteFixture(t)
	ctx := context.Background()
	take("world", TriggerManual, "one")
	if _, err := m.SyncOffsite(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	m.Offsite.Password = "not-it"
	if _, err := m.OffsiteSnapshots(ctx, "alpha"); err == nil {
		t.Error("listing offsite with the wrong password did not fail")
	}
	if _, err := m.SyncOffsite(ctx, "alpha"); err == nil {
		t.Error("syncing with the wrong password did not fail")
	}
	if _, err := m.FetchOffsite(ctx, "alpha"); err == nil {
		t.Error("fetching with the wrong password did not fail")
	}
}

// A removal is noted before it is made, and one that cannot be noted is not
// made: afterwards nothing would say which copy to remove.
func TestARemovalThatCannotBeNotedIsNotMade(t *testing.T) {
	m, take, _ := offsiteFixture(t)
	ctx := context.Background()
	take("world", TriggerManual, "one")
	take("world", TriggerScheduled, "two")
	take("world", TriggerScheduled, "three")
	if _, err := m.SyncOffsite(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	// Something in the way of the note: a directory where the file goes.
	if err := os.Mkdir(m.removedFile("alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "alpha", list[0].ID); err == nil {
		t.Error("a snapshot was deleted though its removal could not be noted")
	}
	if removed, err := m.Forget(ctx, "alpha", "world", Keep{Daily: 1}, nil); err == nil || removed != 0 {
		t.Errorf("retention went ahead though its removals could not be noted: %d, %v", removed, err)
	}
	if after, err := m.List(ctx, "alpha"); err != nil || len(after) != 3 {
		t.Errorf("after two refused removals there are %d snapshots here, %v; want all 3", len(after), err)
	}
	if err := os.Remove(m.removedFile("alpha")); err != nil {
		t.Fatal(err)
	}

	// A note for a snapshot that is still here, as after a removal that
	// then failed, does not cost it its copy.
	if err := m.removing("alpha", list[0].Lineage); err != nil {
		t.Fatal(err)
	}
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res.Removed != 0 || res.Snapshots != 3 {
		t.Errorf("sync with a note for a snapshot still here: %+v, %v; want nothing removed", res, err)
	}
	if _, err := os.Stat(m.removedFile("alpha")); !os.IsNotExist(err) {
		t.Errorf("the void note was kept: %v", err)
	}
}

// What comes back is only what was not removed here on purpose, and only
// into an organization that has nothing.
func TestFetchLeavesBehindWhatWasRemoved(t *testing.T) {
	m, take, names := offsiteFixture(t)
	ctx := context.Background()
	take("world", TriggerManual, "one")
	take("world", TriggerManual, "two")
	if _, err := m.SyncOffsite(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	// Not into one that has snapshots.
	if n, err := m.FetchOffsite(ctx, "alpha"); err != ErrNotEmpty || n != 0 {
		t.Errorf("fetch into an organization with snapshots: %d, %v; want ErrNotEmpty", n, err)
	}
	// Both deleted here, and not yet removed there.
	list, err := m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if err := m.Delete(ctx, "alpha", s.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := names(m.OffsiteSnapshots(ctx, "alpha")); !strings.Contains(got, "world") {
		t.Fatalf("the copies were removed before any sync: %q", got)
	}
	if back, err := m.Recoverable(ctx, "alpha"); err != nil || len(back) != 0 {
		t.Errorf("what could be brought back after deleting everything: %v, %v; want nothing", back, err)
	}
	if n, err := m.FetchOffsite(ctx, "alpha"); err != nil || n != 0 {
		t.Errorf("fetch after deleting everything: %d, %v; want nothing brought back", n, err)
	}
	if here, err := m.List(ctx, "alpha"); err != nil || len(here) != 0 {
		t.Errorf("deleted snapshots came back: %v, %v", here, err)
	}
	// And the next sync removes them there.
	if res, err := m.SyncOffsite(ctx, "alpha"); err != nil || res.Removed != 2 || res.Snapshots != 0 {
		t.Errorf("sync after deleting everything: %+v, %v; want 2 removed", res, err)
	}
}

// Each snapshot says whether it has a copy offsite, as of the last sync,
// without the far end being asked when snapshots are listed.
func TestSnapshotsSayWhetherTheyAreOffsite(t *testing.T) {
	m, take, _ := offsiteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Each snapshot here, oldest first, and whether it is offsite.
	offsite := func(m *Manager) string {
		t.Helper()
		list, err := m.List(ctx, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(list) - 1; i >= 0; i-- {
			s, mark := list[i], "here"
			if s.Offsite {
				mark = "offsite"
			}
			out = append(out, s.Server+" "+s.Trigger+" "+mark)
		}
		return strings.Join(out, ", ")
	}
	sync := func() {
		t.Helper()
		if _, err := m.SyncOffsite(ctx, "alpha"); err != nil {
			t.Fatal(err)
		}
	}

	take("world", TriggerManual, "one")
	take("world", TriggerScheduled, "two")
	if got, want := offsite(m), "world manual here, world scheduled here"; got != want {
		t.Errorf("before any sync: %q, want %q", got, want)
	}
	sync()
	if got, want := offsite(m), "world manual offsite, world scheduled offsite"; got != want {
		t.Errorf("after a sync: %q, want %q", got, want)
	}

	// One taken since is here only until the next sync.
	take("other", TriggerScheduled, "three")
	if got, want := offsite(m), "world manual offsite, world scheduled offsite, other scheduled here"; got != want {
		t.Errorf("with one taken since the sync: %q, want %q", got, want)
	}
	// One removed since leaves the others as they were.
	list, err := m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "alpha", list[len(list)-1].ID); err != nil {
		t.Fatal(err)
	}
	if got, want := offsite(m), "world scheduled offsite, other scheduled here"; got != want {
		t.Errorf("with one removed since the sync: %q, want %q", got, want)
	}
	sync()
	if got, want := offsite(m), "world scheduled offsite, other scheduled offsite"; got != want {
		t.Errorf("after the next sync: %q, want %q", got, want)
	}
	// The copy of the one removed is gone there, and is not counted.
	if copied := m.offsiteCopies("alpha"); len(copied) != 2 {
		t.Errorf("%d snapshots noted as offsite, want 2", len(copied))
	}

	// A template version made of a snapshot is a snapshot of its own, not
	// offsite until it is copied.
	list, err = m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	made, err := m.CopyAs(ctx, "alpha", list[0].ID, "template-t1", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if made.Offsite {
		t.Error("a template version made since the sync is offsite")
	}
	if got, want := offsite(m), "world scheduled offsite, other scheduled offsite, template-t1 template here"; got != want {
		t.Errorf("with a template version made since the sync: %q, want %q", got, want)
	}
	sync()
	if got, want := offsite(m), "world scheduled offsite, other scheduled offsite, template-t1 template offsite"; got != want {
		t.Errorf("after copying the template version: %q, want %q", got, want)
	}

	// It is kept beside the repository: a panel started again knows it.
	again := &Manager{Dir: m.Dir, Restic: m.Restic, Log: m.Log, Offsite: &Offsite{Repository: m.Offsite.Repository, Password: m.Offsite.Password}}
	if got, want := offsite(again), offsite(m); got != want {
		t.Errorf("after a restart: %q, want %q", got, want)
	}
	// Another offsite repository has none of them until it is synced.
	again.Offsite.Repository = filepath.Join(t.TempDir(), "elsewhere")
	if got := offsite(again); strings.Contains(got, "offsite") {
		t.Errorf("with the offsite copy moved: %q, want none offsite", got)
	}
	// And with no offsite copy at all, none is.
	again.Offsite = nil
	if got := offsite(again); strings.Contains(got, "offsite") {
		t.Errorf("with no offsite copy: %q, want none offsite", got)
	}

	// Lost here and brought back: all of it is offsite.
	if err := m.Destroy("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.copiesFile("alpha")); !os.IsNotExist(err) {
		t.Errorf("what was offsite is still noted for an organization that is gone: %v", err)
	}
	if n, err := m.FetchOffsite(ctx, "alpha"); err != nil || n != 3 {
		t.Fatalf("fetch: %d, %v; want 3", n, err)
	}
	if got, want := offsite(m), "world scheduled offsite, other scheduled offsite, template-t1 template offsite"; got != want {
		t.Errorf("after bringing them back: %q, want %q", got, want)
	}
}
