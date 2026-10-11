// Package router is the shared front door for players. Many servers can sit
// behind one address: the hostname a client asks for in its handshake decides
// which server it reaches. Sleeping servers are woken by a join attempt.
package router

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
)

// HostnameIndex is the cache index servers are looked up by. It must be
// registered with IndexHostname before the cache starts.
const HostnameIndex = "avsys.routerHostname"

// IndexHostname is the index function for HostnameIndex.
func IndexHostname(obj client.Object) []string {
	mc := obj.(*avsysv1.MinecraftServer)
	if mc.Spec.Expose.Type != avsysv1.ExposeRouter || mc.Spec.Expose.Hostname == "" {
		return nil
	}
	return []string{NormalizeHostname(mc.Spec.Expose.Hostname)}
}

func NormalizeHostname(h string) string {
	return mcproto.Handshake{Host: h}.Hostname()
}

type Router struct {
	Client client.Client
	Dialer kube.PodDialer
	Log    *slog.Logger
}

func (r *Router) Serve(ctx context.Context, ln net.Listener) error {
	context.AfterFunc(ctx, func() { ln.Close() })
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go r.handle(ctx, conn)
	}
}

func (r *Router) lookup(ctx context.Context, host string) (*avsysv1.MinecraftServer, error) {
	var list avsysv1.MinecraftServerList
	if err := r.Client.List(ctx, &list, client.MatchingFields{HostnameIndex: host}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

func motd(title, detail string) json.RawMessage {
	b, _ := json.Marshal(mcproto.Text{Text: title + "\n", Extra: []mcproto.Text{{Text: detail, Color: "gray"}}})
	return b
}

func (r *Router) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	hs, err := mcproto.ReadHandshake(br)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	host := hs.Hostname()
	mc, err := r.lookup(ctx, host)
	if err != nil {
		r.Log.Error("looking up server", "host", host, "error", err)
		return
	}

	// answer replies without involving a backend: a status line for the server
	// list, or a reason shown when a join is refused.
	answer := func(title, detail string) {
		if hs.NextState == mcproto.NextStatus {
			_ = mcproto.ServeStatus(conn, br, mcproto.Status{
				Version:     mcproto.Version{Name: "avSYS", Protocol: hs.Protocol},
				Description: motd(title, detail),
			})
			return
		}
		_ = mcproto.Disconnect(conn, mcproto.Text{Text: title + "\n\n", Extra: []mcproto.Text{{Text: detail, Color: "gray"}}})
	}

	if mc == nil {
		answer("Unknown server", "Nothing is hosted at "+host+".")
		return
	}
	name := mc.Spec.DisplayName
	if name == "" {
		name = mc.Name
	}

	switch mc.Status.Phase {
	case avsysv1.PhaseRunning:
		r.proxy(ctx, conn, br, hs, mc)
	case avsysv1.PhaseSleeping:
		if hs.NextState == mcproto.NextStatus {
			answer(name, "Asleep. Join to wake it up.")
			return
		}
		if err := controller.SetAsleep(ctx, r.Client, mc, false); err != nil {
			r.Log.Error("waking server", "server", client.ObjectKeyFromObject(mc), "error", err)
			answer(name, "This server could not be woken. Tell an admin.")
			return
		}
		r.Log.Info("woken by join", "server", client.ObjectKeyFromObject(mc))
		answer(name, "Waking up. Join again in a minute.")
	case avsysv1.PhaseStarting:
		answer(name, "Starting up. Try again in a moment.")
	default:
		answer(name, "Offline.")
	}
}

func (r *Router) proxy(ctx context.Context, conn net.Conn, br *bufio.Reader, hs mcproto.Handshake, mc *avsysv1.MinecraftServer) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	backend, err := r.Dialer.DialPod(dialCtx, mc.Namespace, mc.Name, controller.MinecraftPort)
	cancel()
	if err != nil {
		r.Log.Warn("backend unreachable", "server", client.ObjectKeyFromObject(mc), "error", err)
		return
	}
	defer backend.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	if _, err := backend.Write(hs.Raw); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		// br still holds whatever the client sent after its handshake.
		_, _ = io.Copy(backend, br)
		closeWrite(backend)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, backend)
		closeWrite(conn)
		done <- struct{}{}
	}()
	select {
	case <-done:
		// Give the other direction a moment to flush, then drop both.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	case <-ctx.Done():
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
