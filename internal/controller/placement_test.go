package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	// Being made smaller in place: what it has now, until it is done.
	resizing := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "game", Resources: gi(2)}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "game", AllocatedResources: gi(4).Requests, Resources: &corev1.ResourceRequirements{Requests: gi(4).Requests},
		}}},
	}
	if got := podMemory(resizing); got != 4<<30 {
		t.Errorf("resizing: %d GiB, want 4", got>>30)
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
	// Making it failed: it waits to be deleted, holding its workspace.
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
		server("stopped", "Stopped", "a"),  // its workspace: 32Mi
		creating,                           // being made from a snapshot, held stopped: 4Gi + 32Mi
		asleep,                             // gives the game's memory back: 32Mi
		failed, again, leftover, &b,
	).Build()
	_, reserved, err := NodeUsage(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// Four to run, and three that hold only their workspace: stopped,
	// asleep, and the one whose making failed.
	want := 4*(4*gi) + 4*(32<<20) + 3*(32<<20)
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
	if got, err := nodeNamed(context.Background(), c, "host-1"); err != nil || got != "node-1" {
		t.Errorf("nodeNamed(host-1) = %q, %v; want node-1", got, err)
	}
	if got, _ := nodeNamed(context.Background(), c, "unknown"); got != "unknown" {
		t.Errorf("nodeNamed(unknown) = %q; want it as given", got)
	}
}

// A server whose record holds its node's hostname label, as an older panel
// wrote it, has it set to the name of the node its volume is on, which its
// pod is kept to: also where that label is another node's name.
func TestRecordedHostnameRenamed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// Node one's hostname label is node two's name.
	one, two := node("one", "16Gi", ""), node("two", "16Gi", "")
	one.Labels[corev1.LabelHostname] = "two"
	two.Labels[corev1.LabelHostname] = "host-of-two"
	objects := []client.Object{&one, &two}
	server := func(name, recorded, label string) *avsysv1.MinecraftServer {
		mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: name, Annotations: map[string]string{avsysv1.AnnotationNode: recorded}}}
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name},
			Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{label}}},
			}}}}},
		}
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: DataPVCName(name)}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name}}
		objects = append(objects, mc, pv, pvc)
		return mc
	}
	cases := map[*avsysv1.MinecraftServer]string{
		server("older", "two", "two"):                 "one", // its volume is on one
		server("named", "two", "host-of-two"):         "two", // already right
		server("plain", "host-of-two", "host-of-two"): "two",
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &Reconciler{Client: c, Reader: c}
	for mc, want := range cases {
		if err := r.ensurePlacement(context.Background(), mc); err != nil {
			t.Fatal(err)
		}
		got := &avsysv1.MinecraftServer{}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(mc), got); err != nil {
			t.Fatal(err)
		}
		if got.Annotations[avsysv1.AnnotationNode] != want {
			t.Errorf("%s: recorded node %q, want %s", mc.Name, got.Annotations[avsysv1.AnnotationNode], want)
		}
	}
}

// A server being moved has its pod and its volume on the node it leaves
// deleted, and nothing made in their place until the volume is gone; a
// volume on the node it goes to, or not yet tied to one, is left alone.
func TestMoveVolume(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a, b := node("a", "16Gi", ""), node("b", "16Gi", "")
	a.Labels[corev1.LabelHostname], b.Labels[corev1.LabelHostname] = "a", "b"
	volumeOn := func(name, on string) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim) {
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: DataPVCName(name)}}
		if on == "" {
			return nil, pvc
		}
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name},
			Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{on}}},
			}}}}},
		}
		pvc.Spec.VolumeName = pv.Name
		return pv, pvc
	}
	for _, c := range []struct {
		name, on    string
		wantWaiting bool
		wantGone    bool
	}{
		{"leaving", "a", true, true},
		{"there", "b", false, false},
		{"untied", "", false, false},
	} {
		mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: c.name, Annotations: map[string]string{avsysv1.AnnotationMoving: "b", avsysv1.AnnotationNode: "b"}}}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: c.name}, Spec: corev1.PodSpec{NodeName: c.on}}
		pv, pvc := volumeOn(c.name, c.on)
		objects := []client.Object{&a, &b, mc, pod, pvc}
		if pv != nil {
			objects = append(objects, pv)
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		r := &Reconciler{Client: cl, Reader: cl}
		waiting, err := r.moveVolume(context.Background(), mc, "b")
		if err != nil {
			t.Fatal(err)
		}
		if waiting != c.wantWaiting {
			t.Errorf("%s: waiting %v, want %v", c.name, waiting, c.wantWaiting)
		}
		podErr := cl.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
		pvcErr := cl.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{})
		if gone := apierrors.IsNotFound(podErr) && apierrors.IsNotFound(pvcErr); gone != c.wantGone {
			t.Errorf("%s: pod and volume deleted %v, want %v (%v, %v)", c.name, gone, c.wantGone, podErr, pvcErr)
		}
		if c.wantGone {
			// With the volume gone, a new one can be made.
			if waiting, _ := r.moveVolume(context.Background(), mc, "b"); waiting {
				t.Errorf("%s: still waiting once the volume is gone", c.name)
			}
		}
	}
}

// A server being moved says so while it is held, and is not put back on the
// node its old volume is on.
func TestMovingServer(t *testing.T) {
	moving := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "moving", Annotations: map[string]string{
		avsysv1.AnnotationNode: "b", avsysv1.AnnotationMoving: "b", avsysv1.AnnotationRestoring: "abcd1234",
	}}}
	// Stopped for the move, as a move does: it says it is being moved.
	moving.Spec.State = avsysv1.StateStopped
	if phase, message := phaseOf(moving, nil); phase != avsysv1.PhaseStopped || message != "Being moved to b, and held stopped until its files are there." {
		t.Errorf("phase %s, %q", phase, message)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := node("a", "16Gi", "")
	a.Labels[corev1.LabelHostname] = "a"
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-moving"},
		Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}},
		}}}}},
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: DataPVCName("moving")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&a, pv, pvc, moving).Build()
	r := &Reconciler{Client: c, Reader: c}
	if err := r.ensurePlacement(context.Background(), moving); err != nil {
		t.Fatal(err)
	}
	got := &avsysv1.MinecraftServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(moving), got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[avsysv1.AnnotationNode] != "b" {
		t.Errorf("recorded node %q while moving to b, want it left", got.Annotations[avsysv1.AnnotationNode])
	}
}

// A move given up, its mark taken off since the cache was read, deletes
// nothing.
func TestMoveVolumeGivenUp(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := node("a", "16Gi", "")
	a.Labels[corev1.LabelHostname] = "a"
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-given-up"},
		Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}},
		}}}}},
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: DataPVCName("given-up")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name}}
	stored := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "given-up", Annotations: map[string]string{avsysv1.AnnotationNode: "a"}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&a, pv, pvc, stored).Build()
	r := &Reconciler{Client: c, Reader: c}
	// What a reconcile read a moment before the move was given up.
	stale := stored.DeepCopy()
	stale.Annotations[avsysv1.AnnotationMoving] = "b"
	if waiting, err := r.moveVolume(context.Background(), stale, "b"); err != nil || waiting {
		t.Fatalf("waiting %v, %v", waiting, err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Errorf("the volume was deleted on a stale mark: %v", err)
	}
}

// A server being moved holds its memory on the node it goes to, held
// stopped meanwhile though it may be.
func TestMovingHoldsMemory(t *testing.T) {
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{avsysv1.AnnotationMoving: "b"}}}
	mc.Spec.State = avsysv1.StateStopped
	if !holdsMemory(mc) {
		t.Error("a server being moved holds no memory")
	}
}

func TestNodeMatches(t *testing.T) {
	n := node("east-1", "16Gi", "")
	n.Labels["topology.kubernetes.io/zone"] = "east"
	req := func(key string, op corev1.NodeSelectorOperator, values ...string) *corev1.NodeSelector {
		return &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: key, Operator: op, Values: values}}}}}
	}
	for what, c := range map[string]struct {
		sel  *corev1.NodeSelector
		want bool
	}{
		"in its zone":      {req("topology.kubernetes.io/zone", corev1.NodeSelectorOpIn, "east"), true},
		"in another zone":  {req("topology.kubernetes.io/zone", corev1.NodeSelectorOpIn, "west"), false},
		"not in another":   {req("topology.kubernetes.io/zone", corev1.NodeSelectorOpNotIn, "west"), true},
		"a label it has":   {req("topology.kubernetes.io/zone", corev1.NodeSelectorOpExists), true},
		"a label it lacks": {req("disk", corev1.NodeSelectorOpExists), false},
		"by its name":      {&corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"east-1"}}}}}}, true},
		"any of two terms": {&corev1.NodeSelector{NodeSelectorTerms: append(req("disk", corev1.NodeSelectorOpExists).NodeSelectorTerms, req("topology.kubernetes.io/zone", corev1.NodeSelectorOpIn, "east").NodeSelectorTerms...)}, true},
	} {
		if got := nodeMatches(&n, c.sel); got != c.want {
			t.Errorf("%s: %v, want %v", what, got, c.want)
		}
	}
}

// A server being moved holds its memory on the node it goes to even while
// its pod is still bound to the one it leaves.
func TestMovingReservesTargetWhileLeaving(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a, b := node("a", "16Gi", ""), node("b", "16Gi", "")
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "leaving", UID: "u", Annotations: map[string]string{avsysv1.AnnotationNode: "b", avsysv1.AnnotationMoving: "b"}}}
	mc.Spec.State = avsysv1.StateStopped
	mc.Spec.Resources.Memory = resource.MustParse("4Gi")
	yes := true
	old := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "leaving", OwnerReferences: []metav1.OwnerReference{{APIVersion: "avsys.dev/v1alpha1", Kind: "MinecraftServer", Name: "leaving", UID: "u", Controller: &yes}}},
		Spec:       corev1.PodSpec{NodeName: "a", Containers: []corev1.Container{{Name: "w", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&a, &b, mc, old).Build()
	_, reserved, err := NodeUsage(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if reserved["b"] != 4*gi+32<<20 {
		t.Errorf("reserved on b = %d, want its game and workspace", reserved["b"])
	}
	if reserved["a"] != 32<<20 {
		t.Errorf("reserved on a = %d, want only the pod still there", reserved["a"])
	}
}
