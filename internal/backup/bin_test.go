package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// binRepo is a repository with the panel's own restic, and a way to put
// snapshots in it as the panel's servers would, dated as asked.
type binRepo struct {
	t     *testing.T
	m     *Manager
	files string
	n     int
}

func newBinRepo(t *testing.T) *binRepo {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	m := &Manager{Dir: t.TempDir(), Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := m.ensure(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	return &binRepo{t: t, m: m, files: t.TempDir()}
}

// take makes a snapshot of server, of files of its own, and returns it.
func (b *binRepo) take(server, trigger, when string) Snapshot {
	b.t.Helper()
	b.n++
	if err := os.WriteFile(filepath.Join(b.files, "level.dat"), []byte(strings.Repeat("x", b.n)), 0o644); err != nil {
		b.t.Fatal(err)
	}
	args := []string{"--repo", b.m.repoDir("alpha"), "--password-file", b.m.keyFile("alpha"), "backup", b.files, "--host", server, "--tag", "avsys", "--tag", "trigger=" + trigger}
	if when != "" {
		args = append(args, "--time", when)
	}
	before, err := b.m.listAll(b.t.Context(), "alpha")
	if err != nil {
		b.t.Fatal(err)
	}
	if out, err := exec.Command("restic", args...).CombinedOutput(); err != nil {
		b.t.Fatalf("taking a snapshot: %v: %s", err, out)
	}
	after, err := b.m.listAll(b.t.Context(), "alpha")
	if err != nil {
		b.t.Fatal(err)
	}
	for _, s := range after {
		if !slices.Contains(ids(before), s.ID) {
			return s
		}
	}
	b.t.Fatal("the snapshot just taken is not listed")
	return Snapshot{}
}

func ids(list []Snapshot) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	slices.Sort(out)
	return out
}

func binnedIDs(list []Binned) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	slices.Sort(out)
	return out
}

func sorted(in ...string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// A snapshot in the bin is still in the repository, whole, and nothing
// lists it, finds it or reads from it until it is put back.
func TestTheBinHidesASnapshotUntilItIsPutBack(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	m := b.m
	one := b.take("probe", TriggerManual, "")
	two := b.take("probe", TriggerManual, "")
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	got, err := m.Bin(ctx, "alpha", []string{two.ID[:8]}, "steve", at)
	if err != nil || len(got) != 1 || got[0].ID != two.ID {
		t.Fatalf("into the bin by the start of its ID: %+v, %v", got, err)
	}
	if list, err := m.List(ctx, "alpha"); err != nil || !slices.Equal(ids(list), []string{one.ID}) {
		t.Errorf("listed with one in the bin: %v, %v", ids(list), err)
	}
	if _, err := m.Get(ctx, "alpha", two.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("one in the bin is found: %v", err)
	}
	if _, err := m.Record(ctx, "alpha", two.ID); err == nil {
		t.Error("one in the bin is read from, named in full")
	}
	if err := m.Held(ctx, "alpha", two.ID, func(*Snapshot) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("one in the bin is held: %v", err)
	}
	if _, err := m.CopyAs(ctx, "alpha", two.ID, "template-x", "steve"); !errors.Is(err, ErrNotFound) {
		t.Errorf("one in the bin is copied: %v", err)
	}
	// It is still there: an organization with it is not empty.
	if n, known := m.Holds(ctx, "alpha"); n != 2 || !known {
		t.Errorf("Holds with one in the bin: %d, %v; want 2", n, known)
	}
	bin, err := m.BinList(ctx, "alpha")
	if err != nil || len(bin) != 1 || bin[0].ID != two.ID || bin[0].DeletedBy != "steve" || !bin[0].DeletedAt.Equal(at) {
		t.Fatalf("the bin: %+v, %v", bin, err)
	}
	// Deleting it again finds nothing to delete.
	if _, err := m.Bin(ctx, "alpha", []string{two.ID}, "alex", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("into the bin twice: %v", err)
	}
	// All or none: one that is not there keeps the other out too.
	if _, err := m.Bin(ctx, "alpha", []string{one.ID, "ffffffff"}, "alex", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("with one that is not there: %v", err)
	}
	if list, _ := m.List(ctx, "alpha"); len(list) != 1 {
		t.Errorf("a refused delete put some in the bin: %v", ids(list))
	}

	// Put back, it is as it was, under its own ID.
	back, err := m.PutBack(ctx, "alpha", []string{two.ID[:10]})
	if err != nil || len(back) != 1 || back[0].ID != two.ID {
		t.Fatalf("putting back: %+v, %v", back, err)
	}
	if list, err := m.List(ctx, "alpha"); err != nil || !slices.Equal(ids(list), sorted(one.ID, two.ID)) {
		t.Errorf("listed once put back: %v, %v", ids(list), err)
	}
	if _, err := m.Get(ctx, "alpha", two.ID); err != nil {
		t.Errorf("put back, it is not found: %v", err)
	}
	if _, err := m.PutBack(ctx, "alpha", []string{two.ID}); !errors.Is(err, ErrNotBinned) {
		t.Errorf("putting back one not in the bin: %v", err)
	}
	if _, err := os.Stat(m.binFile("alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an empty bin leaves its note: %v", err)
	}
}

// The bin is emptied of what is due, or of what is named, and only those go.
func TestEmptyingTheBin(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	m := b.m
	kept := b.take("probe", TriggerManual, "")
	old := b.take("probe", TriggerManual, "")
	recent := b.take("probe", TriggerManual, "")
	named := b.take("other", TriggerFinal, "")
	day := func(n int) time.Time { return time.Date(2026, 11, n, 12, 0, 0, 0, time.UTC) }
	for id, at := range map[string]time.Time{old.ID: day(1), recent.ID: day(10), named.ID: day(2)} {
		if _, err := m.Bin(ctx, "alpha", []string{id}, "steve", at); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing is due before its time.
	if m.BinDue("alpha", func(at time.Time) bool { return at.Before(day(1)) }) {
		t.Error("due before its time")
	}
	if !m.BinDue("alpha", func(at time.Time) bool { return !at.After(day(2)) }) {
		t.Error("not due once its time is up")
	}
	gone, err := m.EmptyBin(ctx, "alpha", nil, func(x Binned) bool { return !x.DeletedAt.After(day(5)) })
	if err != nil || !slices.Equal(binnedIDs(gone), sorted(old.ID, named.ID)) {
		t.Fatalf("emptied of what was due: %v, %v", binnedIDs(gone), err)
	}
	all, err := m.listAll(ctx, "alpha")
	if err != nil || !slices.Equal(ids(all), sorted(kept.ID, recent.ID)) {
		t.Errorf("left in the repository: %v, %v", ids(all), err)
	}
	if bin, _ := m.BinList(ctx, "alpha"); !slices.Equal(binnedIDs(bin), []string{recent.ID}) {
		t.Errorf("left in the bin: %v", binnedIDs(bin))
	}
	// One that is not in the bin is not removed through it.
	if _, err := m.EmptyBin(ctx, "alpha", []string{kept.ID}, nil); !errors.Is(err, ErrNotBinned) {
		t.Errorf("emptying one not in the bin: %v", err)
	}
	if gone, err := m.EmptyBin(ctx, "alpha", []string{recent.ID[:8]}, nil); err != nil || len(gone) != 1 || gone[0].ID != recent.ID {
		t.Errorf("emptied of one by name: %+v, %v", gone, err)
	}
	if list, _ := m.List(ctx, "alpha"); !slices.Equal(ids(list), []string{kept.ID}) {
		t.Errorf("listed at the end: %v", ids(list))
	}
	if _, err := os.Stat(m.binFile("alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an emptied bin leaves its note: %v", err)
	}

	// A note of one removed some other way lists nothing, and goes.
	other := b.take("probe", TriggerManual, "")
	if _, err := m.Bin(ctx, "alpha", []string{other.ID}, "steve", day(3)); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "alpha", other.ID); err != nil {
		t.Fatal(err)
	}
	if bin, err := m.BinList(ctx, "alpha"); err != nil || len(bin) != 0 {
		t.Errorf("a note of one that is gone is listed: %+v, %v", bin, err)
	}
	if _, err := m.EmptyBin(ctx, "alpha", nil, func(Binned) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if entries, _ := m.binned("alpha"); len(entries) != 0 {
		t.Errorf("a note of one that is gone stays: %v", entries)
	}
}

// Retention removes what its policy lets go of at once, as it always has: it
// puts nothing in the bin, and takes nothing out of it either.
func TestRetentionDoesNotGoThroughTheBin(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	m := b.m
	var days []Snapshot
	for day := 1; day <= 5; day++ {
		days = append(days, b.take("probe", TriggerScheduled, time.Date(2026, 9, day, 4, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")))
	}
	// Someone deleted the oldest, which the policy would let go of too.
	if _, err := m.Bin(ctx, "alpha", []string{days[0].ID}, "steve", time.Now()); err != nil {
		t.Fatal(err)
	}
	removed, err := m.Forget(ctx, "alpha", "probe", Keep{Daily: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("retention removed %d, want 2: days 2 and 3", removed)
	}
	all, err := m.listAll(ctx, "alpha")
	if err != nil || !slices.Equal(ids(all), sorted(days[0].ID, days[3].ID, days[4].ID)) {
		t.Errorf("left: %v, %v; want the one in the bin and the newest two", ids(all), err)
	}
	if bin, _ := m.BinList(ctx, "alpha"); !slices.Equal(binnedIDs(bin), []string{days[0].ID}) {
		t.Errorf("the bin after retention: %v; want only the one deleted", binnedIDs(bin))
	}
}

// What a snapshot taken only if something changed is compared with is the
// newest of its server's that is not in the bin; with all of them in the
// bin it is taken whatever changed.
func TestUnchangedIsToldAgainstWhatIsNotInTheBin(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	m := b.m
	older := b.take("probe", TriggerScheduled, "2026-09-01 04:00:00")
	newer := b.take("probe", TriggerScheduled, "2026-09-02 04:00:00")
	b.take("other", TriggerScheduled, "2026-09-03 04:00:00")
	check := func(when, wantParent string, wantSkip bool) {
		t.Helper()
		parent, skip, err := m.unchangedFrom(ctx, "alpha", "probe")
		if err != nil || parent != wantParent || skip != wantSkip {
			t.Errorf("%s: %q, %v, %v; want %q, %v", when, parent, skip, err, wantParent, wantSkip)
		}
	}
	check("nothing in the bin", "", true)
	if _, err := m.Bin(ctx, "alpha", []string{older.ID}, "steve", time.Now()); err != nil {
		t.Fatal(err)
	}
	check("an older one in the bin", "", true)
	if _, err := m.Bin(ctx, "alpha", []string{newer.ID}, "steve", time.Now()); err != nil {
		t.Fatal(err)
	}
	check("all of them in the bin", "", false)
	if _, err := m.PutBack(ctx, "alpha", []string{older.ID}); err != nil {
		t.Fatal(err)
	}
	check("the newest in the bin", older.ID, true)
	parent, skip, err := m.unchangedFrom(ctx, "alpha", "nobody")
	if err != nil || parent != "" || !skip {
		t.Errorf("a server with none: %q, %v, %v", parent, skip, err)
	}
}

// A bin whose note cannot be read is not taken for an empty one: nothing is
// listed, and nothing written over it.
func TestBinNoteUnreadable(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	b.take("probe", TriggerManual, "")
	if err := os.Mkdir(b.m.binFile("alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := b.m.List(ctx, "alpha"); err == nil {
		t.Error("listed with the bin's note unreadable")
	}
	if _, err := b.m.Bin(ctx, "alpha", []string{"ffffffff"}, "steve", time.Now()); err == nil {
		t.Error("put in a bin whose note cannot be read")
	}
	if b.m.BinDue("alpha", func(time.Time) bool { return true }) {
		t.Error("due in a bin whose note cannot be read")
	}
}

// Destroying an organization's repository takes its bin with it.
func TestDestroyTakesTheBin(t *testing.T) {
	b := newBinRepo(t)
	s := b.take("probe", TriggerManual, "")
	if _, err := b.m.Bin(context.Background(), "alpha", []string{s.ID}, "steve", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.m.Destroy("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.m.binFile("alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the bin outlived its repository: %v", err)
	}
}

// Retention is applied as if what is in the bin were not there: one deleted
// never takes the place of one that was not. Keeping one day, a deleted
// snapshot of Tuesday leaves Monday's kept, as the newest there is, rather
// than letting it go for Tuesday's, which then waits out its own time.
func TestRetentionWeighsNothingInTheBin(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	m := b.m
	monday := b.take("probe", TriggerScheduled, "2026-09-07 04:00:00")
	tuesday := b.take("probe", TriggerScheduled, "2026-09-08 04:00:00")
	if _, err := m.Bin(ctx, "alpha", []string{tuesday.ID}, "steve", time.Now()); err != nil {
		t.Fatal(err)
	}
	kept, err := m.Kept(ctx, "alpha", "probe", Keep{Daily: 1})
	if err != nil || !slices.Equal(kept[monday.ID], []string{"daily"}) || kept[tuesday.ID] != nil {
		t.Errorf("kept: %v, %v; want Monday's alone, as the newest day", kept, err)
	}
	removed, err := m.Forget(ctx, "alpha", "probe", Keep{Daily: 1}, nil)
	if err != nil || removed != 0 {
		t.Fatalf("retention removed %d, %v; want nothing", removed, err)
	}
	all, err := m.listAll(ctx, "alpha")
	if err != nil || !slices.Equal(ids(all), sorted(monday.ID, tuesday.ID)) {
		t.Errorf("left: %v, %v; want both", ids(all), err)
	}
	// Put back, Tuesday's is the newest day, and Monday's goes.
	if _, err := m.PutBack(ctx, "alpha", []string{tuesday.ID}); err != nil {
		t.Fatal(err)
	}
	if removed, err := m.Forget(ctx, "alpha", "probe", Keep{Daily: 1}, nil); err != nil || removed != 1 {
		t.Fatalf("once put back, retention removed %d, %v; want Monday's", removed, err)
	}
	if all, _ := m.listAll(ctx, "alpha"); !slices.Equal(ids(all), []string{tuesday.ID}) {
		t.Errorf("left: %v; want Tuesday's", ids(all))
	}
}

// The policy is applied as restic's own forget applies it, which it was
// before the bin needed it worked out here: the same kept, for the same
// rules, and the same let go of, over days with several snapshots, weeks,
// months and a year that end, and rules with more to keep than there is.
func TestPolicyAsResticAppliesIt(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	for _, when := range []string{
		"2025-12-28 04:00:00", "2025-12-29 04:00:00", "2025-12-31 23:30:00", "2026-01-01 00:30:00",
		"2026-01-01 04:00:00", "2026-01-04 04:00:00", "2026-01-05 04:00:00", "2026-01-05 16:00:00",
		"2026-01-11 04:00:00", "2026-01-12 04:00:00", "2026-01-31 04:00:00", "2026-02-01 04:00:00",
		"2026-02-14 04:00:00", "2026-02-28 04:00:00", "2026-03-01 04:00:00", "2026-03-02 04:00:00",
		"2026-03-02 12:00:00", "2026-03-03 04:00:00",
	} {
		b.take("probe", TriggerScheduled, when)
	}
	b.take("probe", TriggerManual, "2026-03-04 04:00:00")
	b.take("other", TriggerScheduled, "2026-03-04 04:00:00")
	list, err := b.m.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []Keep{
		{Daily: 1}, {Daily: 3}, {Daily: 7, Weekly: 4, Monthly: 12, Yearly: 2}, {Daily: 2, Weekly: 2},
		{Weekly: 1, Monthly: 2}, {Monthly: 1}, {Yearly: 5}, {Daily: 40}, {Weekly: 3, Yearly: 1},
	} {
		args := []string{"forget", "--json", "--dry-run", "--no-lock", "--host", "probe", "--tag", "trigger=" + TriggerScheduled, "--group-by", "host"}
		for flag, n := range map[string]int{"--keep-daily": keep.Daily, "--keep-weekly": keep.Weekly, "--keep-monthly": keep.Monthly, "--keep-yearly": keep.Yearly} {
			if n > 0 {
				args = append(args, flag, strconv.Itoa(n))
			}
		}
		out, err := b.m.restic(ctx, "alpha", args...)
		if err != nil {
			t.Fatal(err)
		}
		var groups []struct {
			Remove  []resticSnapshot `json:"remove"`
			Reasons []struct {
				Snapshot resticSnapshot `json:"snapshot"`
				Matches  []string       `json:"matches"`
			} `json:"reasons"`
		}
		if err := json.Unmarshal(out, &groups); err != nil {
			t.Fatal(err)
		}
		wantKept, wantRemoved := map[string][]string{}, []string{}
		for _, g := range groups {
			for _, r := range g.Reasons {
				for _, m := range r.Matches {
					// "daily snapshot", "oldest daily snapshot", ...
					wantKept[r.Snapshot.ID] = append(wantKept[r.Snapshot.ID], strings.TrimSuffix(strings.TrimPrefix(m, "oldest "), " snapshot"))
				}
			}
			for _, r := range g.Remove {
				wantRemoved = append(wantRemoved, r.ID)
			}
		}
		kept, removed := applyPolicy(scheduledOf(list, "probe"), keep)
		if !reflect.DeepEqual(kept, wantKept) {
			t.Errorf("%+v kept:\n %v\nrestic keeps:\n %v", keep, kept, wantKept)
		}
		if got := ids(removed); !slices.Equal(got, sorted(wantRemoved...)) {
			t.Errorf("%+v lets go of:\n %v\nrestic lets go of:\n %v", keep, got, sorted(wantRemoved...))
		}
	}
}

// Inventory reads the list and all that is held together: one in the bin is
// held, not listed.
func TestInventory(t *testing.T) {
	b := newBinRepo(t)
	ctx := t.Context()
	one := b.take("probe", TriggerManual, "")
	two := b.take("probe", TriggerManual, "")
	if _, err := b.m.Bin(ctx, "alpha", []string{two.ID}, "steve", time.Now()); err != nil {
		t.Fatal(err)
	}
	listed, held, err := b.m.Inventory(ctx, "alpha")
	if err != nil || !slices.Equal(ids(listed), []string{one.ID}) || !slices.Equal(ids(held), sorted(one.ID, two.ID)) {
		t.Errorf("inventory: listed %v, held %v, %v", ids(listed), ids(held), err)
	}
	if listed, held, err := (&Manager{Dir: t.TempDir(), Restic: "restic"}).Inventory(ctx, "alpha"); err != nil || len(listed)+len(held) != 0 {
		t.Errorf("with no repository: %v %v %v", listed, held, err)
	}
}
