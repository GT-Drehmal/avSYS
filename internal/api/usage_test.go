package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

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
	want := usageSample{At: at.Add(8 * time.Hour), CPU: 1500, Memory: 3 << 30, Players: 3}
	if got[1] != want {
		t.Errorf("noted %+v, want %+v", got[1], want)
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
