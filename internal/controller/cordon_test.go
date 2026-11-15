package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// admitsOnCordoned is what the scheduler asks of a pod for a cordoned node:
// that it tolerates the node's taint for that.
func admitsOnCordoned(pod *corev1.Pod) bool {
	for i := range pod.Spec.Tolerations {
		if toleratesTaint(pod.Spec.Tolerations[i], &cordoned) {
			return true
		}
	}
	return false
}

// A server stopped for a move has its pod made again on a cordoned node, so
// that the move can read its files there. Nothing else does: not the game,
// and not the server once the move lets go of its files or is put back.
func TestOnlyAMoveReadsOnACordonedNode(t *testing.T) {
	onA := func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationNode: "a"}
	}
	for name, c := range map[string]struct {
		mutate   func(*avsysv1.MinecraftServer)
		admitted bool
	}{
		"running": {nil, false},
		"stopped": {func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped }, false},
		"stopped for a move": {func(mc *avsysv1.MinecraftServer) {
			mc.Spec.State = avsysv1.StateStopped
			mc.Annotations[avsysv1.AnnotationMoveWas] = "Running"
		}, true},
		"stopped for a move of a stopped server": {func(mc *avsysv1.MinecraftServer) {
			mc.Spec.State = avsysv1.StateStopped
			mc.Annotations[avsysv1.AnnotationMoveWas] = "Stopped"
		}, true},
		// Its files let go of, it is made on the node it goes to.
		"moving": {func(mc *avsysv1.MinecraftServer) {
			mc.Spec.State = avsysv1.StateStopped
			mc.Annotations[avsysv1.AnnotationMoveWas] = "Running"
			mc.Annotations[avsysv1.AnnotationMoving] = "b"
			mc.Annotations[avsysv1.AnnotationRestoring] = "abcd1234"
		}, false},
		// A move whose mark was left on while the game is to run.
		"running with the mark": {func(mc *avsysv1.MinecraftServer) {
			mc.Annotations[avsysv1.AnnotationMoveWas] = "Running"
		}, false},
	} {
		mc := server(func(mc *avsysv1.MinecraftServer) {
			onA(mc)
			if c.mutate != nil {
				c.mutate(mc)
			}
		})
		pod := buildPod(mc, Config{})
		if got := admitsOnCordoned(pod); got != c.admitted {
			t.Errorf("%s: admitted on a cordoned node: %v, want %v", name, got, c.admitted)
		}
		if !c.admitted {
			if len(pod.Spec.Tolerations) != 0 {
				t.Errorf("%s: tolerates %+v", name, pod.Spec.Tolerations)
			}
			continue
		}
		// The cordon, and nothing else: not a node that is down, nor any
		// other taint.
		if len(pod.Spec.Tolerations) != 1 {
			t.Errorf("%s: tolerates %+v", name, pod.Spec.Tolerations)
		}
		for _, other := range []corev1.Taint{
			{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoExecute},
			{Key: corev1.TaintNodeNotReady, Effect: corev1.TaintEffectNoSchedule},
			{Key: corev1.TaintNodeDiskPressure, Effect: corev1.TaintEffectNoSchedule},
			{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		} {
			for _, tol := range pod.Spec.Tolerations {
				if toleratesTaint(tol, &other) {
					t.Errorf("%s: tolerates %s", name, other.Key)
				}
			}
		}
		// Kept to its node as ever.
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0] != "a" {
			t.Errorf("%s: not kept to its node: %+v", name, pod.Spec.Affinity)
		}
	}
}

// Being stopped for a move is no setting a pod is made again for: its pod
// made for the move asks for no restart once the move is over.
func TestReadingForAMoveAsksNoRestart(t *testing.T) {
	stopped := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.State = avsysv1.StateStopped
		mc.Annotations = map[string]string{avsysv1.AnnotationNode: "a"}
	})
	marked := stopped.DeepCopy()
	marked.Annotations[avsysv1.AnnotationMoveWas] = "Stopped"
	if a, b := buildPod(stopped, Config{}).Annotations[annotationSpecHash], buildPod(marked, Config{}).Annotations[annotationSpecHash]; a != b {
		t.Errorf("the hash changes with the mark: %s, %s", a, b)
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
