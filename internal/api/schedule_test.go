package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
)

func TestLastDue(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	policy := backupPolicy{Minute: 9*60 + 30}
	for now, want := range map[string]string{
		"2026-10-12T09:30:00Z":      "2026-10-12T09:30:00Z",
		"2026-10-12T09:29:59Z":      "2026-10-11T09:30:00Z",
		"2026-10-12T23:59:00Z":      "2026-10-12T09:30:00Z",
		"2026-10-12T00:00:00Z":      "2026-10-11T09:30:00Z",
		"2027-01-01T03:00:00Z":      "2026-12-31T09:30:00Z",
		"2026-10-12T06:00:00-04:00": "2026-10-12T09:30:00Z", // 10:00 UTC, whatever the clock on the wall says
	} {
		if got := policy.lastDue(at(now)); !got.Equal(at(want)) {
			t.Errorf("last due at %s: got %s, want %s", now, got.Format(time.RFC3339), want)
		}
	}
}

// A server is owed the run that was last due unless it has had it, or did
// not exist yet when it was due.
func TestOwed(t *testing.T) {
	due := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	server := func(created time.Time, lastRun *time.Time) *avsysv1.MinecraftServer {
		mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)}}
		if lastRun != nil {
			at := metav1.NewTime(*lastRun)
			mc.Status.Snapshots = &avsysv1.SnapshotStatus{LastRun: &at}
		}
		return mc
	}
	old := due.AddDate(0, 0, -30)
	before, after := due.Add(-time.Hour), due.Add(time.Minute)
	for name, c := range map[string]struct {
		mc   *avsysv1.MinecraftServer
		want bool
	}{
		"never run":                     {server(old, nil), true},
		"last run was yesterday's":      {server(old, &before), true},
		"has had this run":              {server(old, &after), false},
		"ran exactly when due":          {server(old, &due), false},
		"made after the run was due":    {server(after, nil), false},
		"only ever snapshotted by hand": {&avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(old)}, Status: avsysv1.MinecraftServerStatus{Snapshots: &avsysv1.SnapshotStatus{LastID: "aaaa"}}}, true},
	} {
		if got := owed(c.mc, due); got != c.want {
			t.Errorf("%s: owed = %v, want %v", name, got, c.want)
		}
	}
}

func TestBackupPolicyValidation(t *testing.T) {
	if err := defaultPolicy.validate(); err != nil {
		t.Fatalf("the default policy is not valid: %v", err)
	}
	if !defaultPolicy.Enabled {
		t.Error("the daily snapshots are off unless switched on")
	}
	for name, change := range map[string]func(*backupPolicy){
		"a negative time":      func(p *backupPolicy) { p.Minute = -1 },
		"a time past midnight": func(p *backupPolicy) { p.Minute = 24 * 60 },
		"no daily snapshots":   func(p *backupPolicy) { p.Keep.Daily = 0 },
		"negative weeks":       func(p *backupPolicy) { p.Keep.Weekly = -1 },
		"a century of years":   func(p *backupPolicy) { p.Keep.Yearly = 100 },
		"a thousand days":      func(p *backupPolicy) { p.Keep.Daily = 1000 },
	} {
		policy := defaultPolicy
		change(&policy)
		if err := policy.validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// Weeks, months and years can each be done without.
	if err := (backupPolicy{Minute: 0, Keep: backup.Keep{Daily: 1}}).validate(); err != nil {
		t.Errorf("a policy that keeps one day only: %v", err)
	}
}

// get returns the JSON a request answers with.
func (p *panel) get(who, method, path, body string) (int, map[string]any) {
	p.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestBackupPolicyIsPerOrganization(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()

	// Nothing set: the default, with when it next acts.
	code, got := p.get("viewer", "GET", "/orgs/alpha/backup-policy", "")
	if code != 200 || got["enabled"] != true || got["minute"] != float64(540) || got["next"] == nil {
		t.Fatalf("the default policy: %d %v", code, got)
	}
	if keep, _ := got["keep"].(map[string]any); keep["daily"] != float64(7) || keep["weekly"] != float64(4) || keep["monthly"] != float64(12) || keep["yearly"] != float64(2) {
		t.Errorf("the default retention: %v", got["keep"])
	}

	if code, got := p.get("admin", "PUT", "/orgs/alpha/backup-policy", `{"enabled":false,"minute":60,"keep":{"daily":3}}`); code != 200 || got["enabled"] != false || got["next"] != nil {
		t.Fatalf("switching the daily snapshots off: %d %v", code, got)
	}
	if code, got := p.get("viewer", "GET", "/orgs/alpha/backup-policy", ""); code != 200 || got["enabled"] != false || got["minute"] != float64(60) {
		t.Errorf("the policy as stored: %d %v", code, got)
	}
	// What is not valid is refused, and leaves what was there.
	if code, _ := p.do("admin", "PUT", "/orgs/alpha/backup-policy", `{"enabled":true,"minute":60,"keep":{"daily":0}}`); code != 400 {
		t.Errorf("a policy that keeps nothing: got %d, want 400", code)
	}
	if _, got := p.get("viewer", "GET", "/orgs/alpha/backup-policy", ""); got["enabled"] != false {
		t.Errorf("a refused policy replaced the stored one: %v", got)
	}
	// Another organization's is its own.
	if _, got := p.get("outsider", "GET", "/orgs/beta/backup-policy", ""); got["enabled"] != true {
		t.Errorf("another organization's policy changed with this one's: %v", got)
	}

	// The policy goes with the organization.
	beta, err := p.srv.Store.OrgBySlug(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := p.do("outsider", "PUT", "/orgs/beta/backup-policy", `{"enabled":true,"minute":0,"keep":{"daily":1}}`); code != 200 {
		t.Fatalf("setting beta's policy: %d", code)
	}
	if code, _ := p.do("outsider", "DELETE", "/orgs/beta", ""); code != 204 {
		t.Fatalf("deleting beta: %d", code)
	}
	if _, err := p.srv.Store.Setting(ctx, policyKey(beta.ID)); err == nil {
		t.Error("a deleted organization's policy was left behind")
	}
}

// The servers a look finds are those of organizations with the daily
// snapshots on, that have not had the run that was last due, and whose last
// attempt did not just fail.
func TestOwedServers(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	p.addServer("alpha", "second", "")
	p.addServer("beta", "theirs", "")

	names := func() string {
		var out []string
		for _, group := range p.srv.owedServers(ctx, now) {
			for _, mc := range group.servers {
				out = append(out, group.org.Slug+"/"+mc.Name)
			}
		}
		return strings.Join(out, " ")
	}
	if got, want := names(), "alpha/second alpha/world beta/theirs"; got != want {
		t.Fatalf("owed with nothing run yet: %q, want %q", got, want)
	}

	// One that has had today's run is not owed it again.
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	p.srv.finish(ctx, "mc-alpha", "world", kindSnapshot, nil, nil, nil, true)
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	if mc.Status.Snapshots == nil || mc.Status.Snapshots.LastRun == nil {
		t.Fatal("a completed scheduled run was not noted on the server")
	}
	// (The run was noted at the real time, which is after the pretend one.)
	if got, want := names(), "alpha/second beta/theirs"; got != want {
		t.Errorf("owed after world's run: %q, want %q", got, want)
	}
	// A snapshot taken by hand does not count as the scheduled run.
	p.srv.finish(ctx, "mc-alpha", "second", kindSnapshot, nil, nil, nil, false)
	if got := names(); !strings.Contains(got, "alpha/second") {
		t.Errorf("a manual snapshot stood in for the scheduled run: %q", got)
	}

	// One whose attempt just failed is left alone for a while, then tried again.
	p.srv.schedule.note("mc-alpha/second", true, now.Add(-10*time.Minute))
	if got, want := names(), "beta/theirs"; got != want {
		t.Errorf("owed just after second failed: %q, want %q", got, want)
	}
	p.srv.schedule.note("mc-alpha/second", true, now.Add(-2*time.Hour))
	if got := names(); !strings.Contains(got, "alpha/second") {
		t.Errorf("a failure two hours ago still holds the server back: %q", got)
	}

	// An organization that has switched them off has none owed.
	if code, _ := p.do("outsider", "PUT", "/orgs/beta/backup-policy", `{"enabled":false,"minute":540,"keep":{"daily":7}}`); code != 200 {
		t.Fatalf("switching beta's off: %d", code)
	}
	if got := names(); strings.Contains(got, "beta/") {
		t.Errorf("a server of an organization with the schedule off is owed a snapshot: %q", got)
	}
}

func TestOneRunPerOrganization(t *testing.T) {
	var sc schedule
	release, ok := sc.claim("a")
	if !ok {
		t.Fatal("could not start a run")
	}
	if _, ok := sc.claim("a"); ok {
		t.Error("two runs of one organization at once")
	}
	if other, ok := sc.claim("b"); !ok {
		t.Error("one organization's run held up another's")
	} else {
		other()
	}
	release()
	if _, ok := sc.claim("a"); !ok {
		t.Error("a run could not start once the last was over")
	}
}

// Space that could not be given back is still owed after the run that
// failed to, so that the next run tries again without needing anything new
// to let go of.
func TestAFailedPruneIsOwed(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	alpha, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if p.srv.schedule.pruneOwed(alpha.ID) {
		t.Fatal("space is owed before anything was removed")
	}
	// A repository this panel cannot prune: there is no restic to do it with.
	if err := os.MkdirAll(filepath.Join(p.srv.Backups.Dir, "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.srv.Backups.Dir, "alpha", "config"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.srv.schedule.owesPrune(alpha.ID, true)
	p.srv.runScheduled(ctx, alpha, defaultPolicy, nil)
	if !p.srv.schedule.pruneOwed(alpha.ID) {
		t.Error("a prune that failed is no longer owed")
	}
	// One that works settles it.
	p.srv.Backups.Restic = "true"
	p.srv.runScheduled(ctx, alpha, defaultPolicy, nil)
	if p.srv.schedule.pruneOwed(alpha.ID) {
		t.Error("a prune that succeeded is still owed")
	}
	// And another organization's debt is its own.
	if p.srv.schedule.pruneOwed("someone-else") {
		t.Error("one organization's owed prune shows up for another")
	}
}
