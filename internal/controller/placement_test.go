package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func node(name, memory, score string, mutate ...func(*corev1.Node)) corev1.Node {
	n := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	if score != "" {
		n.Labels[avsysv1.LabelNodeScore] = score
	}
	for _, m := range mutate {
		m(&n)
	}
	return n
}

const gi = int64(1) << 30

func TestPickNode(t *testing.T) {
	fast, mid, slow := node("fast", "64Gi", "5440"), node("mid", "32Gi", "4470"), node("slow", "8Gi", "1900")
	all := []corev1.Node{slow, mid, fast}

	cases := []struct {
		name     string
		nodes    []corev1.Node
		reserved map[string]int64
		need     int64
		want     string
	}{
		{"fastest when it has room", all, nil, 4 * gi, "fast"},
		// Strict tiers: a busy fast node still wins while the server fits.
		{"fastest even when nearly full", all, map[string]int64{"fast": 59 * gi}, 4 * gi, "fast"},
		{"next tier once the fastest is full", all, map[string]int64{"fast": 61 * gi}, 4 * gi, "mid"},
		{"down again when both are full", all, map[string]int64{"fast": 61 * gi, "mid": 30 * gi}, 4 * gi, "slow"},
		{"nothing fits", all, nil, 100 * gi, ""},
		{"unmeasured nodes come last", []corev1.Node{node("unknown", "64Gi", ""), slow}, nil, 4 * gi, "slow"},
		{"but can still host", []corev1.Node{node("unknown", "64Gi", "")}, nil, 4 * gi, "unknown"},
		{"equal scores: the roomier one", []corev1.Node{node("a", "16Gi", "100"), node("b", "32Gi", "100")}, nil, 4 * gi, "b"},
		{"excluded by label", []corev1.Node{node("fast", "64Gi", "5440", func(n *corev1.Node) { n.Labels[avsysv1.LabelNodeHosts] = "false" }), mid}, nil, 4 * gi, "mid"},
		{"cordoned", []corev1.Node{node("fast", "64Gi", "5440", func(n *corev1.Node) { n.Spec.Unschedulable = true }), mid}, nil, 4 * gi, "mid"},
		{"not ready", []corev1.Node{node("fast", "64Gi", "5440", func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionUnknown }), mid}, nil, 4 * gi, "mid"},
		{"tainted", []corev1.Node{node("fast", "64Gi", "5440", func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/unreachable", Effect: corev1.TaintEffectNoSchedule}}
		}), mid}, nil, 4 * gi, "mid"},
	}
	for _, c := range cases {
		if got := pickNode(c.nodes, c.reserved, c.need); got != c.want {
			t.Errorf("%s: picked %q, want %q", c.name, got, c.want)
		}
	}
}

// A node's score that is not a number counts as not measured.
func TestNodeScoreNotANumber(t *testing.T) {
	for _, label := range []string{"NaN", "Inf", "-Inf", "-1", "fast"} {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{avsysv1.LabelNodeScore: label}}}
		if got := NodeScore(n); got != 0 {
			t.Errorf("score %q read as %v", label, got)
		}
	}
}

// What the scheduler holds for a pod: its containers and sidecars, or more
// if an init container needs it, and its overhead.
func TestPodMemory(t *testing.T) {
	gi := func(n int64) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: *resource.NewQuantity(n<<30, resource.BinarySI)}}
	}
	always := corev1.ContainerRestartPolicyAlways
	for _, c := range []struct {
		name string
		pod  corev1.PodSpec
		want int64
	}{
		{"containers", corev1.PodSpec{Containers: []corev1.Container{{Resources: gi(2)}, {Resources: gi(1)}}}, 3},
		{"a bigger init container", corev1.PodSpec{InitContainers: []corev1.Container{{Resources: gi(5)}}, Containers: []corev1.Container{{Resources: gi(2)}}}, 5},
		{"a smaller init container", corev1.PodSpec{InitContainers: []corev1.Container{{Resources: gi(1)}}, Containers: []corev1.Container{{Resources: gi(2)}}}, 2},
		{"a sidecar", corev1.PodSpec{InitContainers: []corev1.Container{{Resources: gi(1), RestartPolicy: &always}}, Containers: []corev1.Container{{Resources: gi(2)}}}, 3},
		{"the pod as a whole", corev1.PodSpec{Containers: []corev1.Container{{Resources: gi(2)}}, Resources: &corev1.ResourceRequirements{Requests: gi(6).Requests}}, 6},
		{"overhead", corev1.PodSpec{Containers: []corev1.Container{{Resources: gi(2)}}, Overhead: corev1.ResourceList{corev1.ResourceMemory: *resource.NewQuantity(1<<30, resource.BinarySI)}}, 3},
	} {
		if got := podMemory(&corev1.Pod{Spec: c.pod}); got != c.want<<30 {
			t.Errorf("%s: %d GiB, want %d", c.name, got>>30, c.want)
		}
	}
}

// A server given a node counts there from then on, though its pod is not
// made or bound yet, or it is being made from a snapshot; one bound counts
// what its pod has yet to ask for.
func TestNodeUsageCountsPlacedServers(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	n, b := node("a", "16Gi", ""), node("b", "16Gi", "")
	yes := true
	server := func(name, state, node string) *avsysv1.MinecraftServer {
		mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: name, Annotations: map[string]string{avsysv1.AnnotationNode: node}}}
		mc.Spec.State = avsysv1.DesiredState(state)
		mc.Spec.Resources.Memory = resource.MustParse("4Gi")
		return mc
	}
	// Stopped, its workspace bound: only the workspace counts until it runs.
	workspace := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "starting"},
		Spec:       corev1.PodSpec{NodeName: "a", Containers: []corev1.Container{{Name: "w", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}}}},
	}
	creating := server("copy", "Stopped", "a")
	creating.Annotations[avsysv1.AnnotationCreating] = "abcd1234"
	creating.Annotations[avsysv1.AnnotationRestoring] = "abcd1234"
	asleep := server("asleep", "Running", "a")
	asleep.Annotations[avsysv1.AnnotationAsleep] = "true"
	// Making it failed: it waits to be deleted, and holds nothing.
	failed := server("failed", "Stopped", "a")
	failed.Annotations[avsysv1.AnnotationCreating] = "abcd1234"
	failed.Status.Snapshots = &avsysv1.SnapshotStatus{Error: "making this server from the snapshot did not finish"}
	// Made again under the name of a deleted server whose pod, on node b,
	// is still going: it counts on its own node, a.
	again := server("again", "Running", "a")
	again.UID = "new"
	leftover := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "again", OwnerReferences: []metav1.OwnerReference{{APIVersion: "avsys.dev/v1alpha1", Kind: "MinecraftServer", Name: "again", UID: "old", Controller: &yes}}},
		Spec:       corev1.PodSpec{NodeName: "b", Containers: []corev1.Container{{Name: "w", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&n, workspace,
		server("new", "Running", "a"),      // placed, no pod yet: 4Gi + 32Mi
		server("starting", "Running", "a"), // bound workspace of 32Mi: 4Gi more
		server("stopped", "Stopped", "a"),  // nothing
		creating,                           // being made from a snapshot, held stopped: 4Gi + 32Mi
		asleep,                             // gives its memory back: nothing
		failed, again, leftover, &b,
	).Build()
	_, reserved, err := NodeUsage(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := 4*(4*gi) + 4*(32<<20)
	if reserved["a"] != want {
		t.Errorf("reserved on a = %d, want %d", reserved["a"], want)
	}
	if reserved["b"] != 32<<20 {
		t.Errorf("reserved on b = %d, want only the leftover pod's %d", reserved["b"], 32<<20)
	}
}

// A volume tied to a node by its hostname label is on the node of that
// label, whose name may differ.
func TestNodeNamedByHostnameLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	n := node("node-1", "16Gi", "")
	n.Labels[corev1.LabelHostname] = "host-1"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&n).Build()
	r := &Reconciler{Client: c, Reader: c}
	if got, err := r.nodeNamed(context.Background(), "host-1"); err != nil || got != "node-1" {
		t.Errorf("nodeNamed(host-1) = %q, %v; want node-1", got, err)
	}
	if got, _ := r.nodeNamed(context.Background(), "unknown"); got != "unknown" {
		t.Errorf("nodeNamed(unknown) = %q; want it as given", got)
	}
}
