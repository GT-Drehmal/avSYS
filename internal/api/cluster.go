package api

import (
	"context"
	"sync"
	"time"
)

// How often the panel checks that it can reach the cluster, and how many
// checks in a row must fail before it says it cannot: one may be slow.
const (
	reachEvery = 5 * time.Second
	reachFails = 2
)

// reach is whether the panel can reach the cluster. What it shows of
// servers is what it last heard from it, which goes stale while it cannot.
type reach struct {
	mu sync.Mutex
	// failing is when the checks began to fail, and fails how many have
	// since; lost is set once enough have.
	failing time.Time
	fails   int
	lost    bool
}

// clusterView is what browsers are told of the cluster.
type clusterView struct {
	Reachable bool `json:"reachable"`
	// Since is when it could last be reached, while it cannot.
	Since *time.Time `json:"since,omitempty"`
}

// lostSince is when the cluster stopped answering, while it is lost.
func (r *reach) lostSince() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failing, r.lost
}

func (r *reach) view() clusterView {
	since, lost := r.lostSince()
	if !lost {
		return clusterView{Reachable: true}
	}
	return clusterView{Since: &since}
}

// RunReach checks that the cluster can be reached, until ctx ends, and
// tells browsers when that changes.
func (s *Server) RunReach(ctx context.Context) {
	if s.Kube == nil {
		return
	}
	rest := s.Kube.Discovery().RESTClient()
	if rest == nil {
		return
	}
	for {
		check, cancel := context.WithTimeout(ctx, reachEvery)
		_, err := rest.Get().AbsPath("/readyz").Do(check).Raw()
		cancel()
		if ctx.Err() != nil {
			return
		}
		s.noteReach(err, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(reachEvery):
		}
	}
}

// noteReach notes how a check went, at now.
func (s *Server) noteReach(err error, now time.Time) {
	r := &s.reach
	r.mu.Lock()
	changed := false
	if err == nil {
		changed = r.lost
		r.failing, r.fails, r.lost = time.Time{}, 0, false
	} else {
		if r.fails == 0 {
			r.failing = now
		}
		r.fails++
		if !r.lost && r.fails >= reachFails {
			r.lost, changed = true, true
		}
	}
	r.mu.Unlock()
	if !changed {
		return
	}
	if err != nil {
		s.Log.Warn("the cluster cannot be reached", "error", err)
	} else {
		s.Log.Info("the cluster can be reached again")
	}
	s.Events.Publish(Event{Type: "cluster", Data: r.view()})
	if err == nil {
		// What changed meanwhile was not heard: browsers ask again.
		s.Events.Publish(Event{Type: "resync"})
	}
}
