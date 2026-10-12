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
	if removed, err := m.Forget(ctx, "alpha", "world", Keep{Daily: 1}); err != nil || removed != 1 {
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
	m.removed("alpha", "abc")
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
