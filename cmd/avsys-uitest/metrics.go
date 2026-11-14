package main

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/api"
)

// The panel samples what servers and nodes use every thirty seconds, and
// draws the last hours of it. Here, where nothing runs, it is given the two
// hours before it started at once, and goes on sampling from then.
//
// Without --metrics the cluster has no metrics API, so only players are
// noted. With it, a stand-in answers for every running server and for two
// nodes, which are made for it: the Nodes page has something to draw, and
// the other tests, which expect no nodes, are run against a panel without.
const history = 2 * time.Hour

// metricsStandIn answers as a cluster's metrics API would: what each running
// server's game is using, and each node in all, as smooth curves over time.
// Off, it answers as a cluster without one does.
type metricsStandIn struct {
	on      bool
	cluster client.Client
	mu      sync.Mutex
	// at is the moment being sampled: zero is now.
	at time.Time
}

func (m *metricsStandIn) now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at.IsZero() {
		return time.Now()
	}
	return m.at
}

// wave goes between 0 and 1 and back, once in period, starting at phase.
func wave(at time.Time, period time.Duration, phase float64) float64 {
	return (1 + math.Sin(2*math.Pi*(float64(at.UnixNano())/float64(period)+phase))) / 2
}

type usage struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

func (m *metricsStandIn) Pods(ctx context.Context) ([]byte, error) {
	if !m.on {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "")
	}
	at := m.now()
	var servers avsysv1.MinecraftServerList
	if err := m.cluster.List(ctx, &servers); err != nil {
		return nil, err
	}
	type container struct {
		Name  string `json:"name"`
		Usage usage  `json:"usage"`
	}
	type pod struct {
		Metadata   metav1.ObjectMeta `json:"metadata"`
		Timestamp  time.Time         `json:"timestamp"`
		Containers []container       `json:"containers"`
	}
	list := struct {
		Items []pod `json:"items"`
	}{Items: []pod{}}
	for i, mc := range servers.Items {
		if mc.Status.Phase != avsysv1.PhaseRunning {
			continue
		}
		limit := float64(mc.Spec.Resources.Memory.Value())
		// Most of what it may use, rising and falling over the hours as a
		// game's memory does, and CPU busier while the memory grows.
		memory := limit * (0.55 + 0.25*wave(at, 50*time.Minute, float64(i)/5))
		cpu := 400 + 900*wave(at, 13*time.Minute, float64(i)/3)
		list.Items = append(list.Items, pod{
			Metadata:  metav1.ObjectMeta{Name: mc.Name, Namespace: mc.Namespace},
			Timestamp: at,
			Containers: []container{
				{Name: "minecraft", Usage: usage{CPU: resource.NewMilliQuantity(int64(cpu), resource.DecimalSI).String(), Memory: resource.NewQuantity(int64(memory), resource.BinarySI).String()}},
				{Name: "workspace", Usage: usage{CPU: "2m", Memory: "18Mi"}},
			},
		})
	}
	return json.Marshal(list)
}

func (m *metricsStandIn) Nodes(context.Context) ([]byte, error) {
	if !m.on {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, "")
	}
	at := m.now()
	type node struct {
		Metadata metav1.ObjectMeta `json:"metadata"`
		Usage    usage             `json:"usage"`
	}
	gib := float64(1 << 30)
	nodes := []node{
		{Metadata: metav1.ObjectMeta{Name: "pc"}, Usage: usage{
			CPU:    resource.NewMilliQuantity(int64(1800+2400*wave(at, 13*time.Minute, 0)), resource.DecimalSI).String(),
			Memory: resource.NewQuantity(int64((9+3*wave(at, 50*time.Minute, 0))*gib), resource.BinarySI).String(),
		}},
		{Metadata: metav1.ObjectMeta{Name: "pi5"}, Usage: usage{
			CPU:    resource.NewMilliQuantity(int64(150+100*wave(at, 7*time.Minute, 0.5)), resource.DecimalSI).String(),
			Memory: resource.NewQuantity(int64((1.2+0.2*wave(at, 30*time.Minute, 0.5))*gib), resource.BinarySI).String(),
		}},
	}
	return json.Marshal(struct {
		Items []node `json:"items"`
	}{nodes})
}

// seedNodes makes the nodes the stand-in answers for, and puts the first
// organization's servers on the faster.
func seedNodes(ctx context.Context, cluster client.Client) error {
	for _, n := range []struct {
		name, memory, cores, score string
	}{{"pc", "32Gi", "16", "5500"}, {"pi5", "8Gi", "4", "900"}} {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: n.name, Labels: map[string]string{corev1.LabelHostname: n.name, avsysv1.LabelNodeScore: n.score}},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(n.memory), corev1.ResourceCPU: resource.MustParse(n.cores)},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		}
		if err := cluster.Create(ctx, node); err != nil {
			return err
		}
	}
	var servers avsysv1.MinecraftServerList
	if err := cluster.List(ctx, &servers, client.InNamespace("mc-friends")); err != nil {
		return err
	}
	for i := range servers.Items {
		mc := &servers.Items[i]
		// Placed there, so that what it holds on the node counts too.
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationNode] = "pc"
		if err := cluster.Update(ctx, mc); err != nil {
			return err
		}
		mc.Status.Node = "pc"
		if err := cluster.Status().Update(ctx, mc); err != nil {
			return err
		}
	}
	return nil
}

// backfill samples the hours before now, as if the panel had been sampling
// all along. RunUsage goes on from now.
func backfill(ctx context.Context, srv *api.Server, m *metricsStandIn) {
	now := time.Now()
	for at := now.Add(-history); at.Before(now); at = at.Add(30 * time.Second) {
		m.mu.Lock()
		m.at = at
		m.mu.Unlock()
		srv.SampleUsage(ctx, at)
	}
	m.mu.Lock()
	m.at = time.Time{}
	m.mu.Unlock()
}
