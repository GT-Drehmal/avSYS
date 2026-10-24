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
	// What files others have open is only for those who can open them.
	seesFiles := map[string]bool{}
	slugs := map[string]string{}
	refresh := func() error {
		orgs, err := s.Store.OrgsForUser(ctx, u)
		if err != nil {
			return err
		}
		clear(allowed)
		clear(seesFiles)
		clear(slugs)
		for _, o := range orgs {
			ns := s.Namespace(o.Slug)
			allowed[ns], seesFiles[ns], slugs[ns] = true, o.Role.AtLeast(store.RoleOperator), o.Slug
		}
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}
	// The tab it is for is there while it is open.
	defer s.streamOpened(u, r.URL.Query().Get("tab"))()

	ch := s.Events.subscribe()
	defer s.Events.unsubscribe(ch)
	send := sse(w)
	// Whether the cluster can be reached, first: a browser that connected
	// again may have missed its change.
	if err := send("cluster", s.reach.view()); err != nil {
		return nil
	}
	// Who is on its organizations' servers, as it may have missed them come
	// and go.
	for _, v := range s.presenceOf(slugs) {
		if !seesFiles[s.Namespace(v.Org)] {
			v = v.withoutFiles()
		}
		if err := send("presence", v); err != nil {
			return nil
		}
	}
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
			if v, ok := e.Data.(presenceView); ok && !seesFiles[e.Namespace] {
				e.Data = v.withoutFiles()
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
