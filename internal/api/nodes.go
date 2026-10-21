package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Where a server runs is chosen by the panel unless whoever makes it picks
// a node. Any node that is up can be picked, a slow one included: the list
// says how fast each is and how much memory it has free, which is enough to
// choose knowingly. A server's world lives on its node's disk, so the choice
// is made once, when it is made.

type nodeServer struct {
	Org     string `json:"org"`
	Name    string `json:"name"`
	Memory  string `json:"memory"`
	Phase   string `json:"phase,omitempty"`
	Players int32  `json:"players,omitempty"`
}

type nodeView struct {
	Name string `json:"name"`
	// Ready is whether it is up and takes pods.
	Ready bool `json:"ready"`
	// Automatic is whether new servers may be placed on it without asking.
	Automatic bool `json:"automatic"`
	// Hosts is whether it is set to take new servers, up or not.
	Hosts  bool    `json:"hosts"`
	Score  float64 `json:"score"`
	Memory int64   `json:"memory"`
	Free   int64   `json:"free"`
	// Cores is how much CPU it has for pods, in thousandths of a core.
	Cores int64 `json:"cores"`
	// InUse is what it is using in all, where the cluster says.
	InUse *nodeInUse `json:"inUse,omitempty"`
	// Players is how many are on the servers it runs, for the panel's
	// administrator, who is shown those servers.
	Players *int32 `json:"players,omitempty"`
	// Servers are those it runs, shown to the panel's administrator.
	Servers []nodeServer `json:"servers,omitempty"`
}

// nodeReader reads nodes and pods as they are, past any cache that may not
// be watching them.
func (s *Server) nodeReader() client.Reader {
	if s.Reader != nil {
		return s.Reader
	}
	return s.Client
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request, u *store.User) error {
	nodes, reserved, err := controller.NodeUsage(r.Context(), s.nodeReader())
	if err != nil {
		return err
	}
	var servers []avsysv1.MinecraftServer
	if u.IsAdmin {
		var list avsysv1.MinecraftServerList
		if err := s.Client.List(r.Context(), &list); err == nil {
			servers = list.Items
		}
	}
	out := make([]nodeView, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		total := n.Status.Allocatable.Memory().Value()
		v := nodeView{
			Name:      n.Name,
			Ready:     controller.NodeReady(n),
			Automatic: controller.NodeUsable(n),
			Hosts:     n.Labels[avsysv1.LabelNodeHosts] != "false",
			Score:     controller.NodeScore(n),
			Memory:    total,
			Free:      max(0, total-reserved[n.Name]),
			Cores:     n.Status.Allocatable.Cpu().MilliValue(),
		}
		if used, ok := s.usage.node(n.Name); ok {
			if available, _ := s.usage.state(); available {
				v.InUse = &used
			}
		}
		if u.IsAdmin {
			v.Players = new(int32)
		}
		for _, mc := range servers {
			if mc.Status.Node == n.Name || (mc.Status.Node == "" && mc.Annotations[avsysv1.AnnotationNode] == n.Name) {
				org, _ := s.orgOfNamespace(mc.Namespace)
				on := nodeServer{Org: org, Name: mc.Name, Memory: mc.Spec.Resources.Memory.String(), Phase: string(mc.Status.Phase)}
				if mc.Status.Players != nil {
					on.Players = mc.Status.Players.Online
				}
				*v.Players += on.Players
				v.Servers = append(v.Servers, on)
			}
		}
		out = append(out, v)
	}
	// Fastest first, as automatic placement prefers them.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	answer := map[string]any{"nodes": out}
	if u.IsAdmin {
		running, last := s.bench.state()
		answer["measuring"] = running
		if last != nil {
			answer["measured"] = last
		}
	}
	// Which node automatic placement would choose now, for the memory asked
	// about, so that "Automatic" can say where that is.
	if m := r.URL.Query().Get("memory"); m != "" {
		if q, err := resource.ParseQuantity(m); err == nil {
			answer["automatic"] = controller.PickNode(nodes, reserved, controller.ServerNeeds(q))
		}
	}
	writeJSON(w, http.StatusOK, answer)
	return nil
}

// checkNode says whether a new server of this much memory can be put on the
// node asked for: it exists, is up, and has the room.
func (s *Server) checkNode(ctx context.Context, name string, memory resource.Quantity) error {
	nodes, reserved, err := controller.NodeUsage(ctx, s.nodeReader())
	if err != nil {
		return err
	}
	var node *corev1.Node
	for i := range nodes {
		if nodes[i].Name == name {
			node = &nodes[i]
		}
	}
	if node == nil {
		return errBadRequest("There is no node named %q.", name)
	}
	if !controller.NodeReady(node) {
		return errBadRequest("%s is not taking new servers right now.", name)
	}
	free := node.Status.Allocatable.Memory().Value() - reserved[name]
	if need := controller.ServerNeeds(memory); free < need {
		return errBadRequest("%s has %s free, but the server needs %s. Use less memory or another node.", name, gib(free), gib(need))
	}
	return nil
}

// createPlaced creates a server, put on the node given if there is one. That
// node is checked to have room for it again here, one at a time from the
// check until the server is there and counts on it, with the controller's
// own placing too: two placed at once do not both get the same room.
func (s *Server) createPlaced(ctx context.Context, mc *avsysv1.MinecraftServer, node string) error {
	if node != "" {
		defer controller.LockPlacement()()
		if err := s.checkNode(ctx, node, mc.Spec.Resources.Memory); err != nil {
			return err
		}
	}
	return s.Client.Create(ctx, mc)
}

func gib(b int64) string {
	return fmt.Sprintf("%.1f GB", float64(max(b, 0))/(1<<30))
}
