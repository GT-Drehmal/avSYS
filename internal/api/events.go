package api

import (
	"net/http"
	"sync"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Event is one change pushed to connected browsers.
type Event struct {
	Type      string `json:"type"`
	Namespace string `json:"-"`
	Data      any    `json:"data"`
}

// Hub fans server changes out to every open event stream, so the UI reflects
// cluster state without polling.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

func (h *Hub) subscribe() chan Event {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			// A subscriber this far behind is told to refetch everything
			// rather than being allowed to hold up the rest.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- Event{Type: "resync"}:
			default:
			}
		}
	}
}

// WatchServers publishes an event for every MinecraftServer change seen by
// the informer.
func (s *Server) WatchServers(informer ctrlcache.Informer) error {
	publish := func(kind string, obj any) {
		if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
			obj = tomb.Obj
		}
		mc, ok := obj.(*avsysv1.MinecraftServer)
		if !ok {
			return
		}
		s.Events.Publish(Event{Type: kind, Namespace: mc.Namespace, Data: s.view(mc)})
	}
	_, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { publish("server.updated", obj) },
		UpdateFunc: func(old, obj any) {
			publish("server.updated", obj)
			s.started(old, obj)
		},
		DeleteFunc: func(obj any) { publish("server.deleted", obj) },
	})
	return err
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, u *store.User) error {
	ctx := r.Context()
	// Membership is re-read periodically so a stream opened before someone
	// was removed from an organization stops carrying its events.
	allowed := map[string]bool{}
	refresh := func() error {
		orgs, err := s.Store.OrgsForUser(ctx, u)
		if err != nil {
			return err
		}
		clear(allowed)
		for _, o := range orgs {
			allowed[s.Namespace(o.Slug)] = true
		}
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}

	ch := s.Events.subscribe()
	defer s.Events.unsubscribe(ch)
	send := sse(w)
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	membership := time.NewTicker(time.Minute)
	defer membership.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-ch:
			if e.Namespace != "" && !allowed[e.Namespace] {
				continue
			}
			if err := send(e.Type, e.Data); err != nil {
				return nil
			}
		case <-heartbeat.C:
			if err := send("ping", nil); err != nil {
				return nil
			}
		case <-membership.C:
			if err := refresh(); err != nil {
				return nil
			}
		}
	}
}
