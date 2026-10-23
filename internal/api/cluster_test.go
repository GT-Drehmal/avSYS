package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A cluster that does not answer is said to be lost after two checks in a
// row, from when the first failed; browsers are told, and told again, with
// a resync, once it answers. Meanwhile no usage is noted.
func TestReach(t *testing.T) {
	p := newPanel(t)
	events := p.srv.Events.subscribe()
	defer p.srv.Events.unsubscribe(events)
	at := time.Date(2026, 10, 21, 14, 27, 0, 0, time.UTC)
	down := errors.New("connection refused")

	p.srv.noteReach(nil, at)
	p.srv.noteReach(down, at.Add(5*time.Second))
	if _, lost := p.srv.reach.lostSince(); lost {
		t.Fatal("lost after one failed check")
	}
	// One slow answer between failures starts over.
	p.srv.noteReach(nil, at.Add(10*time.Second))
	p.srv.noteReach(down, at.Add(15*time.Second))
	p.srv.noteReach(down, at.Add(20*time.Second))
	since, lost := p.srv.reach.lostSince()
	if !lost || !since.Equal(at.Add(15*time.Second)) {
		t.Fatalf("lost=%v since %v; want lost since the first of two failures, %v", lost, since, at.Add(15*time.Second))
	}
	if e := next(t, events); e.Type != "cluster" || e.Data.(clusterView).Reachable {
		t.Errorf("told %+v, want the cluster lost", e)
	}
	p.srv.noteReach(down, at.Add(25*time.Second))

	// Nothing is noted of servers meanwhile.
	p.srv.usage.setNodes(map[string]nodeInUse{"orbstack": {CPU: 1}})
	p.srv.usageTick(context.Background(), at.Add(30*time.Second))
	if _, ok := p.srv.usage.node("orbstack"); ok {
		t.Error("what nodes use was still shown while the cluster was lost")
	}
	if got := p.srv.usage.of("mc-alpha/world", ""); len(got) != 0 {
		t.Errorf("noted %+v while the cluster was lost", got)
	}

	p.srv.noteReach(nil, at.Add(35*time.Second))
	if e := next(t, events); e.Type != "cluster" || !e.Data.(clusterView).Reachable {
		t.Errorf("told %+v, want the cluster back", e)
	}
	if e := next(t, events); e.Type != "resync" {
		t.Errorf("told %+v, want a resync", e)
	}
	select {
	case e := <-events:
		t.Errorf("told %+v as well", e)
	default:
	}
}

// A browser that connects is told first whether the cluster can be reached.
func TestReachIsToldOnConnecting(t *testing.T) {
	p := newPanel(t)
	at := time.Date(2026, 10, 21, 14, 27, 0, 0, time.UTC)
	p.srv.noteReach(errors.New("connection refused"), at)
	p.srv.noteReach(errors.New("connection refused"), at.Add(reachEvery))
	ts := httptest.NewServer(p.h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := bufio.NewScanner(res.Body)
	var event string
	for lines.Scan() {
		if e, ok := strings.CutPrefix(lines.Text(), "event: "); ok {
			event = e
		}
		if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
			if want := `{"reachable":false,"since":"2026-10-21T14:27:00Z"}`; event != "cluster" || data != want {
				t.Fatalf("first told %s %s, want cluster %s", event, data, want)
			}
			return
		}
	}
	t.Fatalf("told nothing: %v", lines.Err())
}

func next(t *testing.T, events chan Event) Event {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(time.Second):
		t.Fatal("told nothing")
		return Event{}
	}
}
