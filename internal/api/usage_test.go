package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// What a server's game uses is read from the metrics API's list of pods:
// the game's container alone, of the panel's own servers, with how many
// players were on. What is noted is kept for six hours, and let go of for a
// server that is gone.
func TestUsage(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	world.Status.Players = &avsysv1.PlayersStatus{Online: 3}
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	metrics := func(at time.Time) []byte {
		return fmt.Appendf(nil, `{"items":[
			{"metadata":{"name":"world","namespace":"mc-alpha"},"timestamp":%q,"containers":[
				{"name":"minecraft","usage":{"cpu":"1500m","memory":"3Gi"}},
				{"name":"workspace","usage":{"cpu":"5m","memory":"20Mi"}}]},
			{"metadata":{"name":"coredns","namespace":"kube-system"},"timestamp":%q,"containers":[
				{"name":"minecraft","usage":{"cpu":"1","memory":"1Gi"}}]}]}`, at.Format(time.RFC3339), at.Format(time.RFC3339))
	}
	servers := []avsysv1.MinecraftServer{*world}
	for i := range 3 {
		when := at.Add(time.Duration(i) * 4 * time.Hour)
		if err := p.srv.noteUsage(metrics(when), servers, when); err != nil {
			t.Fatal(err)
		}
	}
	got := p.srv.usage.of("mc-alpha/world", world.UID)
	// The first, eight hours before the last, is no longer kept.
	if len(got) != 2 || !got[0].At.Equal(at.Add(4*time.Hour)) {
		t.Fatalf("kept %d samples, from %v; want the last two", len(got), got)
	}
	if last := got[1]; !last.At.Equal(at.Add(8*time.Hour)) || last.CPU == nil || *last.CPU != 1500 || last.Memory == nil || *last.Memory != 3<<30 || last.Players != 3 {
		t.Errorf("noted %s, want 1500m and 3Gi with 3 players at %v", sampleString(last), at.Add(8*time.Hour))
	}
	if other := p.srv.usage.of("kube-system/coredns", ""); len(other) != 0 {
		t.Errorf("a pod that is not a server's was noted: %+v", other)
	}

	// Asked for by anyone who can see the server, with what it may use.
	p.srv.usage.setAvailable(true, at)
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/usage", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("asking for a server's use: %d %s", rec.Code, rec.Body)
	}
	var view usageView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Available || view.Limit != world.Spec.Resources.Memory.Value() || len(view.Samples) != 2 || view.Every != 30 {
		t.Errorf("answered %+v", view)
	}

	// Stopped, so not in the list of pods: what was noted is let go of as it
	// grows old all the same.
	if err := p.srv.noteUsage([]byte(`{"items":[]}`), servers, at.Add(14*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if left := p.srv.usage.of("mc-alpha/world", world.UID); len(left) != 0 {
		t.Errorf("samples older than six hours were kept for a stopped server: %+v", left)
	}
	// With no metrics API, a running server's players are noted all the
	// same, and a stopped one's are not.
	running := *world
	running.Status.Phase = avsysv1.PhaseRunning
	stopped := running
	stopped.Name, stopped.UID, stopped.Status.Phase = "stopped", "stopped", avsysv1.PhaseStopped
	later := at.Add(15 * time.Hour)
	if err := p.srv.noteUsage(nil, []avsysv1.MinecraftServer{running, stopped}, later); err != nil {
		t.Fatal(err)
	}
	if got := p.srv.usage.of("mc-alpha/world", world.UID); len(got) != 1 || !got[0].At.Equal(later) || got[0].CPU != nil || got[0].Memory != nil || got[0].Players != 3 {
		t.Errorf("with no metrics API, noted %d samples, the last %s; want players alone", len(got), sampleString(got[len(got)-1]))
	}
	if got := p.srv.usage.of("mc-alpha/stopped", "stopped"); len(got) != 0 {
		t.Errorf("a stopped server was noted: %+v", got)
	}
	p.srv.usage.keep(nil, later)

	// A server made under the name of one deleted does not take on its history.
	if err := p.srv.noteUsage(metrics(at), servers, at); err != nil {
		t.Fatal(err)
	}
	again := *world
	again.UID = "another"
	if err := p.srv.noteUsage([]byte(`{"items":[]}`), []avsysv1.MinecraftServer{again}, at); err != nil {
		t.Fatal(err)
	}
	if left := p.srv.usage.of("mc-alpha/world", "another"); left == nil || len(left) != 0 {
		t.Errorf("a new server of the same name was given %+v, want none, as an empty list", left)
	}

	// Gone from the list of servers: what was noted of it is let go of.
	if err := p.srv.noteUsage(metrics(at.Add(9*time.Hour)), nil, at); err != nil {
		t.Fatal(err)
	}
	if left := p.srv.usage.of("mc-alpha/world", world.UID); len(left) != 0 {
		t.Errorf("a deleted server's use was kept: %+v", left)
	}
}

func sampleString(s usageSample) string {
	n := func(v *int64) string {
		if v == nil {
			return "nil"
		}
		return fmt.Sprint(*v)
	}
	return fmt.Sprintf("{%v cpu=%s memory=%s players=%d}", s.At, n(s.CPU), n(s.Memory), s.Players)
}

// metricsStandIn answers as a metrics API would: with what pods and nodes
// use, or with err, as a cluster without one does.
type metricsStandIn struct {
	pods, nodes string
	err         error
}

func (m *metricsStandIn) Pods(context.Context) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []byte(m.pods), nil
}

func (m *metricsStandIn) Nodes(context.Context) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []byte(m.nodes), nil
}

// Sampling as the panel does it, every thirty seconds: running servers
// alone are noted, with what they use and their players, and each node with
// what it uses in all. Six hours of each are kept. A server that stops is no
// longer noted, and one deleted is let go of. A cluster with no metrics API
// still has its running servers' players noted.
func TestSampleUsage(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	metrics := &metricsStandIn{
		pods: `{"items":[{"metadata":{"name":"world","namespace":"mc-alpha"},"containers":[{"name":"minecraft","usage":{"cpu":"750m","memory":"2Gi"}}]}]}`,
		// A node nothing of the panel's runs on is shown all the same.
		nodes: `{"items":[{"metadata":{"name":"pc"},"usage":{"cpu":"2","memory":"6Gi"}},{"metadata":{"name":"pi"},"usage":{"cpu":"100m","memory":"1Gi"}}]}`,
	}
	p.srv.Metrics = metrics
	p.addServer("alpha", "idle", "idle.alpha.example")
	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	cpu := resource.MustParse("1500m")
	world.Spec.Resources.CPU = &cpu
	if err := p.srv.Client.Update(ctx, world); err != nil {
		t.Fatal(err)
	}
	setStatus := func(phase avsysv1.Phase, players *avsysv1.PlayersStatus) {
		t.Helper()
		world.Status.Phase, world.Status.Players = phase, players
		if err := p.srv.Client.Status().Update(ctx, world); err != nil {
			t.Fatal(err)
		}
	}
	setStatus(avsysv1.PhaseRunning, &avsysv1.PlayersStatus{Online: 2})

	// Seven hours of samples, thirty seconds apart.
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	ticks := int(7 * time.Hour / usageEvery)
	for i := range ticks {
		p.srv.SampleUsage(ctx, at.Add(time.Duration(i)*usageEvery))
	}
	last := at.Add(time.Duration(ticks-1) * usageEvery)
	kept := int(usageKept/usageEvery) + 1
	got := p.srv.usage.of("mc-alpha/world", world.UID)
	if len(got) != kept || last.Sub(got[0].At) > usageKept {
		t.Fatalf("kept %d samples; want %d, the last six hours", len(got), kept)
	}
	if s := got[len(got)-1]; !s.At.Equal(last) || s.CPU == nil || *s.CPU != 750 || s.Memory == nil || *s.Memory != 2<<30 || s.Players != 2 {
		t.Errorf("last noted %s, want 750m and 2Gi with 2 players at %v", sampleString(s), last)
	}
	idle := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "idle"}, idle); err != nil {
		t.Fatal(err)
	}
	if got := p.srv.usage.of("mc-alpha/idle", idle.UID); len(got) != 0 {
		t.Errorf("a stopped server was noted: %d samples", len(got))
	}
	nodes := p.srv.usage.nodeHistory()
	if len(nodes["pc"]) != kept || len(nodes["pi"]) != kept {
		t.Fatalf("kept %d samples of pc and %d of pi; want %d of each", len(nodes["pc"]), len(nodes["pi"]), kept)
	}
	if s := nodes["pc"][kept-1]; !s.At.Equal(last) || s.CPU != 2000 || s.Memory != 6<<30 {
		t.Errorf("pc last noted %+v", s)
	}

	// What a server may use is said beside what it used.
	_, view := p.get("viewer", "GET", "/orgs/alpha/servers/world/usage", "")
	if view["cpuLimit"] != float64(1500) || view["limit"] != float64(world.Spec.Resources.Memory.Value()) || view["available"] != true {
		t.Errorf("a server's usage: limit %v, cpuLimit %v, available %v", view["limit"], view["cpuLimit"], view["available"])
	}
	// Nodes' use is for the panel's administrator alone.
	if code, _ := p.do("owner", "GET", "/nodes/usage", ""); code != 403 {
		t.Errorf("an organization's owner asked for nodes' use: %d, want 403", code)
	}
	code, all := p.get("root", "GET", "/nodes/usage", "")
	if code != 200 || all["available"] != true || all["every"] != float64(30) {
		t.Fatalf("nodes' use: %d %v", code, all["available"])
	}
	if pc, _ := all["nodes"].(map[string]any)["pc"].([]any); len(pc) != kept {
		t.Errorf("answered %d samples of pc, want %d", len(pc), kept)
	}

	// The server stops: no longer measured, and not noted.
	metrics.pods = `{"items":[]}`
	setStatus(avsysv1.PhaseStopped, nil)
	p.srv.SampleUsage(ctx, last.Add(usageEvery))
	if got := p.srv.usage.of("mc-alpha/world", world.UID); len(got) != kept-1 || !got[len(got)-1].At.Equal(last) {
		t.Errorf("after stopping, %d samples, the last at %v; want nothing new", len(got), got[len(got)-1].At)
	}

	// The metrics API goes away. Running again, the server's players are
	// noted; what it uses is not, nor what nodes use, and nodes' history is
	// still there.
	metrics.err = apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "")
	setStatus(avsysv1.PhaseRunning, &avsysv1.PlayersStatus{Online: 5})
	gone := last.Add(2 * usageEvery)
	p.srv.SampleUsage(ctx, gone)
	if available, _ := p.srv.usage.state(); available {
		t.Error("still said to report use after the metrics API went away")
	}
	got = p.srv.usage.of("mc-alpha/world", world.UID)
	if s := got[len(got)-1]; !s.At.Equal(gone) || s.CPU != nil || s.Memory != nil || s.Players != 5 {
		t.Errorf("with no metrics API, noted %s; want 5 players alone", sampleString(s))
	}
	if n := p.srv.usage.nodeHistory()["pc"]; len(n) == 0 || !n[len(n)-1].At.Equal(last.Add(usageEvery)) {
		t.Errorf("nodes' history after the metrics API went away: %d samples", len(n))
	}
	if _, ok := p.srv.usage.node("pc"); ok {
		t.Error("what a node used was still shown as current")
	}
	// Six hours on, nodes' history has all grown too old.
	p.srv.SampleUsage(ctx, gone.Add(usageKept+time.Minute))
	if n := p.srv.usage.nodeHistory(); len(n) != 0 {
		t.Errorf("kept nodes' history older than six hours: %d nodes", len(n))
	}

	// Deleted: what was noted of it is let go of at the next sample.
	if err := p.srv.Client.Delete(ctx, world); err != nil {
		t.Fatal(err)
	}
	p.srv.SampleUsage(ctx, gone.Add(usageKept+time.Minute+usageEvery))
	if got := p.srv.usage.of("mc-alpha/world", world.UID); len(got) != 0 {
		t.Errorf("a deleted server's samples were kept: %d", len(got))
	}
}
