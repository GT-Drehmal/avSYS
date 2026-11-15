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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// downPanel is a panel whose server alpha/world has its files on node a,
// which is down, with node b up and with room. Its snapshots are answered
// by a stand-in for restic, from what snapshots writes.
type downPanel struct {
	*panel
	repo string
}

func newDownPanel(t *testing.T) *downPanel {
	t.Helper()
	p := newPanel(t)
	ctx := context.Background()
	nodeAt := func(name string, ready corev1.ConditionStatus) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelHostname: name}}, Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Gi")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
		}}
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-world"},
		Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}},
		}}}}},
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world-data"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name}}
	for _, obj := range []client.Object{nodeAt("a", corev1.ConditionUnknown), nodeAt("b", corev1.ConditionTrue), pv, pvc} {
		if err := p.srv.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Annotations = map[string]string{avsysv1.AnnotationNode: "a"}
	mc.Spec.State = avsysv1.StateRunning
	if err := p.srv.Client.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}

	// What makes a directory a repository, as far as the panel looks, and a
	// restic that lists what is in snapshots.json and gives the record in
	// record.json out of any snapshot.
	dir := t.TempDir()
	repo := filepath.Join(p.srv.Backups.Dir, "alpha")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restic := filepath.Join(dir, "restic")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in\n" +
		"snapshots) cat " + filepath.Join(repo, "snapshots.json") + "; exit $? ;;\n" +
		"dump) cat " + filepath.Join(repo, "record.json") + "; exit $? ;;\n" +
		"esac; done\nexit 1\n"
	if err := os.WriteFile(restic, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p.srv.Backups.Restic = restic
	record, err := json.Marshal(snapshotRecord{Format: 1, Org: "alpha", Server: "world", Spec: avsysv1.MinecraftServerSpec{
		EULA: true, Version: "1.21.4", Loader: avsysv1.LoaderVanilla, Properties: map[string]string{"difficulty": "hard"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "record.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	d := &downPanel{panel: p, repo: repo}
	d.snapshots()
	return d
}

// snapshots is what the repository holds: one of each server named, taken
// that long ago, newest first or not.
func (d *downPanel) snapshots(taken ...string) map[string]string {
	d.t.Helper()
	type snap struct {
		ID       string    `json:"id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Tags     []string  `json:"tags"`
	}
	list := []snap{}
	ids := map[string]string{}
	for i, t := range taken {
		server, ago, _ := strings.Cut(t, "@")
		age, err := time.ParseDuration(ago)
		if err != nil {
			d.t.Fatal(err)
		}
		id := strings.Repeat(string(rune('a'+i)), 8) + "0123456789abcdef"
		ids[t] = id
		list = append(list, snap{ID: id, Time: time.Now().Add(-age).UTC(), Hostname: server, Tags: []string{"avsys", "trigger=scheduled"}})
	}
	raw, err := json.Marshal(list)
	if err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.repo, "snapshots.json"), raw, 0o600); err != nil {
		d.t.Fatal(err)
	}
	return ids
}

func (d *downPanel) placement() placementView {
	d.t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/placement", nil)
	req.Header.Set("Authorization", "Bearer "+d.tokens["owner"])
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		d.t.Fatalf("placement: %d %s", rec.Code, rec.Body.String())
	}
	var v placementView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		d.t.Fatal(err)
	}
	return v
}

func (d *downPanel) move(who, body string) (int, string) {
	d.t.Helper()
	return d.do(who, "POST", "/orgs/alpha/servers/world/move", body)
}

func (d *downPanel) world() *avsysv1.MinecraftServer {
	d.t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := d.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		d.t.Fatal(err)
	}
	return mc
}

// A server whose files are on a node that is down cannot have them copied
// off it. It is offered its newest snapshot instead, and moved from it: made
// again on the other node as the same server, held off until the
// snapshot's files are in.
func TestMoveOffANodeThatIsDown(t *testing.T) {
	d := newDownPanel(t)
	ids := d.snapshots("other@1h", "world@3h", "world@30h")
	newest, older := ids["world@3h"], ids["world@30h"]

	v := d.placement()
	if !v.Down || v.Node != "a" || v.Newest == nil || v.Newest.ID != newest || v.Unable != "" {
		t.Fatalf("placement %+v, newest %+v; want down on a, offered %s", v, v.Newest, newest)
	}
	if got, code := d.move("owner", `{"node":"b"}`); got != 409 || code != "node_down" {
		t.Errorf("moved as usual: %d %q, want 409 node_down", got, code)
	}
	if got, code := d.move("owner", `{"node":"b","fromSnapshot":"`+older+`"}`); got != 409 || code != "snapshot_changed" {
		t.Errorf("from an older snapshot than the newest: %d %q, want 409 snapshot_changed", got, code)
	}
	// As any move: for those who may change its settings.
	if got, code := d.move("operator", `{"node":"b","fromSnapshot":"`+newest+`"}`); got != 403 || code != "forbidden" {
		t.Errorf("an operator: %d %q, want 403", got, code)
	}
	if mc := d.world(); mc.Annotations[avsysv1.AnnotationMoving] != "" {
		t.Fatalf("refused, and still marked: %v", mc.Annotations)
	}

	if got, code := d.move("owner", `{"node":"b","fromSnapshot":"`+newest+`"}`); got != 202 {
		t.Fatalf("moving from its newest snapshot: %d %q", got, code)
	}
	// Placed on b and held for the snapshot's files, in one change; the
	// controller takes its old pod and volume from there.
	var mc *avsysv1.MinecraftServer
	for range 50 {
		if mc = d.world(); mc.Annotations[avsysv1.AnnotationMoving] != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if mc.Annotations[avsysv1.AnnotationNode] != "b" || mc.Annotations[avsysv1.AnnotationMoving] != "b" || mc.Annotations[avsysv1.AnnotationRestoring] != newest {
		t.Errorf("annotations %v, want placed on b and held for %s", mc.Annotations, newest)
	}
	// It is the same server, as it was: nothing was stopped or made anew.
	if mc.Spec.State != avsysv1.StateRunning || mc.Spec.Expose.Hostname != "play.alpha.example" {
		t.Errorf("state %s, address %s", mc.Spec.State, mc.Spec.Expose.Hostname)
	}
	// The snapshot is all there is of its files while it moves.
	if _, ok := d.srv.ops.remove("mc-alpha", newest); ok {
		t.Error("the snapshot it is moved from could be deleted")
	}
}

// Once its files are in, a server made again from a snapshot takes the
// settings its files go with, as a restore does, and is let go of as it
// was: running here.
func TestSettleRebuilt(t *testing.T) {
	d := newDownPanel(t)
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	mc := d.world()
	mc.Annotations[avsysv1.AnnotationMoving], mc.Annotations[avsysv1.AnnotationRestoring] = "b", "aaaaaaaa"
	mc.Spec.Version, mc.Spec.Properties = "1.21.5", map[string]string{"difficulty": "easy"}
	if err := d.srv.Client.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}
	record := &snapshotRecord{Spec: avsysv1.MinecraftServerSpec{Version: "1.21.4", Loader: avsysv1.LoaderVanilla, Properties: map[string]string{"difficulty": "hard"}}, AutoOps: "Steve"}
	if err := d.srv.settleRebuilt(ctx, key, record); err != nil {
		t.Fatal(err)
	}
	mc = d.world()
	if mc.Spec.Version != "1.21.4" || mc.Spec.Properties["difficulty"] != "hard" || mc.Spec.State != avsysv1.StateRunning {
		t.Errorf("version %s, properties %v, state %s", mc.Spec.Version, mc.Spec.Properties, mc.Spec.State)
	}
	if mc.Annotations[avsysv1.AnnotationAutoOps] != "Steve" {
		t.Errorf("the note of operators is %q", mc.Annotations[avsysv1.AnnotationAutoOps])
	}
	for _, mark := range []string{avsysv1.AnnotationMoving, avsysv1.AnnotationRestoring, avsysv1.AnnotationMoveWas} {
		if _, held := mc.Annotations[mark]; held {
			t.Errorf("still marked %s", mark)
		}
	}
	if mc.Spec.Expose.Hostname != "play.alpha.example" {
		t.Errorf("address %s", mc.Spec.Expose.Hostname)
	}
}

// It is not moved from a snapshot when there is none of it, when its
// snapshots cannot be read, or when its node is up, which has its files as
// they are now.
func TestMoveFromSnapshotRefused(t *testing.T) {
	t.Run("no snapshot", func(t *testing.T) {
		d := newDownPanel(t)
		d.snapshots("other@1h")
		v := d.placement()
		if !v.Down || v.Newest != nil || !strings.Contains(v.Unable, "There is no snapshot of world") {
			t.Errorf("placement %+v", v)
		}
		if got, code := d.move("owner", `{"node":"b","fromSnapshot":"aaaaaaaa"}`); got != 409 || code != "no_snapshot" {
			t.Errorf("%d %q, want 409 no_snapshot", got, code)
		}
	})
	unreachable := map[string]func(t *testing.T, d *downPanel){
		// restic cannot read it.
		"unreadable": func(t *testing.T, d *downPanel) {
			if err := os.Remove(filepath.Join(d.repo, "snapshots.json")); err != nil {
				t.Fatal(err)
			}
		},
		// Where the repositories are kept is not there as it should be.
		"not there": func(t *testing.T, d *downPanel) {
			blocked := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocked, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			d.srv.Backups.Dir = filepath.Join(blocked, "under-a-file")
		},
		// Reading it does not end, as on storage that went with a node.
		"hanging": func(t *testing.T, d *downPanel) {
			if err := os.WriteFile(d.srv.Backups.Restic, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			was := repositoryWait
			repositoryWait = 500 * time.Millisecond
			t.Cleanup(func() { repositoryWait = was })
		},
	}
	for name, break_ := range unreachable {
		t.Run(name, func(t *testing.T) {
			d := newDownPanel(t)
			ids := d.snapshots("world@3h")
			break_(t, d)
			v := d.placement()
			if !v.Down || v.Newest != nil || v.Unable != "Snapshots cannot be read right now." {
				t.Errorf("placement %+v", v)
			}
			if got, code := d.move("owner", `{"node":"b","fromSnapshot":"`+ids["world@3h"]+`"}`); got != 503 || code != "snapshots_unreachable" {
				t.Errorf("%d %q, want 503 snapshots_unreachable", got, code)
			}
			if mc := d.world(); mc.Annotations[avsysv1.AnnotationMoving] != "" {
				t.Errorf("refused, and still marked: %v", mc.Annotations)
			}
		})
	}
	t.Run("node up", func(t *testing.T) {
		d := newDownPanel(t)
		ids := d.snapshots("world@3h")
		a := &corev1.Node{}
		if err := d.srv.Client.Get(context.Background(), client.ObjectKey{Name: "a"}, a); err != nil {
			t.Fatal(err)
		}
		a.Status.Conditions[0].Status = corev1.ConditionTrue
		if err := d.srv.Client.Status().Update(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		if v := d.placement(); v.Down || v.Newest != nil {
			t.Errorf("placement %+v", v)
		}
		if got, code := d.move("owner", `{"node":"b","fromSnapshot":"`+ids["world@3h"]+`"}`); got != 400 || code != "invalid" {
			t.Errorf("%d %q, want 400", got, code)
		}
	})
}
