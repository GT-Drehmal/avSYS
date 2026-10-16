package api

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A change to a server's settings is merged into the server as it is when
// it is written. One that found it changed since it was read, as by the
// controller saying how the server is doing, which it does all the time, is
// read and merged again, not refused as changed by someone else.
func TestUpdateIsMergedAgainWhenTheServerMoved(t *testing.T) {
	p := newPanel(t)
	cluster := p.srv.Client.(client.WithWatch)
	refusals := 0
	refuse := func(times int) {
		p.srv.Client = interceptor.NewClient(cluster, interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*avsysv1.MinecraftServer); ok && refusals < times {
					refusals++
					// What the controller does meanwhile: says how it is doing.
					mc := &avsysv1.MinecraftServer{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), mc); err != nil {
						return err
					}
					mc.Status.Players = &avsysv1.PlayersStatus{Online: int32(refusals)}
					if err := c.Status().Update(ctx, mc); err != nil {
						return err
					}
					return apierrors.NewConflict(schema.GroupResource{Group: "avsys.dev", Resource: "minecraftservers"}, obj.GetName(), nil)
				}
				return c.Update(ctx, obj, opts...)
			},
		})
		p.srv.Reader = p.srv.Client
	}
	refuse(2)
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"displayName":"Renamed"}}`); got != 200 {
		t.Fatalf("a change after the server moved twice: %d %s", got, code)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := cluster.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	if mc.Spec.DisplayName != "Renamed" {
		t.Errorf("display name %q", mc.Spec.DisplayName)
	}
	// One that keeps finding it changed is told so, in the end.
	refusals = 0
	refuse(10)
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"displayName":"Again"}}`); got != 409 || code != "stale" {
		t.Errorf("a change that keeps finding it changed: %d %s", got, code)
	}
	if refusals != 3 {
		t.Errorf("tried %d times, want 3", refusals)
	}
}
