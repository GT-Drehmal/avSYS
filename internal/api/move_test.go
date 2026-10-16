package api

import (
	"context"
	"errors"
	"testing"

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
