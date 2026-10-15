package controller

import (
	"context"
	"math"
	"sort"
	"strconv"
	"sync"

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
				return r.nodeNamed(ctx, expr.Values[0])
			}
		}
	}
	return "", nil
}

// placing holds placements to one at a time, the controller's and the
// panel's: from the look at what is free until the server placed is
// recorded on its node, and so counts there for the next.
var placing sync.Mutex

// LockPlacement holds placements until the function it returns is called.
func LockPlacement() (unlock func()) {
	placing.Lock()
	return placing.Unlock
}

// renameRecordedNode puts in a server's record the node its volume is on,
// where that is another: an older panel wrote the node's hostname label
// there, read off the volume, and the pod is now kept to a node by name.
// Node-local files decide where the server can run.
func (r *Reconciler) renameRecordedNode(ctx context.Context, mc *avsysv1.MinecraftServer, recorded string) error {
	node, err := r.nodeOfVolume(ctx, mc)
	if err != nil || node == "" || node == recorded {
		return err
	}
	orig := mc.DeepCopy()
	mc.Annotations[avsysv1.AnnotationNode] = node
	log.FromContext(ctx).Info("recorded node set to where its volume is", "recorded", recorded, "node", node)
	return r.Patch(ctx, mc, client.MergeFrom(orig))
}

// nodeNamed returns the name of the node whose hostname label is this, which
// is usually but not always its name.
func (r *Reconciler) nodeNamed(ctx context.Context, hostname string) (string, error) {
	var nodes corev1.NodeList
	if err := r.Reader.List(ctx, &nodes, client.MatchingLabels{corev1.LabelHostname: hostname}); err != nil {
		return "", err
	}
	if len(nodes.Items) == 1 {
		return nodes.Items[0].Name, nil
	}
	return hostname, nil
}

// ensurePlacement records the node a server lives on, choosing one for a new
// server. Servers that select nodes themselves are left to the scheduler.
func (r *Reconciler) ensurePlacement(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	if len(mc.Spec.NodeSelector) > 0 {
		return nil
	}
	if recorded := mc.Annotations[avsysv1.AnnotationNode]; recorded != "" {
		return r.renameRecordedNode(ctx, mc, recorded)
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
		defer LockPlacement()()
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
// each have asked for. A server given a node that is to run there counts in
// full from then on, before its pod is made or bound: two servers placed a
// moment apart are not both given the same memory. One being made from a
// snapshot counts too, as it may be started once its files are in.
func NodeUsage(ctx context.Context, reader client.Reader) ([]corev1.Node, map[string]int64, error) {
	var nodes corev1.NodeList
	if err := reader.List(ctx, &nodes); err != nil {
		return nil, nil, err
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods); err != nil {
		return nil, nil, err
	}
	var servers avsysv1.MinecraftServerList
	if err := reader.List(ctx, &servers); err != nil {
		return nil, nil, err
	}
	reserved := map[string]int64{}
	bound := map[types.NamespacedName]*corev1.Pod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		reserved[p.Spec.NodeName] += podMemory(p)
		bound[types.NamespacedName{Namespace: p.Namespace, Name: p.Name}] = p
	}
	for i := range servers.Items {
		mc := &servers.Items[i]
		// Every server has a pod for its workspace, stopped or not; one
		// that is to run has its game there too.
		need := workspaceMemory.Value()
		if holdsMemory(mc) {
			need = ServerNeeds(mc.Spec.Resources.Memory)
		}
		// A server's pod has its name. Where it is bound already, only
		// what it has yet to ask for there is added: a stopped server's
		// workspace becoming the game.
		// A pod of its name that another server owns is a deleted one's,
		// on its way out, and not where this one runs.
		if p := bound[client.ObjectKeyFromObject(mc)]; p != nil && !leftOf(p, mc) {
			reserved[p.Spec.NodeName] += max(0, need-podMemory(p))
		} else if node := mc.Annotations[avsysv1.AnnotationNode]; node != "" && len(mc.Spec.NodeSelector) == 0 {
			reserved[node] += need
		}
	}
	return nodes.Items, reserved, nil
}

// holdsMemory says whether a server's memory is kept for it on its node:
// one that is to run, though held stopped for a moment while a restore puts
// its files back, or while it is made from a snapshot. One asleep gives it
// back until woken.
func holdsMemory(mc *avsysv1.MinecraftServer) bool {
	if _, asleep := mc.Annotations[avsysv1.AnnotationAsleep]; asleep {
		return false
	}
	if mc.Annotations[avsysv1.AnnotationCreating] != "" {
		// Until making it failed, which is recorded on it: it then waits
		// to be deleted, and starts on nothing.
		return mc.Status.Snapshots == nil || mc.Status.Snapshots.Error == ""
	}
	return mc.Spec.State != avsysv1.StateStopped
}

// podMemory is the memory the scheduler holds on a node for a pod: its
// containers and the init containers that keep running beside them, or, if
// more, any one init container that runs before them with those started so
// far; or what the pod as a whole asks for; and the pod's overhead.
func podMemory(p *corev1.Pod) int64 {
	// While a container's memory is being changed in place, the scheduler
	// holds the most of what is asked for and what it has now.
	status := map[string]int64{}
	for _, list := range [][]corev1.ContainerStatus{p.Status.ContainerStatuses, p.Status.InitContainerStatuses} {
		for _, cs := range list {
			m := cs.AllocatedResources.Memory().Value()
			if cs.Resources != nil {
				m = max(m, cs.Resources.Requests.Memory().Value())
			}
			status[cs.Name] = m
		}
	}
	request := func(c corev1.Container) int64 { return max(c.Resources.Requests.Memory().Value(), status[c.Name]) }
	var running, sidecars, most int64
	for _, c := range p.Spec.Containers {
		running += request(c)
	}
	for _, c := range p.Spec.InitContainers {
		m := request(c)
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
