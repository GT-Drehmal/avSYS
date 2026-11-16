package controller

import (
	"context"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// StandIn answers on the extra ports of a server whose game is not running,
// in the game's place, from the panel: a sleeping server is woken by someone
// reaching them, as the router wakes one for a join. Meanwhile the Service
// of those ports points at the panel, and once the game runs, at its pod
// again.
type StandIn interface {
	// Open listens for each of a server's ports, and says where: the
	// panel's address, and the port each is on there, by name. Those it
	// listened for before and are not asked for now are closed.
	Open(server types.NamespacedName, ports []avsysv1.ExtraPort) (ip string, at map[string]int32, err error)
	// Close stops listening for a server.
	Close(server types.NamespacedName)
}

// StandInSliceName is the endpoints of a server's extra ports at the panel,
// while it stands in for the game.
func StandInSliceName(server string) string { return PortsServiceName(server) + "-panel" }

// standInManager marks the endpoints of the panel's, which the cluster's
// own controller of endpoints leaves alone.
const standInManager = "stand-in.avsys.dev"

// standInLabel is in the selector of a server's extra ports while the panel
// stands in, and on no pod: the Service selects none.
const standInLabel = "avsys.dev/stand-in"

// standInSelector is what a server's Service of extra ports selects while
// the panel stands in: no pod at all. The cluster then keeps its own
// endpoints of the Service empty, beside the panel's. A Service with no
// selector would not do: the cluster leaves what it last made of the game's
// pod, and copies its Endpoints into endpoints of the Service again, so
// that players would be sent there as often as to the panel.
func standInSelector(mc *avsysv1.MinecraftServer) map[string]string {
	return map[string]string{avsysv1.LabelServer: mc.Name, standInLabel: "true"}
}

// standingIn says whether the panel answers on a server's extra ports: while
// its game is not running, if the panel can.
func (r *Reconciler) standingIn(ctx context.Context, mc *avsysv1.MinecraftServer) (bool, error) {
	if r.Config.StandIn == nil {
		return false, nil
	}
	pod := &corev1.Pod{}
	switch err := r.Get(ctx, client.ObjectKeyFromObject(mc), pod); {
	case apierrors.IsNotFound(err):
		pod = nil
	case err != nil:
		return false, err
	}
	phase, _ := phaseOf(mc, pod)
	return phase != avsysv1.PhaseRunning, nil
}

// pointPorts has a server's Service of extra ports, svc, reach the panel
// while it stands in, through endpoints of its own beside the cluster's,
// which are of no pod (see standInSelector). Otherwise those are taken away,
// the Service selecting the game's pod.
//
// A panel that cannot stand in, as one outside the cluster, leaves
// endpoints alone: it may not be allowed to read them.
func (r *Reconciler) pointPorts(ctx context.Context, mc *avsysv1.MinecraftServer, svc *corev1.Service, open []avsysv1.ExtraPort, standIn bool) error {
	if r.Config.StandIn == nil {
		return nil
	}
	key := client.ObjectKeyFromObject(mc)
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: mc.Namespace, Name: StandInSliceName(mc.Name)}}
	if !standIn {
		r.closeStandIn(key)
		return r.dropSlice(ctx, slice)
	}
	ip, at, err := r.Config.StandIn.Open(key, open)
	if err != nil {
		return fmt.Errorf("listening in the game's place: %w", err)
	}
	family := discoveryv1.AddressTypeIPv4
	if parsed := net.ParseIP(ip); parsed == nil {
		return fmt.Errorf("the panel's address %q is not one", ip)
	} else if parsed.To4() == nil {
		family = discoveryv1.AddressTypeIPv6
	}
	// The kind of address is fixed once made.
	existing := &discoveryv1.EndpointSlice{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(slice), existing); err == nil && existing.AddressType != family {
		if err := r.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
			return err
		}
	} else if client.IgnoreNotFound(err) != nil {
		return err
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, slice, func() error {
		slice.Labels = labelsFor(mc)
		slice.Labels[discoveryv1.LabelServiceName] = svc.Name
		slice.Labels[discoveryv1.LabelManagedBy] = standInManager
		slice.AddressType = family
		ready := true
		slice.Endpoints = []discoveryv1.Endpoint{{Addresses: []string{ip}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}
		ports := make([]discoveryv1.EndpointPort, 0, len(open))
		for _, p := range open {
			name, protocol, port := portServiceName(p), protocolOf(p), at[p.Name]
			ports = append(ports, discoveryv1.EndpointPort{Name: &name, Protocol: &protocol, Port: &port})
		}
		slice.Ports = ports
		// Goes with the Service.
		return controllerutil.SetControllerReference(svc, slice, r.Scheme)
	})
	return err
}

// closeStandIn stops the panel answering for a server, if it does.
func (r *Reconciler) closeStandIn(server types.NamespacedName) {
	if r.Config.StandIn != nil {
		r.Config.StandIn.Close(server)
	}
}

// dropSlice removes the endpoints at the panel, where there are any.
func (r *Reconciler) dropSlice(ctx context.Context, slice *discoveryv1.EndpointSlice) error {
	if err := r.Get(ctx, client.ObjectKeyFromObject(slice), &discoveryv1.EndpointSlice{}); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	return client.IgnoreNotFound(r.Delete(ctx, slice))
}
