package controller

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// PortRange is the public ports given out to the extra ports of servers that
// share the router's address. Zero is none: the cluster then gives each a
// node port.
type PortRange struct{ First, Last int32 }

// ParsePortRange reads "first-last", or "" for none.
func ParsePortRange(s string) (PortRange, error) {
	if s == "" {
		return PortRange{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	first, err1 := strconv.Atoi(strings.TrimSpace(a))
	last, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || first < 1 || last > 65535 || first > last {
		return PortRange{}, fmt.Errorf("%q is not a range of ports, as 25600-25699", s)
	}
	return PortRange{int32(first), int32(last)}, nil
}

// PortsServiceName is the Service that opens the extra ports of a server
// sharing the router's address.
func PortsServiceName(server string) string { return server + "-ports" }

// sharesAddress says whether a server is reached through the router, whose
// one address its extra ports then need public ports of their own on.
func sharesAddress(mc *avsysv1.MinecraftServer) bool {
	return mc.Spec.Expose.Type == "" || mc.Spec.Expose.Type == avsysv1.ExposeRouter
}

// portServiceName is the name a port has in a Service: its own, which is
// never the game's.
func portServiceName(p avsysv1.ExtraPort) string { return p.Name }

func protocolOf(p avsysv1.ExtraPort) corev1.Protocol {
	if p.Protocol == "" {
		return corev1.ProtocolTCP
	}
	return p.Protocol
}

// publicPorts reads the public ports given out to a server, by port name.
func publicPorts(mc *avsysv1.MinecraftServer) map[string]int32 {
	out := map[string]int32{}
	for _, pair := range strings.Split(mc.Annotations[avsysv1.AnnotationPublicPorts], ",") {
		name, port, ok := strings.Cut(pair, "=")
		if n, err := strconv.Atoi(port); ok && err == nil {
			out[name] = int32(n)
		}
	}
	return out
}

func writePublicPorts(ports map[string]int32) string {
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s=%d", name, ports[name])
	}
	return strings.Join(parts, ",")
}

// allocating holds giving out public ports to one server at a time.
var allocating sync.Mutex

// allocatePorts gives each extra port of a server sharing the router's
// address a public port of its own from the range, one that no other server
// has, and records them on the server. Ports it no longer has give theirs
// back. Without a range nothing is given out. With every one given out, the
// ports left over get none, and full says so: the server runs without them.
//
// With none, it gives none, and any it had go back.
func (r *Reconciler) allocatePorts(ctx context.Context, mc *avsysv1.MinecraftServer, none bool) (given map[string]int32, full bool, err error) {
	have := publicPorts(mc)
	want := map[string]int32{}
	if sharesAddress(mc) && r.Config.PortRange.First != 0 && !none {
		for _, p := range mc.Spec.Ports {
			if n, ok := have[p.Name]; ok && n >= r.Config.PortRange.First && n <= r.Config.PortRange.Last {
				want[p.Name] = n
			}
		}
		if len(want) < len(mc.Spec.Ports) {
			allocating.Lock()
			defer allocating.Unlock()
			var all avsysv1.MinecraftServerList
			if err := r.Reader.List(ctx, &all); err != nil {
				return nil, false, err
			}
			taken := map[int32]bool{}
			// The router's own ports on the address they share are its: the
			// game's, and any other it has, as for the panel or SSH.
			if r.Config.RouterService.Name != "" {
				router := &corev1.Service{}
				if err := r.Reader.Get(ctx, r.Config.RouterService, router); client.IgnoreNotFound(err) != nil {
					return nil, false, err
				}
				for _, p := range router.Spec.Ports {
					taken[p.Port] = true
				}
			}
			for i := range all.Items {
				other := &all.Items[i]
				if other.Namespace == mc.Namespace && other.Name == mc.Name {
					continue
				}
				for _, n := range publicPorts(other) {
					taken[n] = true
				}
			}
			for _, n := range want {
				taken[n] = true
			}
			next := r.Config.PortRange.First
			for _, p := range mc.Spec.Ports {
				if _, ok := want[p.Name]; ok {
					continue
				}
				for next <= r.Config.PortRange.Last && taken[next] {
					next++
				}
				if next > r.Config.PortRange.Last {
					full = true
					continue
				}
				want[p.Name], taken[next] = next, true
			}
		}
	}
	if written := writePublicPorts(want); written != mc.Annotations[avsysv1.AnnotationPublicPorts] {
		orig := mc.DeepCopy()
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		if written == "" {
			delete(mc.Annotations, avsysv1.AnnotationPublicPorts)
		} else {
			mc.Annotations[avsysv1.AnnotationPublicPorts] = written
		}
		if err := r.Patch(ctx, mc, client.MergeFrom(orig)); err != nil {
			return nil, false, err
		}
	}
	return want, full, nil
}

// ensurePorts opens a server's extra ports and says where they are reached.
// One with an address of its own has them beside the game port on its
// Service (see applyService), at their own numbers or node ports. One that
// shares the router's address has a Service of their own, on the public
// ports given out to it, or node ports. One that is not exposed has none.
//
// A port that cannot be opened says why, and the server runs without it:
// retry is set while some wait for a public port to come free, or for the
// Service of their name to stop being another server's. Neither says so to
// this server when it happens.
func (r *Reconciler) ensurePorts(ctx context.Context, mc *avsysv1.MinecraftServer, svc *corev1.Service) (st []avsysv1.PortStatus, retry bool, err error) {
	own := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: mc.Namespace, Name: PortsServiceName(mc.Name)}}
	// Its name can be another server's own Service, of a server named so:
	// that one is never changed or removed from here, and no public port is
	// held for ports that cannot be opened on it.
	existing := &corev1.Service{}
	err = r.Reader.Get(ctx, client.ObjectKeyFromObject(own), existing)
	if client.IgnoreNotFound(err) != nil {
		return nil, false, err
	}
	theirs := err == nil && !metav1.IsControlledBy(existing, mc)
	public, full, err := r.allocatePorts(ctx, mc, theirs)
	if err != nil {
		return nil, false, err
	}
	// Without a public port they are left off the Service, which cannot be
	// made with no ports at all.
	open := make([]avsysv1.ExtraPort, 0, len(mc.Spec.Ports))
	for _, p := range mc.Spec.Ports {
		if _, ok := public[p.Name]; ok || r.Config.PortRange.First == 0 {
			open = append(open, p)
		}
	}
	if !sharesAddress(mc) || len(open) == 0 || theirs {
		if !theirs {
			if err := r.Delete(ctx, own); client.IgnoreNotFound(err) != nil {
				return nil, false, err
			}
		}
		if len(mc.Spec.Ports) == 0 || mc.Spec.Expose.Type == avsysv1.ExposeNone {
			return nil, false, nil
		}
		if !sharesAddress(mc) {
			return portStatus(mc, svc, nil, r.Config), false, nil
		}
		st = portStatus(mc, nil, nil, r.Config)
		for i := range st {
			if theirs {
				st[i].Message = fmt.Sprintf("Not open: another server, %s, has them.", own.Name)
			} else {
				st[i].Message = r.noneFree()
			}
		}
		return st, full || theirs, nil
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, own, func() error {
		own.Labels = labelsFor(mc)
		own.Annotations = r.Config.PortsServiceAnnotations
		own.Spec.Selector = map[string]string{avsysv1.LabelServer: mc.Name}
		own.Spec.Type = corev1.ServiceTypeNodePort
		if r.Config.PortRange.First != 0 {
			own.Spec.Type = corev1.ServiceTypeLoadBalancer
		}
		kept := map[string]int32{}
		for _, p := range own.Spec.Ports {
			kept[p.Name] = p.NodePort
		}
		ports := make([]corev1.ServicePort, 0, len(open))
		for _, p := range open {
			sp := corev1.ServicePort{Name: portServiceName(p), Port: p.Port, TargetPort: intstr.FromInt32(p.Port), Protocol: protocolOf(p), NodePort: kept[portServiceName(p)]}
			if n := public[p.Name]; n != 0 {
				sp.Port = n
			}
			ports = append(ports, sp)
		}
		own.Spec.Ports = ports
		return controllerutil.SetControllerReference(mc, own, r.Scheme)
	})
	if err != nil {
		return nil, false, err
	}
	st = portStatus(mc, own, public, r.Config)
	// Where the load balancer put these ports on an address of their own,
	// not the router's that the hostname points at, that address is shown.
	if host := r.ownAddress(ctx, own); host != "" {
		for i := range st {
			if st[i].PublicPort != 0 {
				st[i].Address = net.JoinHostPort(host, strconv.Itoa(int(st[i].PublicPort)))
			}
		}
	}
	for i := range st {
		if _, ok := public[st[i].Name]; !ok && r.Config.PortRange.First != 0 {
			st[i].Message = r.noneFree()
		}
	}
	return st, full, nil
}

// noneFree says a port has no public port as every one is given out.
func (r *Reconciler) noneFree() string {
	return fmt.Sprintf("Not open: all public ports from %d to %d are in use. It opens once one is free.", r.Config.PortRange.First, r.Config.PortRange.Last)
}

// ownAddress is the address a server's Service of extra ports was given by
// the load balancer, if that is not the router's or the router's is not
// known, or "".
func (r *Reconciler) ownAddress(ctx context.Context, own *corev1.Service) string {
	mine := ingressOf(own)
	if len(mine) == 0 {
		return ""
	}
	// The router's Service unknown, as for a panel run outside the cluster,
	// the address the load balancer gave is the one known to reach them.
	if r.Config.RouterService.Name == "" {
		return mine[0]
	}
	router := &corev1.Service{}
	if err := r.Reader.Get(ctx, r.Config.RouterService, router); err != nil {
		return ""
	}
	theirs := ingressOf(router)
	for _, a := range mine {
		for _, b := range theirs {
			if a == b {
				return ""
			}
		}
	}
	return mine[0]
}

func ingressOf(svc *corev1.Service) []string {
	var out []string
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.Hostname != "" {
			out = append(out, ing.Hostname)
		} else if ing.IP != "" {
			out = append(out, ing.IP)
		}
	}
	return out
}

// portStatus says where each extra port is reached, by the Service that
// opens it.
func portStatus(mc *avsysv1.MinecraftServer, svc *corev1.Service, public map[string]int32, cfg Config) []avsysv1.PortStatus {
	byName := map[string]corev1.ServicePort{}
	if svc != nil {
		for _, sp := range svc.Spec.Ports {
			byName[sp.Name] = sp
		}
	}
	host := mc.Spec.Expose.Hostname
	if host == "" && svc != nil {
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if host = ing.Hostname; host == "" {
				host = ing.IP
			}
			if host != "" {
				break
			}
		}
	}
	out := make([]avsysv1.PortStatus, 0, len(mc.Spec.Ports))
	for _, p := range mc.Spec.Ports {
		st := avsysv1.PortStatus{Name: p.Name, Port: p.Port, Protocol: protocolOf(p)}
		sp, ok := byName[portServiceName(p)]
		switch {
		case !ok:
		case svc.Spec.Type == corev1.ServiceTypeNodePort:
			st.PublicPort = sp.NodePort
		case public != nil:
			st.PublicPort = public[p.Name]
		default:
			st.PublicPort = sp.Port
		}
		if st.PublicPort != 0 && host != "" {
			st.Address = net.JoinHostPort(host, strconv.Itoa(int(st.PublicPort)))
		}
		out = append(out, st)
	}
	return out
}

// extraServicePorts are a server's extra ports on its own Service, when it
// has an address of its own: at their own numbers, keeping node ports given
// before.
func extraServicePorts(mc *avsysv1.MinecraftServer, existing []corev1.ServicePort) []corev1.ServicePort {
	if sharesAddress(mc) || mc.Spec.Expose.Type == avsysv1.ExposeNone {
		return nil
	}
	kept := map[string]int32{}
	for _, p := range existing {
		kept[p.Name] = p.NodePort
	}
	out := make([]corev1.ServicePort, 0, len(mc.Spec.Ports))
	for _, p := range mc.Spec.Ports {
		sp := corev1.ServicePort{Name: portServiceName(p), Port: p.Port, TargetPort: intstr.FromInt32(p.Port), Protocol: protocolOf(p)}
		// A load balancer's ports have node ports too, which the cluster
		// gave and which are kept, as the game port's are.
		if mc.Spec.Expose.Type == avsysv1.ExposeNodePort || mc.Spec.Expose.Type == avsysv1.ExposeLoadBalancer {
			sp.NodePort = kept[sp.Name]
		}
		out = append(out, sp)
	}
	return out
}
