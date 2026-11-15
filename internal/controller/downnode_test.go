package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A server moved off a node that is down, from a snapshot. Its pod there is
// removed at once: a node that is down never says it has stopped it, and
// its volume is kept for as long as the pod is there. The volume made in
// place of the old one is a new one, on the node it goes to, even when the
// node comes back before the old one is gone; and with the pod gone, the
// node that comes back has nothing of the server's to start again.
func TestMoveOffANodeThatIsDown(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := node("a", "16Gi", "", func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionUnknown })
	b := node("b", "16Gi", "")
	a.Labels[corev1.LabelHostname], b.Labels[corev1.LabelHostname] = "a", "b"
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-world"},
		Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}},
		}}}}},
	}
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "world", UID: "world-uid", Annotations: map[string]string{
		avsysv1.AnnotationNode: "b", avsysv1.AnnotationMoving: "b", avsysv1.AnnotationRestoring: "abcd1234",
	}}, Spec: avsysv1.MinecraftServerSpec{EULA: true, State: avsysv1.StateRunning}}
	// The finalizer stands in for the node, which has to say a pod has
	// stopped before it is gone, and cannot.
	const nodeSaysStopped = "test.avsys.dev/node-says-stopped"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "world", Finalizers: []string{nodeSaysStopped}}, Spec: corev1.PodSpec{NodeName: "a"}}
	// And this one for the cluster, which keeps a volume while a pod uses it.
	const inUse = "kubernetes.io/pvc-protection"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: DataPVCName("world"), Finalizers: []string{inUse}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name}}

	forced := 0
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&avsysv1.MinecraftServer{}).WithObjects(&a, &b, pv, pvc, mc, pod).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		o := &client.DeleteOptions{}
		o.ApplyOptions(opts)
		if _, isPod := obj.(*corev1.Pod); isPod && o.GracePeriodSeconds != nil && *o.GracePeriodSeconds == 0 {
			// Removed at once, whatever its node says.
			forced++
			now := &corev1.Pod{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), now); err == nil {
				now.Finalizers = nil
				if err := c.Update(ctx, now); client.IgnoreNotFound(err) != nil {
					return err
				}
			}
		}
		return c.Delete(ctx, obj, opts...)
	}})
	r := &Reconciler{Client: c, Reader: c, Scheme: scheme}
	reconcile := func() {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mc)}); err != nil {
			t.Fatal(err)
		}
	}
	// What the cluster does once nothing uses the volume.
	letGo := func() {
		t.Helper()
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
			return
		}
		now := &corev1.PersistentVolumeClaim{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(pvc), now); err != nil {
			return
		}
		now.Finalizers = nil
		if err := c.Update(ctx, now); client.IgnoreNotFound(err) != nil {
			t.Fatal(err)
		}
	}

	reconcile()
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("its pod on the node that is down is still there, waiting for that node (%v)", err)
	}
	if forced != 1 {
		t.Errorf("removed at once %d times, want once", forced)
	}
	old := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pvc), old); err != nil || old.DeletionTimestamp.IsZero() {
		t.Fatalf("its old volume is not on its way out: %v", err)
	}

	// The node comes back while the old volume is still going: nothing is
	// made in its place yet, and the old one is not taken up again.
	now := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(&a), now); err != nil {
		t.Fatal(err)
	}
	now.Status.Conditions[0].Status = corev1.ConditionTrue
	if err := c.Update(ctx, now); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("a pod was made while its old volume is still going: %v", err)
	}

	// Gone: a new volume is made, tied to no volume of before, and its pod
	// is kept to b.
	letGo()
	reconcile()
	made := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pvc), made); err != nil {
		t.Fatal(err)
	}
	if made.Spec.VolumeName != "" || !made.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(made, mc) {
		t.Errorf("the volume is not a new one of the server: bound to %q, owners %v", made.Spec.VolumeName, made.OwnerReferences)
	}
	again := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), again); err != nil {
		t.Fatal(err)
	}
	terms := again.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if again.Spec.NodeName == "a" || len(terms) != 1 || terms[0].MatchFields[0].Values[0] != "b" {
		t.Errorf("its pod is not kept to b: on %q, %+v", again.Spec.NodeName, terms)
	}
	// Held off until the snapshot's files are in.
	if hasContainer(again, MinecraftContainer) {
		t.Error("the game was started on the new, empty volume")
	}
}
