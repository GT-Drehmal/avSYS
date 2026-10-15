package controller

import (
	"context"
	"math"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// Placement is decided once, by avSYS rather than by the scheduler's scoring.
// A game server's speed is mostly the speed of one CPU core, which Kubernetes
// does not know, and with node-local storage the first choice is permanent.
// So nodes carry a measured score, and a new server goes to the best-scoring
// node that still has the memory for it, falling down the ranking only when
// the better nodes are full.

// workspaceMemory is what the workspace container requests, on top of the game.
var workspaceMemory = resource.MustParse("32Mi")

// nodeUsable reports whether game servers may be placed on a node
// automatically: it is ready, and not kept out of placement by its label.
func nodeUsable(n *corev1.Node) bool {
	return n.Labels[avsysv1.LabelNodeHosts] != "false" && NodeReady(n)
}

func nodeScore(n *corev1.Node) float64 {
	score, err := strconv.ParseFloat(n.Labels[avsysv1.LabelNodeScore], 64)
	if err != nil || score < 0 || math.IsNaN(score) || math.IsInf(score, 0) {
		// Unmeasured nodes can host, after every measured one.
		return 0
	}
	return score
}

// pickNode returns the best node with at least need bytes of memory not yet
// promised to other pods, or "" if none has. reserved is the memory requested
// by the pods already on each node.
func pickNode(nodes []corev1.Node, reserved map[string]int64, need int64) string {
	type candidate struct {
		name  string
		score float64
		free  int64
	}
	var fits []candidate
	for i := range nodes {
		n := &nodes[i]
		if !nodeUsable(n) {
			continue
		}
		free := n.Status.Allocatable.Memory().Value() - reserved[n.Name]
		if free >= need {
			fits = append(fits, candidate{n.Name, nodeScore(n), free})
		}
	}
	sort.Slice(fits, func(i, j int) bool {
		a, b := fits[i], fits[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.free != b.free {
			return a.free > b.free
		}
		return a.name < b.name
	})
	if len(fits) == 0 {
		return ""
	}
	return fits[0].name
}

// nodeOfVolume returns the node a server's existing volume is tied to, if it
// is tied to one, as node-local storage is.
func (r *Reconciler) nodeOfVolume(ctx context.Context, mc *avsysv1.MinecraftServer) (string, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Namespace: mc.Namespace, Name: DataPVCName(mc.Name)}, pvc)
	if apierrors.IsNotFound(err) || (err == nil && pvc.Spec.VolumeName == "") {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	pv := &corev1.PersistentVolume{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Name: pvc.Spec.VolumeName}, pv); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return "", nil
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == corev1.LabelHostname && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) == 1 {
				return expr.Values[0], nil
			}
		}
	}
	return "", nil
}

// ensurePlacement records the node a server lives on, choosing one for a new
// server. Servers that select nodes themselves are left to the scheduler.
func (r *Reconciler) ensurePlacement(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	if len(mc.Spec.NodeSelector) > 0 || mc.Annotations[avsysv1.AnnotationNode] != "" {
		return nil
	}

	// A server that already has a home keeps it: one that predates this
	// record, or whose record was removed.
	node, err := r.nodeOfVolume(ctx, mc)
	if err != nil {
		return err
	}
	if node == "" {
		pod := &corev1.Pod{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(mc), pod); err == nil {
			node = pod.Spec.NodeName
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}

	if node == "" {
		nodes, reserved, err := NodeUsage(ctx, r.Reader)
		if err != nil {
			return err
		}
		node = pickNode(nodes, reserved, ServerNeeds(mc.Spec.Resources.Memory))
		if node == "" {
			// Nothing fits. Leave the pod unpinned so the scheduler reports
			// why, and so it starts as soon as room appears anywhere.
			log.FromContext(ctx).Info("no node has room for the server", "memory", mc.Spec.Resources.Memory.String())
			return nil
		}
		log.FromContext(ctx).Info("placed server", "node", node)
	}

	orig := mc.DeepCopy()
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	mc.Annotations[avsysv1.AnnotationNode] = node
	return r.Patch(ctx, mc, client.MergeFrom(orig))
}

// NodeUsage lists the cluster's nodes and the memory the pods already on
// each have asked for.
func NodeUsage(ctx context.Context, reader client.Reader) ([]corev1.Node, map[string]int64, error) {
	var nodes corev1.NodeList
	if err := reader.List(ctx, &nodes); err != nil {
		return nil, nil, err
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods); err != nil {
		return nil, nil, err
	}
	reserved := map[string]int64{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		reserved[p.Spec.NodeName] += podMemory(p)
	}
	return nodes.Items, reserved, nil
}

// podMemory is the memory the scheduler holds on a node for a pod: its
// containers and the init containers that keep running beside them, or, if
// more, any one init container that runs before them with those started so
// far; or what the pod as a whole asks for; and the pod's overhead.
func podMemory(p *corev1.Pod) int64 {
	var running, sidecars, most int64
	for _, c := range p.Spec.Containers {
		running += c.Resources.Requests.Memory().Value()
	}
	for _, c := range p.Spec.InitContainers {
		m := c.Resources.Requests.Memory().Value()
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars += m
			most = max(most, sidecars)
			continue
		}
		most = max(most, sidecars+m)
	}
	total := max(running+sidecars, most)
	// Asked for the pod as a whole, where the cluster allows that, it is
	// what counts.
	if p.Spec.Resources != nil {
		if mem, ok := p.Spec.Resources.Requests[corev1.ResourceMemory]; ok {
			total = mem.Value()
		}
	}
	if mem, ok := p.Spec.Overhead[corev1.ResourceMemory]; ok {
		total += mem.Value()
	}
	return total
}

// ServerNeeds is the memory a server's pod asks a node for: its game and
// its workspace.
func ServerNeeds(memory resource.Quantity) int64 {
	return memory.Value() + workspaceMemory.Value()
}

// PickNode is the node automatic placement would choose for a server that
// needs need bytes, or "" if none has room.
func PickNode(nodes []corev1.Node, reserved map[string]int64, need int64) string {
	return pickNode(nodes, reserved, need)
}

// NodeUsable reports whether game servers may be placed on a node
// automatically.
func NodeUsable(n *corev1.Node) bool { return nodeUsable(n) }

// NodeReady reports whether a node is up and takes pods, whether or not it
// is kept out of automatic placement. Servers carry no tolerations, so any
// taint that repels pods rules it out; that is also what an unreachable
// node looks like.
func NodeReady(n *corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// NodeScore is a node's measured single-core speed, 0 if unmeasured.
func NodeScore(n *corev1.Node) float64 { return nodeScore(n) }
