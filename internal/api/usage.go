package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// How often each running server's use of memory and CPU is noted, and how
// much of that is kept: six hours, in the panel's memory. A panel that
// restarts starts over.
const (
	usageEvery = 30 * time.Second
	usageKept  = 6 * time.Hour
)

// usageSample is what a server's game was using at one moment.
type usageSample struct {
	At time.Time `json:"t"`
	// CPU is in thousandths of a core.
	CPU int64 `json:"cpu"`
	// Memory is in bytes: what the game holds, not counting what the
	// system could take back from it.
	Memory  int64 `json:"memory"`
	Players int32 `json:"players"`
}

// usage keeps the samples of each server, by namespace/name.
type usage struct {
	mu      sync.Mutex
	samples map[string]*history
	// available is whether the cluster could last be asked, and checked
	// when; a cluster without a metrics API is asked again now and then.
	available bool
	checked   time.Time
	// nodes is what each node was last using in all, by name.
	nodes map[string]nodeInUse
}

// nodeInUse is what a node is using in all, of everything that runs on it.
type nodeInUse struct {
	// CPU is in thousandths of a core.
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

func (u *usage) setNodes(nodes map[string]nodeInUse) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.nodes = nodes
}

// node returns what a node was last using, if that is known.
func (u *usage) node(name string) (nodeInUse, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	n, ok := u.nodes[name]
	return n, ok
}

// history is one server's samples, oldest first. uid is the server's: one
// made under the name of one deleted does not take on its history.
type history struct {
	uid     types.UID
	samples []usageSample
}

func (u *usage) add(key string, uid types.UID, sample usageSample) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.samples == nil {
		u.samples = map[string]*history{}
	}
	h := u.samples[key]
	if h == nil || h.uid != uid {
		h = &history{uid: uid}
		u.samples[key] = h
	}
	h.samples = append(h.samples, sample)
}

// of returns a copy of a server's samples, oldest first.
func (u *usage) of(key string, uid types.UID) []usageSample {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := []usageSample{}
	if h := u.samples[key]; h != nil && h.uid == uid {
		out = append(out, h.samples...)
	}
	return out
}

// keep lets go of the samples of servers that are not in servers, by
// namespace/name and uid, and of any older than usageKept before now,
// also of servers that have not run since.
func (u *usage) keep(servers map[string]types.UID, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for key, h := range u.samples {
		if uid, ok := servers[key]; !ok || uid != h.uid {
			delete(u.samples, key)
			continue
		}
		cut := 0
		for cut < len(h.samples) && now.Sub(h.samples[cut].At) > usageKept {
			cut++
		}
		if cut == len(h.samples) {
			delete(u.samples, key)
		} else {
			h.samples = h.samples[cut:]
		}
	}
}

func (u *usage) setAvailable(ok bool, at time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.available, u.checked = ok, at
}

func (u *usage) state() (available bool, checked time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.available, u.checked
}

// podMetrics is the part of the metrics API's answer that is read.
type podMetrics struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Timestamp  time.Time `json:"timestamp"`
		Containers []struct {
			Name  string            `json:"name"`
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

// RunUsage notes what each running server's game is using, until ctx ends.
func (s *Server) RunUsage(ctx context.Context) {
	for {
		s.usageTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(usageEvery):
		}
	}
}

func (s *Server) usageTick(ctx context.Context, now time.Time) {
	if s.Kube == nil {
		return
	}
	// A cluster that had no metrics API is asked again every few minutes,
	// for one installed meanwhile.
	if available, checked := s.usage.state(); !available && !checked.IsZero() && now.Sub(checked) < 5*time.Minute {
		return
	}
	rest := s.Kube.Discovery().RESTClient()
	if rest == nil {
		return
	}
	// The panel's own pods only, not every pod in the cluster.
	raw, err := rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/pods").Param("labelSelector", "app.kubernetes.io/managed-by=avsys").Do(ctx).Raw()
	if err != nil {
		// No metrics API, or none the panel may read: asked again in a few
		// minutes. Anything else is asked again at the next sample, and
		// what is known meanwhile is still shown.
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			if available, _ := s.usage.state(); available {
				s.Log.Warn("the cluster no longer says what servers are using", "error", err)
			}
			s.usage.setAvailable(false, now)
		} else {
			s.Log.Warn("could not read what servers are using", "error", err)
		}
		return
	}
	var servers avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &servers); err != nil {
		return
	}
	if err := s.noteUsage(raw, servers.Items, now); err != nil {
		s.Log.Warn("could not read what servers are using", "error", err)
		return
	}
	s.usage.setAvailable(true, now)
	raw, err = rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").Do(ctx).Raw()
	if err != nil {
		s.Log.Warn("could not read what nodes are using", "error", err)
		return
	}
	nodes, err := nodesInUse(raw)
	if err != nil {
		s.Log.Warn("could not read what nodes are using", "error", err)
		return
	}
	s.usage.setNodes(nodes)
}

// nodesInUse reads the metrics API's list of nodes.
func nodesInUse(raw []byte) (map[string]nodeInUse, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage map[string]string `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	nodes := map[string]nodeInUse{}
	for _, n := range list.Items {
		cpu, memory := quantityOf(n.Usage["cpu"]), quantityOf(n.Usage["memory"])
		nodes[n.Metadata.Name] = nodeInUse{CPU: cpu.MilliValue(), Memory: memory.Value()}
	}
	return nodes, nil
}

// noteUsage notes, from the metrics API's list of pods, what each of these
// servers' games is using, and lets go of what was noted of servers that
// are gone.
func (s *Server) noteUsage(raw []byte, servers []avsysv1.MinecraftServer, now time.Time) error {
	var list podMetrics
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	known := map[string]*avsysv1.MinecraftServer{}
	for i := range servers {
		mc := &servers[i]
		known[mc.Namespace+"/"+mc.Name] = mc
	}
	for _, pod := range list.Items {
		key := pod.Metadata.Namespace + "/" + pod.Metadata.Name
		mc := known[key]
		if mc == nil || !strings.HasPrefix(pod.Metadata.Namespace, s.Config.NamespacePrefix) {
			continue
		}
		for _, c := range pod.Containers {
			// The game's own: the workspace beside it is the panel's.
			if c.Name != controller.MinecraftContainer {
				continue
			}
			cpu, memory := quantityOf(c.Usage["cpu"]), quantityOf(c.Usage["memory"])
			sample := usageSample{At: pod.Timestamp, CPU: cpu.MilliValue(), Memory: memory.Value()}
			if sample.At.IsZero() {
				sample.At = now
			}
			if p := mc.Status.Players; p != nil {
				sample.Players = p.Online
			}
			s.usage.add(key, mc.UID, sample)
		}
	}
	keep := map[string]types.UID{}
	for key, mc := range known {
		keep[key] = mc.UID
	}
	s.usage.keep(keep, now)
	return nil
}

func quantityOf(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return resource.Quantity{}
	}
	return q
}

// usageView is what a server's page draws its use of memory and CPU from.
type usageView struct {
	// Available is false where the cluster has no metrics API: nothing is
	// known of what servers use.
	Available bool `json:"available"`
	// Limit is the memory the game may use, in bytes.
	Limit   int64         `json:"limit"`
	Every   int           `json:"every"`
	Samples []usageSample `json:"samples"`
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	available, _ := s.usage.state()
	writeJSON(w, http.StatusOK, usageView{
		Available: available,
		Limit:     mc.Spec.Resources.Memory.Value(),
		Every:     int(usageEvery / time.Second),
		Samples:   s.usage.of(a.Namespace+"/"+mc.Name, mc.UID),
	})
	return nil
}
