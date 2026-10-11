package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
