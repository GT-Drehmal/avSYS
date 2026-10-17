// Package sshgw is the SSH front door to server workspaces.
//
// People connect as `ssh <server>.<org>@panel` with a key from their avSYS
// account. The gateway authenticates the key, checks their role in the
// organization, and then relays the whole SSH connection to the sshd in that
// server's workspace container. Shells, scp/sftp and port forwarding (and so
// VS Code Remote SSH) all pass through untouched.
package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Authorizer decides whether a user may open a workspace.
type Authorizer interface {
	// AuthorizeWorkspace returns the namespace of the named server if the user
	// may open a shell on it, and an error meant for the user otherwise.
	AuthorizeWorkspace(ctx context.Context, user *store.User, org, server string) (namespace string, err error)
}

type Gateway struct {
	Store  *store.Store
	Authz  Authorizer
	Dialer kube.PodDialer
	// HostKey identifies the gateway to people connecting.
	HostKey ssh.Signer
	// WorkspaceKey is the only key workspace containers accept.
	WorkspaceKey ssh.Signer
	Log          *slog.Logger
}

// LoadOrCreateKey returns the ed25519 key stored under name, generating it on
// first use so it stays stable across restarts.
func LoadOrCreateKey(ctx context.Context, st *store.Store, name string) (ssh.Signer, error) {
	key := "ssh-key/" + name
	if stored, err := st.Setting(ctx, key); err == nil {
		return ssh.ParsePrivateKey([]byte(stored))
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "avsys "+name)
	if err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(block)
	if err := st.SetSetting(ctx, key, string(encoded)); err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(encoded)
}

// splitTarget parses the SSH user name `<server>.<org>`.
func splitTarget(user string) (server, org string, ok bool) {
	server, org, ok = strings.Cut(user, ".")
	return server, org, ok && server != "" && org != ""
}

const (
	extUserID    = "avsys-user-id"
	extUsername  = "avsys-username"
	extNamespace = "avsys-namespace"
	extServer    = "avsys-server"
)

func (g *Gateway) serverConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-avSYS",
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			user, err := g.Store.UserBySSHKey(ctx, ssh.FingerprintSHA256(key))
			if err != nil {
				return nil, errors.New("unknown key")
			}
			server, org, ok := splitTarget(meta.User())
			if !ok {
				return nil, &ssh.BannerError{
					Err:     errors.New("no target"),
					Message: fmt.Sprintf("Hi %s. Connect to a server with: ssh <server>.<org>@<this host>\n", user.Username),
				}
			}
			namespace, err := g.Authz.AuthorizeWorkspace(ctx, user, org, server)
			if err != nil {
				return nil, &ssh.BannerError{Err: err, Message: err.Error() + "\n"}
			}
			return &ssh.Permissions{Extensions: map[string]string{
				extUserID: user.ID, extUsername: user.Username, extNamespace: namespace, extServer: server,
			}}, nil
		},
	}
	cfg.AddHostKey(g.HostKey)
	return cfg
}

// Serve accepts connections until ctx is cancelled.
func (g *Gateway) Serve(ctx context.Context, ln net.Listener) error {
	cfg := g.serverConfig()
	context.AfterFunc(ctx, func() { ln.Close() })
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go g.handle(ctx, conn, cfg)
	}
}

func (g *Gateway) handle(ctx context.Context, nc net.Conn, cfg *ssh.ServerConfig) {
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
	down, downChans, downReqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer down.Close()

	ext := down.Permissions.Extensions
	log := g.Log.With("user", ext[extUsername], "namespace", ext[extNamespace], "server", ext[extServer])

	up, upChans, upReqs, err := g.dialWorkspace(ctx, ext[extNamespace], ext[extServer])
	if err != nil {
		log.Warn("workspace unreachable", "error", err)
		go ssh.DiscardRequests(downReqs)
		refuse(downChans, "The workspace for this server is not reachable right now. It may be restarting; try again in a moment.\r\n")
		return
	}
	defer up.Close()

	log.Info("ssh session opened", "remote", nc.RemoteAddr().String())
	_ = g.Store.Audit(ctx, store.AuditEntry{UserID: ext[extUserID], Action: "ssh.connect", Target: ext[extNamespace] + "/" + ext[extServer]})

	env := map[string]string{"AVSYS_USER": ext[extUsername]}
	go relayChannels(up, downChans, env)
	go relayChannels(down, upChans, nil)
	go relayRequests(up, downReqs, nil)
	// The workspace's own host keys must not reach the client: it knows this
	// connection by the gateway's key.
	go relayRequests(down, upReqs, func(t string) bool { return !strings.HasPrefix(t, "hostkeys-") })

	done := make(chan struct{}, 2)
	go func() { _ = down.Wait(); done <- struct{}{} }()
	go func() { _ = up.Wait(); done <- struct{}{} }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	log.Info("ssh session closed")
}

// workspaceDials is how many times a workspace is tried, a second apart,
// before it is taken as unreachable: long enough for a pod being replaced.
var workspaceDials = 15

// dialWorkspace opens an SSH connection to a server's workspace, waiting
// briefly for a pod that is being replaced.
func (g *Gateway) dialWorkspace(ctx context.Context, namespace, server string) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	cfg := &ssh.ClientConfig{
		User: "mc",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(g.WorkspaceKey)},
		// The workspace is reached over the cluster's pod network (or the API
		// server's tunnel) by pod name, and its host key is generated per pod,
		// so there is nothing stable to pin here.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	var lastErr error
	for attempt := 0; attempt < workspaceDials; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		nc, err := g.Dialer.DialPod(ctx, namespace, server, controller.WorkspacePort)
		if err != nil {
			lastErr = err
			continue
		}
		conn, chans, reqs, err := ssh.NewClientConn(nc, server, cfg)
		if err != nil {
			nc.Close()
			lastErr = err
			continue
		}
		return conn, chans, reqs, nil
	}
	return nil, nil, nil, lastErr
}

// DialWorkspace returns an SSH client connected to a server's workspace, for
// the panel's own terminal and file browser. The caller must have authorized
// the user already.
func (g *Gateway) DialWorkspace(ctx context.Context, namespace, server string) (*ssh.Client, error) {
	conn, chans, reqs, err := g.dialWorkspace(ctx, namespace, server)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

// refuse accepts session channels only to say why nothing will happen.
func refuse(chans <-chan ssh.NewChannel, message string) {
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.ConnectionFailed, "workspace unreachable")
			continue
		}
		ch, reqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range reqs {
				start := req.Type == "shell" || req.Type == "exec" || req.Type == "subsystem"
				if req.WantReply {
					_ = req.Reply(start || req.Type == "pty-req" || req.Type == "env", nil)
				}
				if start {
					_, _ = ch.Stderr().Write([]byte(message))
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
					ch.Close()
				}
			}
		}()
	}
}
