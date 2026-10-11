// Package kube holds the pieces of Kubernetes plumbing shared by the gateway,
// router and API: reaching a port inside a pod, and streaming its logs.
package kube

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PodDialer opens a TCP connection to a port of a pod.
type PodDialer interface {
	DialPod(ctx context.Context, namespace, pod string, port int) (net.Conn, error)
}

// NewPodDialer returns a dialer that connects straight to pod IPs when running
// inside the cluster, and tunnels through the API server otherwise, so the
// panel behaves the same when run from a laptop against a remote cluster.
func NewPodDialer(cfg *rest.Config, cs kubernetes.Interface, inCluster bool) PodDialer {
	if inCluster {
		return &directDialer{cs: cs}
	}
	return &forwardDialer{cfg: cfg, cs: cs}
}

type directDialer struct{ cs kubernetes.Interface }

func (d *directDialer) DialPod(ctx context.Context, namespace, pod string, port int) (net.Conn, error) {
	p, err := d.cs.CoreV1().Pods(namespace).Get(ctx, pod, getOptions)
	if err != nil {
		return nil, err
	}
	if p.Status.PodIP == "" {
		return nil, fmt.Errorf("pod %s/%s has no IP yet", namespace, pod)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", net.JoinHostPort(p.Status.PodIP, strconv.Itoa(port)))
}

type forwardDialer struct {
	cfg *rest.Config
	cs  kubernetes.Interface
	seq atomic.Int64
}

func (d *forwardDialer) DialPod(ctx context.Context, namespace, pod string, port int) (net.Conn, error) {
	transport, upgrader, err := spdy.RoundTripperFor(d.cfg)
	if err != nil {
		return nil, err
	}
	url := d.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url)
	stream, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("port-forward to %s/%s: %w", namespace, pod, err)
	}

	headers := http.Header{}
	headers.Set(corev1.PortHeader, strconv.Itoa(port))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.FormatInt(d.seq.Add(1), 10))

	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	errStream, err := stream.CreateStream(headers)
	if err != nil {
		stream.Close()
		return nil, err
	}
	// The error stream is read-only for us; closing our side tells the kubelet so.
	errStream.Close()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	data, err := stream.CreateStream(headers)
	if err != nil {
		stream.Close()
		return nil, err
	}

	c := &forwardConn{Stream: data, conn: stream, remote: forwardAddr(fmt.Sprintf("%s/%s:%d", namespace, pod, port))}
	go func() {
		// Anything on the error stream means the kubelet could not connect.
		if msg, _ := io.ReadAll(errStream); len(msg) > 0 {
			c.Close()
		}
	}()
	return c, nil
}

type forwardAddr string

func (a forwardAddr) Network() string { return "portforward" }
func (a forwardAddr) String() string  { return string(a) }

type forwardConn struct {
	httpstream.Stream
	conn   httpstream.Connection
	remote forwardAddr
}

func (c *forwardConn) Close() error {
	c.Stream.Close()
	return c.conn.Close()
}

func (c *forwardConn) LocalAddr() net.Addr  { return forwardAddr("local") }
func (c *forwardConn) RemoteAddr() net.Addr { return c.remote }

// Deadlines are not supported by SPDY streams. Callers that need a timeout
// must bound the operation another way.
func (c *forwardConn) SetDeadline(time.Time) error      { return nil }
func (c *forwardConn) SetReadDeadline(time.Time) error  { return nil }
func (c *forwardConn) SetWriteDeadline(time.Time) error { return nil }
