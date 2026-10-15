package api

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
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
