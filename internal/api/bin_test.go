package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// binOfAlpha is alpha's bin as the API lists it, to whoever asks.
func (l *libraryRepo) bin(who string) binList {
	l.t.Helper()
	code, raw := l.call(who, "GET", "/orgs/alpha/bin", "")
	if code != http.StatusOK {
		l.t.Fatalf("the bin as %s: %d %s", who, code, raw)
	}
	var out binList
	if err := json.Unmarshal(raw, &out); err != nil {
		l.t.Fatal(err)
	}
	return out
}

// listed is the IDs of alpha's snapshots of servers on the Backups page.
func (l *libraryRepo) listed() []string {
	l.t.Helper()
	code, raw := l.call("viewer", "GET", "/orgs/alpha/snapshots", "")
	if code != http.StatusOK {
		l.t.Fatalf("the snapshots: %d %s", code, raw)
	}
	var out snapshotList
	if err := json.Unmarshal(raw, &out); err != nil {
		l.t.Fatal(err)
	}
	var ids []string
	for _, s := range out.Snapshots {
		ids = append(ids, s.ID)
	}
	return ids
}

// snapshotOf takes a snapshot of a server straight into alpha's repository,
// with the record the panel writes, and returns its ID.
func (l *libraryRepo) snapshotOf(server, trigger string) string {
	l.t.Helper()
	before, err := l.p.srv.Backups.List(l.t.Context(), "alpha")
	if err != nil {
		l.t.Fatal(err)
	}
	dir := l.t.TempDir()
	record := `{"format":1,"org":"alpha","server":"` + server + `","spec":{"eula":true,"loader":"vanilla","version":"1.21.4",` +
		`"resources":{"memory":"2Gi"},"storage":{"size":"2Gi"},"expose":{"type":"None"}}}`
	for path, content := range map[string]string{"data/world/level.dat": time.Now().String(), "data/.avsys/snapshot.json": record} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			l.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			l.t.Fatal(err)
		}
	}
	l.restic(dir, "backup", "data", "--host", server, "--tag", "avsys", "--tag", "trigger="+trigger, "--tag", "version=1.21.4", "--tag", "loader=vanilla")
	after, err := l.p.srv.Backups.List(l.t.Context(), "alpha")
	if err != nil {
		l.t.Fatal(err)
	}
	for _, s := range after {
		if !slices.ContainsFunc(before, func(b backup.Snapshot) bool { return b.ID == s.ID }) {
			return s.ID
		}
	}
	l.t.Fatal("no new snapshot")
	return ""
}

// activityOf is what the activity log says was done to a server's
// snapshots, oldest first, as "who action".
func (l *libraryRepo) activityOf(server string) []string {
	l.t.Helper()
	org, _ := l.p.srv.Store.OrgBySlug(l.t.Context(), "alpha")
	list, err := l.p.srv.Store.ServerActivity(l.t.Context(), org.ID, server, time.Time{}, []string{"snapshot."}, 50)
	if err != nil {
		l.t.Fatal(err)
	}
	var out []string
	for _, a := range slices.Backward(list) {
		who := "panel"
		if a.User != nil {
			who = a.User.Username
		}
		out = append(out, who+" "+a.Action)
	}
	return out
}

// A deleted snapshot goes to the bin: hidden everywhere a snapshot is listed
// or used, with its name and its server's notes kept, until an admin puts it
// back. Only the panel's administrator removes it early.
func TestADeletedSnapshotGoesToTheBin(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	org, _ := p.srv.Store.OrgBySlug(ctx, "alpha")
	src, short := l.src, l.src[:8]
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/"+short, `{"name":"First"}`); code != 200 {
		t.Fatal(code)
	}
	if err := p.srv.Store.SetServerNotes(ctx, org.ID, "survival", "Our first world."); err != nil {
		t.Fatal(err)
	}
	if got := l.bin("viewer"); !got.Available || got.Days != defaultBinDays || got.CanEmpty || len(got.Snapshots) != 0 {
		t.Errorf("an empty bin: %+v", got)
	}

	// Deleting is for admins, as it was.
	if code, _ := p.call("operator", "DELETE", "/orgs/alpha/snapshots/"+short, ""); code != http.StatusForbidden {
		t.Errorf("an operator deleting: %d", code)
	}
	before := time.Now()
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/snapshots/"+short, ""); code != http.StatusNoContent {
		t.Fatalf("deleting: %d", code)
	}
	// Gone from every list and every use.
	if ids := l.listed(); len(ids) != 0 {
		t.Errorf("listed after it was deleted: %v", ids)
	}
	for _, c := range []struct{ who, method, path, body string }{
		{"viewer", "GET", "/orgs/alpha/snapshots/" + short, ""},
		{"viewer", "GET", "/orgs/alpha/snapshots/" + src + "/icon", ""},
		{"admin", "PATCH", "/orgs/alpha/snapshots/" + short, `{"name":"Again"}`},
		{"admin", "DELETE", "/orgs/alpha/snapshots/" + short, ""},
		{"admin", "DELETE", "/orgs/alpha/snapshots", `{"ids":["` + short + `"]}`},
		{"admin", "POST", "/orgs/alpha/servers", `{"name":"survival","fromSnapshot":"` + short + `"}`},
		{"admin", "POST", "/orgs/alpha/servers", `{"name":"another","fromSnapshot":"` + src + `"}`},
		{"admin", "POST", "/orgs/alpha/templates", `{"name":"From the bin","snapshot":"` + src + `"}`},
		{"operator", "POST", "/orgs/alpha/worlds?wait=true", `{"name":"From the bin","snapshot":"` + src + `"}`},
		{"admin", "POST", "/orgs/alpha/servers/world/snapshots/" + short + "/restore", ""},
	} {
		if code, raw := l.call(c.who, c.method, c.path, c.body); code != http.StatusNotFound {
			t.Errorf("%s %s with it in the bin: %d %s, want 404", c.method, c.path, code, raw)
		}
	}
	// Its server's name stays its, while it can be put back and the server
	// brought back from it.
	if code, raw := l.call("admin", "POST", "/orgs/alpha/servers", `{"name":"survival","spec":{"eula":true,"version":"1.21.4"}}`); code != http.StatusConflict {
		t.Errorf("a new server under the name of one whose snapshots are in the bin: %d %s", code, raw)
	}
	// The organization still holds it.
	if n, known := p.srv.Backups.Holds(ctx, "alpha"); n != 1 || !known {
		t.Errorf("held: %d %v", n, known)
	}
	// In the bin, with who deleted it, when, and until when.
	bin := l.bin("viewer")
	if len(bin.Snapshots) != 1 {
		t.Fatalf("the bin: %+v", bin)
	}
	got := bin.Snapshots[0]
	if got.ID != src || got.Server != "survival" || got.DeletedBy != "admin" || got.Name != "First" ||
		got.DeletedAt.Before(before.Add(-time.Second)) || !got.Until.Equal(got.DeletedAt.Add(7*24*time.Hour)) {
		t.Errorf("in the bin: %+v", got)
	}
	if names, _ := p.srv.Store.SnapshotNames(ctx, org.ID); names[src].Name != "First" {
		t.Errorf("its name went: %v", names)
	}
	if notes, _ := p.srv.Store.ServerNotes(ctx, org.ID, "survival"); notes != "Our first world." {
		t.Errorf("its server's notes went: %q", notes)
	}
	if !l.bin("root").CanEmpty {
		t.Error("the panel's administrator is not offered to empty it")
	}

	// Put back by those who can restore one.
	for _, who := range []string{"viewer", "operator"} {
		if code, _ := p.call(who, "POST", "/orgs/alpha/bin/"+short+"/put-back", ""); code != http.StatusForbidden {
			t.Errorf("put back by %s: %d, want 403", who, code)
		}
	}
	if code, back := p.call("admin", "POST", "/orgs/alpha/bin/"+short+"/put-back", ""); code != http.StatusOK || back["id"] != src || back["name"] != "First" {
		t.Fatalf("put back: %d %v", code, back)
	}
	if ids := l.listed(); !slices.Equal(ids, []string{src}) {
		t.Errorf("listed once put back: %v", ids)
	}
	if code, _ := p.call("admin", "POST", "/orgs/alpha/bin/"+short+"/put-back", ""); code != http.StatusNotFound {
		t.Errorf("put back twice: %d", code)
	}

	// Several at once go to the bin together; only the panel's
	// administrator removes them early, an owner of the organization not.
	other := l.snapshotOf("world", backup.TriggerManual)
	if code, got := p.call("admin", "DELETE", "/orgs/alpha/snapshots", `{"ids":["`+short+`","`+other[:8]+`"]}`); code != 200 || got["deleted"] != float64(2) || got["keptDays"] != float64(7) {
		t.Fatalf("deleting two: %d %v", code, got)
	}
	if ids := l.listed(); len(ids) != 0 {
		t.Errorf("listed: %v", ids)
	}
	// The server that exists does not list it on its own tab either.
	if code, got := p.call("viewer", "GET", "/orgs/alpha/servers/world/snapshots", ""); code != 200 || len(got["snapshots"].([]any)) != 0 {
		t.Errorf("its server's own list: %d %v", code, got["snapshots"])
	}
	for _, who := range []string{"viewer", "admin", "owner"} {
		if code, _ := p.call(who, "DELETE", "/orgs/alpha/bin/"+short, ""); code != http.StatusForbidden {
			t.Errorf("emptied early by %s: %d, want 403", who, code)
		}
		if code, _ := p.call(who, "DELETE", "/orgs/alpha/bin", ""); code != http.StatusForbidden {
			t.Errorf("all emptied early by %s: %d, want 403", who, code)
		}
	}
	if code, got := p.call("root", "DELETE", "/orgs/alpha/snapshots/"+short, ""); code != http.StatusNotFound {
		t.Errorf("deleted from the list once in the bin: %d %v", code, got)
	}
	if code, got := p.call("root", "DELETE", "/orgs/alpha/bin/"+short, ""); code != 200 || got["removed"] != float64(1) {
		t.Fatalf("one emptied: %d %v", code, got)
	}
	if ids, _ := p.srv.Backups.Present("alpha"); ids[src] {
		t.Error("an emptied snapshot is still in the repository")
	}
	if code, got := p.call("root", "DELETE", "/orgs/alpha/bin", ""); code != 200 || got["removed"] != float64(1) {
		t.Fatalf("the rest emptied: %d %v", code, got)
	}
	if bin := l.bin("viewer"); len(bin.Snapshots) != 0 {
		t.Errorf("left in the bin: %+v", bin.Snapshots)
	}
	// Its name and its server's notes go with the last of it.
	if names, _ := p.srv.Store.SnapshotNames(ctx, org.ID); len(names) != 0 {
		t.Errorf("names left: %v", names)
	}
	if notes, _ := p.srv.Store.ServerNotes(ctx, org.ID, "survival"); notes != "" {
		t.Errorf("notes left of a server with nothing to bring it back from: %q", notes)
	}
	// Who did what, in the activity log.
	if got, want := l.activityOf("survival"), []string{"admin snapshot.name", "admin snapshot.delete", "admin snapshot.put-back", "admin snapshot.delete", "root snapshot.empty"}; !slices.Equal(got, want) {
		t.Errorf("activity: %v, want %v", got, want)
	}
	// Its name is free again.
	if code, raw := l.call("admin", "POST", "/orgs/alpha/servers", `{"name":"survival","spec":{"eula":true,"version":"1.21.4"}}`); code != http.StatusCreated {
		t.Errorf("a new server under the name once nothing is left of the old: %d %s", code, raw)
	}
}

// The bin empties itself once a snapshot's days are up, and as many days as
// the panel's administrator says; none deletes at once, as before.
func TestTheBinEmptiesItselfInTime(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/snapshots/"+l.src, ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	deleted := l.bin("viewer").Snapshots[0].DeletedAt

	p.srv.emptyExpired(ctx, deleted.Add(7*24*time.Hour-time.Minute))
	if len(l.bin("viewer").Snapshots) != 1 {
		t.Fatal("emptied before its time")
	}
	// Fewer days: what is in the bin goes sooner, counted from its deletion.
	if code, _ := p.call("admin", "PATCH", "/panel", `{"binDays":3}`); code != http.StatusForbidden {
		t.Errorf("the days set by an organization's admin: %d", code)
	}
	for _, bad := range []string{`{"binDays":-1}`, `{"binDays":91}`} {
		if code, _ := p.call("root", "PATCH", "/panel", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	if code, got := p.call("root", "PATCH", "/panel", `{"binDays":3}`); code != 200 || got["binDays"] != float64(3) {
		t.Fatalf("set to 3 days: %d %v", code, got)
	}
	if got := l.bin("viewer"); got.Days != 3 || !got.Snapshots[0].Until.Equal(deleted.Add(3*24*time.Hour)) {
		t.Errorf("with 3 days: %+v", got)
	}
	p.srv.emptyExpired(ctx, deleted.Add(3*24*time.Hour))
	if got := l.bin("viewer"); len(got.Snapshots) != 0 {
		t.Fatalf("not emptied once its time was up: %+v", got.Snapshots)
	}
	if ids, _ := p.srv.Backups.Present("alpha"); ids[l.src] {
		t.Error("still in the repository")
	}
	if got := l.activityOf("survival"); !slices.Equal(got, []string{"admin snapshot.delete", "panel snapshot.expire"}) {
		t.Errorf("activity: %v", got)
	}

	// None: deleted for good at once.
	if code, _ := p.call("root", "PATCH", "/panel", `{"binDays":0}`); code != 200 {
		t.Fatal(code)
	}
	if code, meta := p.call("", "GET", "/meta", ""); code != 200 || meta["binDays"] != float64(0) {
		t.Errorf("meta: %d %v", code, meta["binDays"])
	}
	now := l.snapshotOf("world", backup.TriggerManual)
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/snapshots/"+now, ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if ids, _ := p.srv.Backups.Present("alpha"); ids[now] {
		t.Error("deleted with no days to keep it, it is still in the repository")
	}
	if got := l.bin("viewer"); len(got.Snapshots) != 0 || got.Days != 0 {
		t.Errorf("with no days: %+v", got)
	}
}

// Deleting a server keeps its snapshots, listed, as before. Deleting those
// puts them in the bin; put back, the server is brought back from one as
// ever.
func TestADeletedServerIsBroughtBackFromTheBin(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	org, _ := p.srv.Store.OrgBySlug(ctx, "alpha")
	first := l.snapshotOf("world", backup.TriggerManual)
	last := l.snapshotOf("world", backup.TriggerFinal)
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/servers/world", `{"notes":"The main world."}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/servers/world?snapshot=false", ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if ids := l.listed(); !slices.Contains(ids, first) || !slices.Contains(ids, last) {
		t.Fatalf("a deleted server's snapshots are not listed: %v", ids)
	}
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/snapshots", `{"ids":["`+first+`","`+last+`"]}`); code != 200 {
		t.Fatal(code)
	}
	if notes, _ := p.srv.Store.ServerNotes(ctx, org.ID, "world"); notes != "The main world." {
		t.Errorf("the notes of a server that can be brought back went: %q", notes)
	}
	if code, raw := l.call("admin", "POST", "/orgs/alpha/servers", `{"name":"world","fromSnapshot":"`+last+`"}`); code != http.StatusNotFound {
		t.Errorf("brought back from the bin itself: %d %s", code, raw)
	}
	if code, _ := p.call("admin", "POST", "/orgs/alpha/bin/"+last+"/put-back", ""); code != 200 {
		t.Fatal(code)
	}
	code, made := p.call("admin", "POST", "/orgs/alpha/servers", `{"name":"world","fromSnapshot":"`+last[:8]+`"}`)
	if code != http.StatusCreated || made["notes"] != "The main world." {
		t.Fatalf("brought back: %d %v", code, made)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil || mc.Annotations[avsysv1.AnnotationCreating] != last {
		t.Errorf("the server brought back: %v %v", err, mc.Annotations)
	}
	// Its other snapshot is still in the bin, to be put back as its history.
	if bin := l.bin("viewer"); len(bin.Snapshots) != 1 || bin.Snapshots[0].ID != first {
		t.Errorf("the bin: %+v", bin.Snapshots)
	}
	// Calls off its making, which has no files to reach here.
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/servers/world?snapshot=false", ""); code != http.StatusNoContent {
		t.Errorf("deleting it again: %d", code)
	}
}

// Retention lets go of scheduled snapshots at once, as before: nothing it
// removes goes to the bin, and what someone put there stays its time.
func TestRetentionBypassesTheBin(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	var days []string
	for day := 1; day <= 4; day++ {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte{byte(day)}, 0o644); err != nil {
			t.Fatal(err)
		}
		l.restic(dir, "backup", "f", "--host", "world", "--tag", "avsys", "--tag", "trigger=scheduled", "--time", time.Date(2026, 9, day, 4, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05"))
	}
	list, err := p.srv.Backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Server == "world" {
			days = append(days, s.ID)
		}
	}
	slices.Reverse(days) // oldest first
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/snapshots/"+days[0], ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	removed, err := p.srv.Backups.Forget(ctx, "alpha", "world", backup.Keep{Daily: 1}, nil)
	if err != nil || removed != 2 {
		t.Fatalf("retention: %d, %v; want 2 removed", removed, err)
	}
	if bin := l.bin("viewer"); len(bin.Snapshots) != 1 || bin.Snapshots[0].ID != days[0] {
		t.Errorf("the bin after retention: %+v", bin.Snapshots)
	}
	there, _ := p.srv.Backups.Present("alpha")
	if !there[days[0]] || there[days[1]] || there[days[2]] || !there[days[3]] {
		t.Errorf("left after retention: %v", there)
	}
}

// An organization whose servers' snapshots would go with it, bin and all, is
// deleted by its owner only once they have been deleted and their time is
// up. The panel's administrator can delete it at once.
func TestAnOrganizationKeepsItsDeletedSnapshots(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/servers/world?snapshot=false", ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if got, code := p.do("owner", "DELETE", "/orgs/alpha?snapshots=delete", ""); got != 409 || code != "has_snapshots" {
		t.Errorf("with snapshots listed: %d %s", got, code)
	}
	if code, _ := p.call("owner", "DELETE", "/orgs/alpha/snapshots/"+l.src, ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if got, code := p.do("owner", "DELETE", "/orgs/alpha?snapshots=delete", ""); got != 409 || code != "snapshots_kept" {
		t.Errorf("with snapshots in the bin: %d %s", got, code)
	}
	if got, _ := p.do("owner", "GET", "/orgs/alpha", ""); got != 200 {
		t.Fatal("the organization went")
	}
	// The panel's administrator may, bin and all.
	if err := p.srv.orgKeepsSnapshots(context.Background(), p.users["root"], "alpha"); err != nil {
		t.Errorf("for the panel's administrator: %v", err)
	}
	p.srv.emptyExpired(context.Background(), time.Now().Add(8*24*time.Hour))
	if got, code := p.do("owner", "DELETE", "/orgs/alpha?snapshots=delete", ""); got != 204 {
		t.Errorf("once the bin is empty: %d %s", got, code)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "alpha")); !os.IsNotExist(err) {
		t.Errorf("the repository is still there: %v", err)
	}
}

// Without restic the snapshots cannot be counted, in the bin or not, but
// deleting the organization would remove them all the same: its owner is
// refused unless there is nothing at all, and the panel's administrator is
// not.
func TestAnOrganizationKeepsItsSnapshotsWithoutRestic(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	if code, _ := p.call("admin", "DELETE", "/orgs/alpha/servers/world?snapshot=false", ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if code, _ := p.call("owner", "DELETE", "/orgs/alpha/snapshots/"+l.src, ""); code != http.StatusNoContent {
		t.Fatal(code)
	}
	p.srv.Backups.Restic = filepath.Join(t.TempDir(), "no-restic")
	if got, code := p.do("owner", "DELETE", "/orgs/alpha?snapshots=delete", ""); got != http.StatusConflict || code != "has_snapshots" {
		t.Errorf("by its owner: %d %s, want 409 has_snapshots", got, code)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "alpha", "config")); err != nil {
		t.Fatalf("the repository went: %v", err)
	}
	// One with nothing here is deleted as ever.
	if err := p.srv.Store.CreateOrg(ctx, &store.Org{Name: "Gamma", Slug: "gamma"}, p.users["owner"].ID); err != nil {
		t.Fatal(err)
	}
	if got, code := p.do("owner", "DELETE", "/orgs/gamma?snapshots=delete", ""); got != http.StatusNoContent {
		t.Errorf("one with nothing: %d %s", got, code)
	}
	if got, code := p.do("root", "DELETE", "/orgs/alpha?snapshots=delete", ""); got != http.StatusNoContent {
		t.Errorf("by the panel's administrator: %d %s", got, code)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "alpha")); !os.IsNotExist(err) {
		t.Errorf("the repository is still there: %v", err)
	}
}

// A snapshot put in the bin and back while the list is read keeps its name:
// it is in the repository all along, whichever of the two it was in.
func TestANameSurvivesPuttingBackWhileListed(t *testing.T) {
	l := newLibraryPanel(t, nil)
	p := l.p
	ctx := context.Background()
	org, _ := p.srv.Store.OrgBySlug(ctx, "alpha")
	if code, _ := p.call("admin", "PATCH", "/orgs/alpha/snapshots/"+l.src[:8], `{"name":"First","color":"teal"}`); code != 200 {
		t.Fatal(code)
	}
	stop := make(chan struct{})
	done := make(chan error)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := p.srv.Backups.Bin(ctx, "alpha", []string{l.src}, "admin", time.Now()); err != nil {
				done <- err
				return
			}
			if _, err := p.srv.Backups.PutBack(ctx, "alpha", []string{l.src}); err != nil {
				done <- err
				return
			}
		}
	}()
	for range 40 {
		if code, raw := l.call("viewer", "GET", "/orgs/alpha/snapshots", ""); code != http.StatusOK {
			t.Fatalf("listing: %d %s", code, raw)
		}
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if names, _ := p.srv.Store.SnapshotNames(ctx, org.ID); names[l.src].Name != "First" || names[l.src].Color != "teal" {
		t.Errorf("its name: %+v", names[l.src])
	}
}
