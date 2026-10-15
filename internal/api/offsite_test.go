package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// withOffsite gives the panel an offsite copy that is a directory, and
// returns where its own repositories are.
func (p *panel) withOffsite(restic string) string {
	dir := p.t.TempDir()
	p.srv.Backups = &backup.Manager{
		Dir: dir, Restic: restic, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Offsite: &backup.Offsite{Repository: filepath.Join(p.t.TempDir(), "far"), Password: "kept-by-the-operator"},
	}
	return dir
}

func (p *panel) noteOffsite(dir, org string, st backup.OffsiteStatus) {
	p.t.Helper()
	data, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, org+".offsite"), data, 0o600); err != nil {
		p.t.Fatal(err)
	}
}

// An organization is synced when something has changed since the last sync
// that went through, and once after every start; a failure is not retried
// every minute.
func TestOffsiteDue(t *testing.T) {
	p := newPanel(t)
	dir := p.withOffsite("restic-is-not-installed")
	started := p.srv.offsite.since()
	at := func(d time.Duration) *time.Time { v := started.Add(d); return &v }
	now := started.Add(10 * time.Minute)

	for name, c := range map[string]struct {
		st   backup.OffsiteStatus
		want bool
	}{
		"never synced":                          {backup.OffsiteStatus{}, true},
		"synced before this start":              {backup.OffsiteStatus{LastAttempt: at(-time.Hour), LastSuccess: at(-time.Hour)}, true},
		"synced since this start":               {backup.OffsiteStatus{LastAttempt: at(time.Minute), LastSuccess: at(time.Minute)}, false},
		"failed a moment ago":                   {backup.OffsiteStatus{LastAttempt: at(5 * time.Minute), LastSuccess: at(-time.Hour), Error: "unreachable"}, false},
		"failed before this start":              {backup.OffsiteStatus{LastAttempt: at(-time.Minute), LastSuccess: at(-time.Hour), Error: "unreachable"}, true},
		"failed, and never went through at all": {backup.OffsiteStatus{LastAttempt: at(5 * time.Minute), Error: "unreachable"}, false},
	} {
		p.noteOffsite(dir, "alpha", c.st)
		if got := p.srv.offsiteDue("alpha", now); got != c.want {
			t.Errorf("%s: due %v, want %v", name, got, c.want)
		}
	}
	// A failure is tried again once it is an hour old.
	p.noteOffsite(dir, "alpha", backup.OffsiteStatus{LastAttempt: at(5 * time.Minute), Error: "unreachable"})
	if !p.srv.offsiteDue("alpha", started.Add(70*time.Minute)) {
		t.Error("a failure over an hour old is not tried again")
	}
}

func TestOffsiteIsOffUnlessSetUp(t *testing.T) {
	p := newPanel(t)
	code, got := p.get("viewer", "GET", "/orgs/alpha/offsite", "")
	if code != 200 || got["configured"] != false || got["pending"] != false {
		t.Errorf("with no offsite copy: %d %v", code, got)
	}
	// With restic there and no offsite copy, asking for one says so.
	if _, err := exec.LookPath("restic"); err == nil {
		p.srv.Backups.Restic = "restic"
		if code, name := p.do("admin", "POST", "/orgs/alpha/offsite/run", ""); code != 503 || name != "no_offsite" {
			t.Errorf("copy now with no offsite copy: %d %s, want 503 no_offsite", code, name)
		}
	}
}

// The whole of it against a directory: copied, shown as current, lost here,
// and brought back.
func TestOffsiteThroughTheAPI(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	dir := p.withOffsite("restic")
	repo, key := filepath.Join(dir, "alpha"), filepath.Join(dir, "alpha.key")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("restic", append([]string{"--repo", repo, "--password-file", key}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
	}
	files := t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "level.dat"), []byte("a world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("init")
	run("backup", files, "--host", "world", "--tag", "avsys", "--tag", "trigger=manual")

	if code, got := p.get("viewer", "GET", "/orgs/alpha/offsite", ""); code != 200 || got["configured"] != true || got["pending"] != true || got["lastSuccess"] != nil {
		t.Fatalf("before the first copy: %d %v", code, got)
	}
	if code, got := p.get("admin", "POST", "/orgs/alpha/offsite/run?wait=true", ""); code != 200 || got["copied"] != float64(1) || got["snapshots"] != float64(1) {
		t.Fatalf("copy now: %d %v", code, got)
	}
	if code, got := p.get("viewer", "GET", "/orgs/alpha/offsite", ""); code != 200 || got["pending"] != false || got["snapshots"] != float64(1) || got["lastSuccess"] == nil || got["error"] != nil {
		t.Errorf("after the copy: %d %v", code, got)
	}
	// Nothing is brought back into an organization that has its snapshots.
	if code, name := p.do("root", "POST", "/orgs/alpha/offsite/fetch", ""); code != 409 || name != "has_snapshots" {
		t.Errorf("fetch into an organization with snapshots: %d %s, want 409 has_snapshots", code, name)
	}

	// The panel's storage is lost.
	for _, path := range []string{repo, key, filepath.Join(dir, "alpha.offsite")} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	if code, got := p.get("viewer", "GET", "/orgs/alpha/snapshots", ""); code != 200 || len(got["snapshots"].([]any)) != 0 {
		t.Fatalf("snapshots here after the loss: %d %v", code, got)
	}
	if code, got := p.get("root", "GET", "/orgs/alpha/offsite/snapshots", ""); code != 200 || len(got["snapshots"].([]any)) != 1 {
		t.Fatalf("what is offsite: %d %v", code, got)
	}
	if code, got := p.get("root", "POST", "/orgs/alpha/offsite/fetch", ""); code != 200 || got["fetched"] != float64(1) {
		t.Fatalf("fetch: %d %v", code, got)
	}
	code, got := p.get("viewer", "GET", "/orgs/alpha/snapshots", "")
	list, _ := got["snapshots"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["server"] != "world" {
		t.Errorf("snapshots here after the fetch: %d %v", code, got)
	}

	// A failure is shown, and is not "nothing there".
	p.srv.Backups.Offsite.Password = "not-it"
	if code, name := p.do("admin", "POST", "/orgs/alpha/offsite/run?wait=true", ""); code != 502 || name != "offsite_failed" {
		t.Errorf("copy with the wrong password: %d %s, want 502 offsite_failed", code, name)
	}
	if _, got := p.get("viewer", "GET", "/orgs/alpha/offsite", ""); got["error"] == nil || got["pending"] != true {
		t.Errorf("after a failed copy: %v", got)
	}
}

// The panel copies a few organizations at a time on its own, so that one
// long copy does not hold up the rest, and never one organization twice.
func TestOffsiteCopiesAFewAtATime(t *testing.T) {
	var o offsite
	var release []func()
	for i := 0; i < offsiteAtOnce; i++ {
		done, ok := o.slot()
		if !ok {
			t.Fatalf("place %d of %d was refused", i+1, offsiteAtOnce)
		}
		release = append(release, done)
	}
	if _, ok := o.slot(); ok {
		t.Error("one more than the limit was let in")
	}
	release[0]()
	if _, ok := o.slot(); !ok {
		t.Error("a place given back was not given out again")
	}

	done, ok := o.claim("alpha")
	if !ok || !o.isRunning("alpha") {
		t.Fatal("claiming an organization")
	}
	if _, ok := o.claim("alpha"); ok {
		t.Error("an organization was claimed twice")
	}
	if _, ok := o.claim("beta"); !ok {
		t.Error("one organization's copy held up another's")
	}
	done()
	if o.isRunning("alpha") {
		t.Error("an organization stayed claimed after its copy")
	}
}

// Snapshots brought back from offsite are not handed to a server that took
// their server's name after them; those of a server that is still there go
// back to it, which is what fetching is for.
func TestFetchingOffsiteKeepsHistoriesApart(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	dir := p.withOffsite("restic")
	repo, key := filepath.Join(dir, "alpha"), filepath.Join(dir, "alpha.key")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("restic", append([]string{"--repo", repo, "--password-file", key}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
	}
	files := t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "level.dat"), []byte("a world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("init")
	// Of world, which exists, now; and of gone, deleted long ago.
	run("backup", files, "--host", "world", "--tag", "avsys", "--tag", "trigger=manual")
	run("backup", files, "--host", "gone", "--tag", "avsys", "--tag", "trigger=final", "--time", "2026-01-01 12:00:00")
	if code, got := p.get("admin", "POST", "/orgs/alpha/offsite/run?wait=true", ""); code != 200 || got["copied"] != float64(2) {
		t.Fatalf("copy now: %d %v", code, got)
	}
	// The panel's storage is lost, and a new server is made under the old name.
	for _, path := range []string{repo, key, filepath.Join(dir, "alpha.offsite")} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	p.addServer("alpha", "gone", "")
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "gone"}, mc); err != nil {
		t.Fatal(err)
	}
	if mc.CreationTimestamp.IsZero() {
		mc.CreationTimestamp = metav1.Now()
		if err := p.srv.Client.Update(context.Background(), mc); err != nil {
			t.Fatal(err)
		}
	}

	code, name := p.do("root", "POST", "/orgs/alpha/offsite/fetch", "")
	if code != 409 || name != "name_has_history" {
		t.Fatalf("fetch with a new server under an old name: %d %s, want 409 name_has_history", code, name)
	}
	if code, got := p.get("viewer", "GET", "/orgs/alpha/snapshots", ""); code != 200 || len(got["snapshots"].([]any)) != 0 {
		t.Errorf("something was brought back anyway: %v", got)
	}

	// Once the new one is gone, both histories come back, world's to world.
	if err := p.srv.Client.Delete(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if code, got := p.get("root", "POST", "/orgs/alpha/offsite/fetch", ""); code != 200 || got["fetched"] != float64(2) {
		t.Fatalf("fetch: %d %v", code, got)
	}
}
