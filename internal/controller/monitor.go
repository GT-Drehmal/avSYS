package controller

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
)

// Monitor pings running servers for their player list, and puts idle ones to
// sleep when they are configured for it.
type Monitor struct {
	Client   client.Client
	Dialer   kube.PodDialer
	Interval time.Duration
}

// Start implements manager.Runnable.
func (m *Monitor) Start(ctx context.Context) error {
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			m.sweep(ctx)
		}
	}
}

func (m *Monitor) sweep(ctx context.Context) {
	var list avsysv1.MinecraftServerList
	if err := m.Client.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "listing servers")
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range list.Items {
		mc := &list.Items[i]
		if mc.Status.Phase != avsysv1.PhaseRunning {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m.check(ctx, mc)
		}()
	}
	wg.Wait()
}

func (m *Monitor) ping(ctx context.Context, mc *avsysv1.MinecraftServer) (mcproto.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := m.Dialer.DialPod(ctx, mc.Namespace, mc.Name, MinecraftPort)
	if err != nil {
		return mcproto.Status{}, err
	}
	// Tunnelled connections ignore deadlines, so the timeout is enforced by closing.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	return mcproto.Ping(conn, "localhost")
}

func (m *Monitor) check(ctx context.Context, mc *avsysv1.MinecraftServer) {
	st, err := m.ping(ctx, mc)
	if err != nil {
		// A server that is restarting or lagging misses pings; the pod status
		// already covers real outages.
		return
	}
	orig := mc.DeepCopy()
	players := &avsysv1.PlayersStatus{Online: int32(st.Players.Online), Max: int32(st.Players.Max)}
	for _, p := range st.Players.Sample {
		players.Names = append(players.Names, p.Name)
	}
	mc.Status.Players = players
	mc.Status.Version = st.Version.Name

	now := metav1.Now()
	// Refreshed at most once a minute to keep status writes (and the UI events
	// they cause) rare.
	if players.Online > 0 && (mc.Status.LastActive == nil || now.Sub(mc.Status.LastActive.Time) > time.Minute) {
		mc.Status.LastActive = &now
	}
	if !equality.Semantic.DeepEqual(orig.Status, mc.Status) {
		if err := m.Client.Status().Patch(ctx, mc, client.MergeFrom(orig)); err != nil {
			log.FromContext(ctx).Error(err, "updating player status", "server", client.ObjectKeyFromObject(mc))
			return
		}
	}

	sleep := mc.Spec.Sleep
	if !sleep.Enabled || mc.Spec.Expose.Type != avsysv1.ExposeRouter || players.Online > 0 || mc.Status.LastActive == nil {
		return
	}
	if now.Sub(mc.Status.LastActive.Time) >= time.Duration(sleep.IdleMinutes)*time.Minute {
		if err := SetAsleep(ctx, m.Client, mc, true); err != nil {
			log.FromContext(ctx).Error(err, "putting server to sleep", "server", client.ObjectKeyFromObject(mc))
		}
	}
}

// SetAsleep marks a server as asleep, or wakes it.
func SetAsleep(ctx context.Context, c client.Client, mc *avsysv1.MinecraftServer, asleep bool) error {
	_, is := mc.Annotations[avsysv1.AnnotationAsleep]
	if is == asleep {
		return nil
	}
	orig := mc.DeepCopy()
	if asleep {
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationAsleep] = time.Now().UTC().Format(time.RFC3339)
	} else {
		delete(mc.Annotations, avsysv1.AnnotationAsleep)
	}
	return c.Patch(ctx, mc, client.MergeFrom(orig))
}
