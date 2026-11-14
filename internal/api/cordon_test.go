package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
)

// cordonPanel is a panel whose server alpha/world has its files on node a,
// with node b beside it, and a cluster that runs: the controller, and a
// scheduler and kubelet in one, which put a pod on the node it is kept to
// unless that node is cordoned and the pod does not tolerate it, and have
// every container of a pod put there ready at once.
type cordonPanel struct {
	*panel
	world client.ObjectKey
}

var cordonTaint = corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}

func newCordonPanel(t *testing.T) *cordonPanel {
	t.Helper()
	p := newPanel(t)
	ctx, cancel := context.WithCancel(context.Background())
	p.srv.Backups.Restic = "true"
	c := p.srv.Client
	for _, name := range []string{"a", "b"} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelHostname: name}}, Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Gi")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		}}
		if err := c.Create(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-world"},
		Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}},
		}}}}},
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world-data"}, Spec: corev1.PersistentVolumeClaimSpec{
		VolumeName: pv.Name,
		Resources:  corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Ti")}},
	}}
	for _, obj := range []client.Object{pv, pvc} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	mc := &avsysv1.MinecraftServer{}
	if err := c.Get(ctx, key, mc); err != nil {
		t.Fatal(err)
	}
	mc.Annotations = map[string]string{avsysv1.AnnotationNode: "a"}
	mc.Spec.State = avsysv1.StateRunning
	if err := c.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}

	r := &controller.Reconciler{Client: c, Reader: c, Scheme: c.Scheme(), Config: controller.Config{WorkspaceImage: "workspace"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_, _ = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			runPod(ctx, c, key)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	d := &cordonPanel{panel: p, world: key}
	d.waitFor("the server running on a", func(mc *avsysv1.MinecraftServer, pod *corev1.Pod) bool {
		return mc.Status.Phase == avsysv1.PhaseRunning && pod != nil && pod.Spec.NodeName == "a"
	})
	return d
}

// runPod is the scheduler and the kubelet, for one pod.
func runPod(ctx context.Context, c client.Client, key client.ObjectKey) {
	pod := &corev1.Pod{}
	if err := c.Get(ctx, key, pod); err != nil || !pod.DeletionTimestamp.IsZero() {
		return
	}
	if pod.Spec.NodeName == "" {
		nodeName := ""
		if a := pod.Spec.Affinity; a != nil && a.NodeAffinity != nil && a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
			nodeName = a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0]
		}
		node := &corev1.Node{}
		if err := c.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
			return
		}
		if node.Spec.Unschedulable && !tolerates(pod, &cordonTaint) {
			return
		}
		pod.Spec.NodeName = nodeName
		if err := c.Update(ctx, pod); err != nil {
			return
		}
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		return
	}
	pod.Status.Phase = corev1.PodRunning
	for _, ctr := range pod.Spec.Containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: ctr.Name, Ready: true, ContainerID: "containerd://" + ctr.Name,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}},
		})
	}
	if err := c.Status().Update(ctx, pod); err != nil {
		_ = c.Update(ctx, pod)
	}
}

func tolerates(pod *corev1.Pod, taint *corev1.Taint) bool {
	for i := range pod.Spec.Tolerations {
		if toleratesTaint(pod.Spec.Tolerations[i], taint) {
			return true
		}
	}
	return false
}

func (d *cordonPanel) cordon(name string, on bool) {
	d.t.Helper()
	ctx := context.Background()
	node := &corev1.Node{}
	if err := d.srv.Client.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
		d.t.Fatal(err)
	}
	node.Spec.Unschedulable = on
	node.Spec.Taints = nil
	if on {
		node.Spec.Taints = []corev1.Taint{cordonTaint}
	}
	if err := d.srv.Client.Update(ctx, node); err != nil {
		d.t.Fatal(err)
	}
}

// waitFor waits for the server and its pod, if it has one, to be as said.
func (d *cordonPanel) waitFor(what string, is func(*avsysv1.MinecraftServer, *corev1.Pod) bool) (*avsysv1.MinecraftServer, *corev1.Pod) {
	d.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		mc := &avsysv1.MinecraftServer{}
		if err := d.srv.Client.Get(context.Background(), d.world, mc); err != nil {
			d.t.Fatal(err)
		}
		var pod *corev1.Pod
		if p := (&corev1.Pod{}); d.srv.Client.Get(context.Background(), d.world, p) == nil {
			pod = p
		}
		if is(mc, pod) {
			return mc, pod
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("not %s: server %+v %v, pod %+v", what, mc.Status, mc.Annotations, pod)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (d *cordonPanel) move(node string) (int, string, string) {
	d.t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/orgs/alpha/servers/world/move?wait=true", strings.NewReader(`{"node":"`+node+`"}`))
	req.Header.Set("Authorization", "Bearer "+d.tokens["owner"])
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	var e struct{ Code, Message string }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return rec.Code, e.Code, e.Message
}

// readsFiles stands in for a workspace: it notes the pod its files were
// asked of as that pod was then, and does not answer.
type readsFiles struct {
	c    client.Client
	mu   sync.Mutex
	from []corev1.Pod
}

func (r *readsFiles) DialPod(ctx context.Context, namespace, name string, _ int) (net.Conn, error) {
	pod := &corev1.Pod{}
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pod); err == nil {
		r.mu.Lock()
		r.from = append(r.from, *pod)
		r.mu.Unlock()
	}
	return nil, errors.New("no workspace in this test")
}

func (r *readsFiles) pods() []corev1.Pod {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]corev1.Pod(nil), r.from...)
}

// A server on a node that is cordoned, as before maintenance, is moved off
// it: stopped, its pod is made again there to read its files from, which
// the cordon lets that pod alone do. Nothing else of it runs there.
func TestMoveOffACordonedNode(t *testing.T) {
	was := stopWait
	stopWait = 5 * time.Second
	t.Cleanup(func() { stopWait = was })
	d := newCordonPanel(t)
	reads := &readsFiles{c: d.srv.Client}
	d.srv.Gateway = &sshgw.Gateway{Dialer: reads, WorkspaceTries: 1}
	d.cordon("a", true)

	// The workspace here does not answer, so the move goes as far as
	// reading the files, and is put back from there.
	status, code, message := d.move("b")
	if strings.Contains(message, "did not stop") {
		t.Fatalf("the server was not stopped on the cordoned node to read its files: %d %s %q", status, code, message)
	}
	if !strings.Contains(message, "could not be saved") {
		t.Fatalf("the move ended with %d %s %q, want it to fail at reading the files", status, code, message)
	}
	pods := reads.pods()
	if len(pods) == 0 {
		t.Fatal("the server's files were never asked for")
	}
	for _, pod := range pods {
		if pod.Spec.NodeName != "a" || hasGame(&pod) || !tolerates(&pod, &cordonTaint) {
			t.Errorf("read from a pod on %q, game %v, tolerations %+v", pod.Spec.NodeName, hasGame(&pod), pod.Spec.Tolerations)
		}
		if len(pod.Spec.Tolerations) != 1 {
			t.Errorf("the pod read from tolerates more than the cordon: %+v", pod.Spec.Tolerations)
		}
	}

	// Put back as it was, running: its pod is no longer let onto a, and
	// waits there as any other would.
	d.waitFor("put back, waiting for a", func(mc *avsysv1.MinecraftServer, pod *corev1.Pod) bool {
		_, marked := mc.Annotations[avsysv1.AnnotationMoveWas]
		return mc.Spec.State == avsysv1.StateRunning && !marked && pod != nil && hasGame(pod) && pod.Spec.NodeName == "" && len(pod.Spec.Tolerations) == 0
	})
}

func hasGame(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == controller.MinecraftContainer {
			return true
		}
	}
	return false
}

// A cordoned node is shown as such, and takes no server moved to it.
func TestMoveToACordonedNodeIsRefused(t *testing.T) {
	d := newCordonPanel(t)
	d.cordon("b", true)
	status, code, message := d.move("b")
	if status != 400 || message != "b is cordoned. Choose another node." {
		t.Errorf("moving to a cordoned node: %d %s %q", status, code, message)
	}
	if mc, _ := d.waitFor("left on a", func(*avsysv1.MinecraftServer, *corev1.Pod) bool { return true }); mc.Annotations[avsysv1.AnnotationNode] != "a" || mc.Spec.State != avsysv1.StateRunning {
		t.Errorf("refused, and changed: %s %v", mc.Spec.State, mc.Annotations)
	}

	req := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+d.tokens["root"])
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	var listed struct{ Nodes []nodeView }
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	got := map[string][2]bool{}
	for _, n := range listed.Nodes {
		got[n.Name] = [2]bool{n.Ready, n.Cordoned}
	}
	if got["a"] != [2]bool{true, false} || got["b"] != [2]bool{false, true} {
		t.Errorf("listed as (ready, cordoned): %v", got)
	}
}

// toleratesTaint is what the scheduler asks of a toleration for a taint.
func toleratesTaint(tol corev1.Toleration, taint *corev1.Taint) bool {
	if tol.Effect != "" && tol.Effect != taint.Effect {
		return false
	}
	if tol.Key != "" && tol.Key != taint.Key {
		return false
	}
	switch tol.Operator {
	case corev1.TolerationOpExists:
		return true
	case "", corev1.TolerationOpEqual:
		return tol.Key != "" && tol.Value == taint.Value
	}
	return false
}
