package api

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func node(name, memory, score string) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelHostname: name}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	if score != "" {
		n.Labels[avsysv1.LabelNodeScore] = score
	}
	return n
}

// The nodes a server can be put on, fastest first, with what automatic
// placement would choose; a node chosen by hand has to be up and have room.
func TestChoosingANode(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	for _, n := range []*corev1.Node{node("fast", "16Gi", "2.0"), node("pi", "3Gi", "0.4")} {
		if err := p.srv.Client.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	code, got := p.get("viewer", "GET", "/nodes?memory=4Gi", "")
	nodes, _ := got["nodes"].([]any)
	if code != 200 || len(nodes) != 2 || nodes[0].(map[string]any)["name"] != "fast" || got["automatic"] != "fast" {
		t.Fatalf("listing: %d %v", code, got)
	}
	if _, ok := nodes[0].(map[string]any)["servers"]; ok {
		t.Error("a member is shown which servers run where across the panel")
	}

	// Too little room on the small one for 4 GB, enough for 1.
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"big","node":"pi","spec":{"eula":true,"version":"1.21.4","resources":{"memory":"4Gi"}}}`); code != 400 {
		t.Errorf("4 GB on a 3 GB node: %d %s, want 400", code, msg)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"nowhere","node":"missing","spec":{"eula":true,"version":"1.21.4"}}`); code != 400 {
		t.Errorf("a node that is not there: %d %s, want 400", code, msg)
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"small","node":"pi","spec":{"eula":true,"version":"1.21.4","resources":{"memory":"1Gi"}}}`); code != 201 {
		t.Fatalf("1 GB on the small one: %d %s", code, msg)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "small"}, mc); err != nil {
		t.Fatal(err)
	}
	if mc.Annotations[avsysv1.AnnotationNode] != "pi" {
		t.Errorf("not kept to the node chosen: %v", mc.Annotations)
	}

	// The panel's administrator sees what runs where.
	_, got = p.get("root", "GET", "/nodes", "")
	for _, n := range got["nodes"].([]any) {
		if n.(map[string]any)["name"] == "pi" {
			servers, _ := n.(map[string]any)["servers"].([]any)
			if len(servers) != 1 || servers[0].(map[string]any)["name"] != "small" {
				t.Errorf("servers on pi: %v", servers)
			}
		}
	}
}

// The panel's administrator keeps a node out of automatic placement, and
// lets it back in; nobody else can.
func TestSwitchingANodesPlacement(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	if err := p.srv.Client.Create(ctx, node("pi", "4Gi", "1900")); err != nil {
		t.Fatal(err)
	}
	if code, _ := p.do("owner", "PATCH", "/nodes/pi", `{"automatic":false}`); code != 403 {
		t.Errorf("an organization's owner switched a node: %d, want 403", code)
	}
	if code, msg := p.do("root", "PATCH", "/nodes/missing", `{"automatic":false}`); code != 404 {
		t.Errorf("a node that is not there: %d %s, want 404", code, msg)
	}
	automatic := func() any {
		_, got := p.get("root", "GET", "/nodes", "")
		return got["nodes"].([]any)[0].(map[string]any)["automatic"]
	}
	if code, msg := p.do("root", "PATCH", "/nodes/pi", `{"automatic":false}`); code != 204 {
		t.Fatalf("switching it off: %d %s", code, msg)
	}
	if automatic() != false {
		t.Error("still takes new servers")
	}
	n := &corev1.Node{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Name: "pi"}, n); err != nil || n.Labels[avsysv1.LabelNodeHosts] != "false" || n.Labels[avsysv1.LabelNodeScore] != "1900" {
		t.Errorf("labels: %v %v", n.Labels, err)
	}
	if code, msg := p.do("root", "PATCH", "/nodes/pi", `{"automatic":true}`); code != 204 || automatic() != true {
		t.Errorf("switching it back on: %d %s", code, msg)
	}
	if code, _ := p.do("owner", "POST", "/nodes/measure", ""); code != 403 {
		t.Errorf("an organization's owner measured nodes: %d, want 403", code)
	}
}

func TestBestRun(t *testing.T) {
	for out, want := range map[string]int{"4120\n4262\n": 4262, "5500\n": 5500, "\n4100\n": 4100} {
		if got, ok := bestRun(out); !ok || got != want {
			t.Errorf("bestRun(%q) = %d, %v; want %d", out, got, ok, want)
		}
	}
	for _, out := range []string{"", "ERROR: no space\n", "0\n"} {
		if got, ok := bestRun(out); ok {
			t.Errorf("bestRun(%q) = %d, want none", out, got)
		}
	}
}
