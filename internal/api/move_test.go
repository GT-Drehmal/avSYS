package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// While a server is moved, nothing else changes it, and the snapshot that
// carries its files is not deleted.
func TestMoveHoldsTheServer(t *testing.T) {
	s := &Server{}
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-x", Name: "world"}}
	op, ok := s.ops.begin("mc-x", "x", "world", kindMove, "")
	if !ok {
		t.Fatal("the move did not begin")
	}
	if _, err := s.changing(mc, false); !errors.Is(err, errMoving) {
		t.Errorf("changing it during a move: %v, want errMoving", err)
	}
	if !s.ops.carry("mc-x", op, "abcd1234") {
		t.Fatal("the snapshot was not taken up")
	}
	if _, ok := s.ops.remove("mc-x", "abcd1234"); ok {
		t.Error("the snapshot carrying it could be deleted")
	}
	// One already being deleted is not taken up.
	release, ok := s.ops.remove("mc-x", "ffff0000")
	if !ok {
		t.Fatal("another snapshot could not be deleted")
	}
	defer release()
	if s.ops.carry("mc-x", op, "ffff0000") {
		t.Error("a snapshot being deleted was taken up")
	}
}

// A snapshot that a server's interrupted restore or move is to be put back
// from is named as held, whatever this panel remembers.
func TestHeldIn(t *testing.T) {
	p := newPanel(t)
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "halfway", Annotations: map[string]string{avsysv1.AnnotationRestoring: "abcd1234", avsysv1.AnnotationMoving: "b"}}}
	if err := p.srv.Client.Create(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if got, err := p.srv.heldIn(context.Background(), "mc-alpha", "abcd1234"); err != nil || got != "halfway" {
		t.Errorf("held by %q (%v), want halfway", got, err)
	}
	if got, err := p.srv.heldIn(context.Background(), "mc-alpha", "ffff0000"); err != nil || got != "" {
		t.Errorf("another snapshot held by %q (%v)", got, err)
	}
	// Not able to look: not taken as free.
	failing := &Server{Client: interceptor.NewClient(p.srv.Client.(client.WithWatch), interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("the cluster is away")
	}})}
	if _, err := failing.heldIn(context.Background(), "mc-alpha", "ffff0000"); err == nil {
		t.Error("a snapshot was taken as free when the servers could not be listed")
	}
}

func TestFinishesMove(t *testing.T) {
	move := &backup.Snapshot{ID: "abcd1234", Trigger: backup.TriggerMove}
	halfway := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{avsysv1.AnnotationRestoring: "abcd1234", avsysv1.AnnotationMoving: "b"}}}
	if !finishesMove(halfway, move) {
		t.Error("the snapshot of a move left halfway does not finish it")
	}
	// A move long done: its snapshot is restored as any other.
	if finishesMove(&avsysv1.MinecraftServer{}, move) {
		t.Error("a finished move's snapshot puts the home back")
	}
	// Another snapshot of a move, while one is left halfway.
	if finishesMove(halfway, &backup.Snapshot{ID: "ffff0000", Trigger: backup.TriggerMove}) {
		t.Error("another move's snapshot finishes this one")
	}
	if finishesMove(halfway, &backup.Snapshot{ID: "abcd1234", Trigger: backup.TriggerManual}) {
		t.Error("a snapshot not of a move puts the home back")
	}
}

// A move cut short with its files let go of holds the server until its
// snapshot is restored: nothing changes its files meanwhile, and it can
// still be deleted.
func TestHalfMovedIsHeld(t *testing.T) {
	p := newPanel(t)
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "halfway", Annotations: map[string]string{avsysv1.AnnotationRestoring: "abcd1234ef", avsysv1.AnnotationMoving: "b"}}}
	if _, err := p.srv.changing(mc, false); err == nil || !strings.Contains(err.Error(), "Restore snapshot abcd1234") {
		t.Errorf("changing it: %v", err)
	}
	release, err := p.srv.admit(mc, false)
	if err != nil {
		t.Fatalf("deleting it: %v", err)
	}
	release()
	delete(mc.Annotations, avsysv1.AnnotationMoving)
	release, err = p.srv.changing(mc, false)
	if err != nil {
		t.Errorf("once moved: %v", err)
	} else {
		release()
	}
}

// A move cut short before it let go of the server's files is undone: the
// server is put back in the state it was in, and only then.
func TestMoveCutShortIsPutBack(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	for _, mc := range []*avsysv1.MinecraftServer{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "was-running", Annotations: map[string]string{avsysv1.AnnotationMoveWas: "Running"}}, Spec: avsysv1.MinecraftServerSpec{State: avsysv1.StateStopped}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "was-stopped", Annotations: map[string]string{avsysv1.AnnotationMoveWas: "Stopped"}}, Spec: avsysv1.MinecraftServerSpec{State: avsysv1.StateStopped}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "let-go", Annotations: map[string]string{avsysv1.AnnotationMoveWas: "Running", avsysv1.AnnotationMoving: "b", avsysv1.AnnotationRestoring: "abcd1234"}}, Spec: avsysv1.MinecraftServerSpec{State: avsysv1.StateStopped}},
	} {
		if err := p.srv.Client.Create(ctx, mc); err != nil {
			t.Fatal(err)
		}
	}
	p.srv.resumeSaving(ctx)
	for name, want := range map[string]avsysv1.DesiredState{"was-running": avsysv1.StateRunning, "was-stopped": avsysv1.StateStopped, "let-go": avsysv1.StateStopped} {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: name}, mc); err != nil {
			t.Fatal(err)
		}
		if mc.Spec.State != want {
			t.Errorf("%s: %s, want %s", name, mc.Spec.State, want)
		}
		_, noted := mc.Annotations[avsysv1.AnnotationMoveWas]
		if noted != (name == "let-go") {
			t.Errorf("%s: still noted as moving: %v", name, noted)
		}
	}
}

// While a move is unfinished, only its own snapshot may be restored, which
// finishes it.
func TestHeldForMove(t *testing.T) {
	halfway := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{avsysv1.AnnotationRestoring: "abcd1234", avsysv1.AnnotationMoving: "b"}}}
	if err := heldForMove(halfway, "abcd1234"); err != nil {
		t.Errorf("its own snapshot: %v", err)
	}
	if err := heldForMove(halfway, "ffff0000"); err == nil || !strings.Contains(err.Error(), "Restore snapshot abcd1234 first") {
		t.Errorf("another: %v", err)
	}
	if err := heldForMove(&avsysv1.MinecraftServer{}, "ffff0000"); err != nil {
		t.Errorf("no move: %v", err)
	}
}

// A move that carries a server's files holds it for their restore and marks
// it as moving in one change: a panel stopped between two would leave it
// held for a restore that finishes no move, and not put back either.
func TestPlaceMarksTheMoveAtOnce(t *testing.T) {
	p := newPanel(t)
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	patches := 0
	p.srv.Client = interceptor.NewClient(p.srv.Client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Gi")},
		Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
	}}
	if err := p.srv.Client.Create(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if err := p.srv.place(context.Background(), key, "b", "abcd1234"); err != nil {
		t.Fatal(err)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), key, mc); err != nil {
		t.Fatal(err)
	}
	if patches != 1 || mc.Annotations[avsysv1.AnnotationMoving] != "b" || mc.Annotations[avsysv1.AnnotationRestoring] != "abcd1234" || mc.Annotations[avsysv1.AnnotationNode] != "b" {
		t.Errorf("%d changes, annotations %v", patches, mc.Annotations)
	}
}

// A move that takes no snapshot, over shared storage, leaves what the server
// says of its snapshots as it was: a failed one stays failed, and its own
// failure is not one.
func TestMoveWithoutSnapshotLeavesSnapshotStatus(t *testing.T) {
	p := newPanel(t)
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), key, mc); err != nil {
		t.Fatal(err)
	}
	mc.Status.Snapshots = &avsysv1.SnapshotStatus{Error: "the nightly snapshot failed"}
	if err := p.srv.Client.Status().Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	for _, moved := range []error{nil, errors.New("its pod did not start on b")} {
		if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindMove, ""); !ok {
			t.Fatal("the move did not begin")
		}
		p.srv.finishUnsaved(context.Background(), "mc-alpha", "world", kindMove, moved)
		if err := p.srv.Client.Get(context.Background(), key, mc); err != nil {
			t.Fatal(err)
		}
		if mc.Status.Snapshots == nil || mc.Status.Snapshots.Error != "the nightly snapshot failed" {
			t.Errorf("after a move that ended with %v: %+v", moved, mc.Status.Snapshots)
		}
		if p.srv.ops.current("mc-alpha", "world") != nil {
			t.Error("the move was not ended")
		}
	}
}

// A server placed on another node whose pod is still where it was, as when a
// move over shared storage did not finish, can be moved back there.
func TestMoveBackWhilePlacedElsewhere(t *testing.T) {
	p := newPanel(t)
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), key, mc); err != nil {
		t.Fatal(err)
	}
	mc.Annotations = map[string]string{avsysv1.AnnotationNode: "b"}
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	mc.Status.Node = "a"
	if err := p.srv.Client.Status().Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	move := func(node string) string {
		req := httptest.NewRequest("POST", "/api/v1/orgs/alpha/servers/world/move", strings.NewReader(`{"node":"`+node+`"}`))
		req.Header.Set("Authorization", "Bearer "+p.tokens["owner"])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if got := move("a"); strings.Contains(got, "already") {
		t.Errorf("moving it back to where its pod still is: %s", got)
	}
	// Placed where it runs, it is there already.
	if err := p.srv.Client.Get(context.Background(), key, mc); err != nil {
		t.Fatal(err)
	}
	mc.Annotations[avsysv1.AnnotationNode] = "a"
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if got := move("a"); !strings.Contains(got, "already") {
		t.Errorf("moving it to where it is: %s", got)
	}
}
