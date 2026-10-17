package sshgw

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

func TestSplitTarget(t *testing.T) {
	for user, want := range map[string][2]string{
		"world.alpha":       {"world", "alpha"},
		"world.alpha.extra": {"world", "alpha.extra"},
	} {
		server, org, ok := splitTarget(user)
		if !ok || server != want[0] || org != want[1] {
			t.Errorf("%q: %q %q %v", user, server, org, ok)
		}
	}
	for _, user := range []string{"", "world", ".alpha", "world.", "."} {
		if _, _, ok := splitTarget(user); ok {
			t.Errorf("%q was taken as a target", user)
		}
	}
}

// signer is a new key. t is nil where there is no test to fail.
func signer(t *testing.T) ssh.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		panic(err)
	}
	return s
}

// allow lets one user into one server, and says no to anything else as the
// panel does.
type allow struct{ user, org, server string }

func (a allow) AuthorizeWorkspace(_ context.Context, u *store.User, org, server string) (string, error) {
	if u.Username == a.user && org == a.org && server == a.server {
		return "mc-" + org, nil
	}
	return "", errors.New("No server named " + server + "." + org + ", or you do not have shell access to it.")
}

// workspace stands in for a server's workspace sshd: it takes only the
// gateway's key, runs "say <text>" by echoing it with the user it was told,
// "fail" by writing to stderr and exiting 3, and offers its own host keys,
// which must not reach the person connecting.
type workspace struct {
	key   ssh.Signer
	mu    sync.Mutex
	dials int
	down  bool
}

func (w *workspace) DialPod(_ context.Context, namespace, pod string, port int) (net.Conn, error) {
	w.mu.Lock()
	w.dials++
	down := w.down
	w.mu.Unlock()
	if down {
		return nil, errors.New("no such pod")
	}
	// A real connection: over net.Pipe both ends of an SSH handshake write
	// their version first, and each waits for the other to read.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	ours, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, err
	}
	theirs, ok := <-accepted
	if !ok {
		ours.Close()
		return nil, errors.New("the workspace did not answer")
	}
	host := signer(nil)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(k.Marshal(), w.key.PublicKey().Marshal()) {
			return nil, errors.New("not the panel's key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(host)
	go func() {
		conn, chans, reqs, err := ssh.NewServerConn(theirs, cfg)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(reqs)
		// As OpenSSH does once someone is in.
		_, _, _ = conn.SendRequest("hostkeys-00@openssh.com", false, host.PublicKey().Marshal())
		for nc := range chans {
			ch, chReqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer ch.Close()
				env := map[string]string{}
				for req := range chReqs {
					switch req.Type {
					case "env":
						var kv struct{ Name, Value string }
						_ = ssh.Unmarshal(req.Payload, &kv)
						env[kv.Name] = kv.Value
						_ = req.Reply(true, nil)
					case "exec":
						var cmd struct{ Command string }
						_ = ssh.Unmarshal(req.Payload, &cmd)
						_ = req.Reply(true, nil)
						status := uint32(0)
						if text, ok := strings.CutPrefix(cmd.Command, "say "); ok {
							_, _ = io.WriteString(ch, text+" to "+env["AVSYS_USER"]+"\n")
						} else {
							_, _ = io.WriteString(ch.Stderr(), "boom\n")
							status = 3
						}
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
						return
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()
		}
	}()
	return ours, nil
}

// gateway is a gateway on a port of its own, with steve, who has a key, let
// into world.alpha only.
func gateway(t *testing.T) (addr string, steve ssh.Signer, ws *workspace, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u := &store.User{Username: "steve"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	steve = signer(t)
	if err := st.AddSSHKey(ctx, &store.SSHKey{UserID: u.ID, Name: "laptop", PublicKey: string(ssh.MarshalAuthorizedKey(steve.PublicKey())), Fingerprint: ssh.FingerprintSHA256(steve.PublicKey())}); err != nil {
		t.Fatal(err)
	}
	ws = &workspace{key: signer(t)}
	g := &Gateway{Store: st, Authz: allow{"steve", "alpha", "world"}, Dialer: ws, HostKey: signer(t), WorkspaceKey: ws.key, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = g.Serve(ctx, ln) }()
	return ln.Addr().String(), steve, ws, st
}

// connect signs in to the gateway as user with key, and returns what the
// gateway said before refusing, if it did.
func connect(t *testing.T, addr, user string, key ssh.Signer) (*ssh.Client, string, error) {
	t.Helper()
	var banner strings.Builder
	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		BannerCallback:  func(m string) error { banner.WriteString(m); return nil },
		Timeout:         5 * time.Second,
	})
	return c, banner.String(), err
}

// run runs a command over c, and returns its output, its errors and how it
// exited.
func run(t *testing.T, c *ssh.Client, command string) (string, string, int) {
	t.Helper()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var out, errs bytes.Buffer
	sess.Stdout, sess.Stderr = &out, &errs
	err = sess.Run(command)
	code := 0
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitStatus()
	} else if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return out.String(), errs.String(), code
}

// Someone with a key on their account reaches the server they may, as
// themselves, with everything the workspace says passed back whole.
func TestGatewayRelaysToTheWorkspace(t *testing.T) {
	addr, steve, _, _ := gateway(t)
	c, _, err := connect(t, addr, "world.alpha", steve)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if out, _, code := run(t, c, "say hello"); out != "hello to steve\n" || code != 0 {
		t.Errorf("a command: %q, exit %d", out, code)
	}
	// What failed says so, on stderr, with its own exit status.
	if out, errs, code := run(t, c, "fail"); out != "" || errs != "boom\n" || code != 3 {
		t.Errorf("a command that fails: %q %q, exit %d", out, errs, code)
	}
}

// Nobody gets in without a key on their account, and someone who has one is
// told why they cannot reach what they asked for.
func TestGatewayRefuses(t *testing.T) {
	addr, steve, ws, _ := gateway(t)
	if _, _, err := connect(t, addr, "world.alpha", signer(t)); err == nil {
		t.Error("a key on no account got in")
	}
	if _, banner, err := connect(t, addr, "steve", steve); err == nil || !strings.Contains(banner, "Connect to a server with: ssh <server>.<org>@") {
		t.Errorf("no server named: %q, %v", banner, err)
	}
	if _, banner, err := connect(t, addr, "other.alpha", steve); err == nil || !strings.Contains(banner, "No server named other.alpha, or you do not have shell access to it.") {
		t.Errorf("a server not theirs: %q, %v", banner, err)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.dials != 0 {
		t.Errorf("a workspace was dialed for someone refused: %d", ws.dials)
	}
}

// A workspace that cannot be reached is said to be so, once someone is in.
func TestGatewayWorkspaceUnreachable(t *testing.T) {
	was := workspaceDials
	workspaceDials = 2
	t.Cleanup(func() { workspaceDials = was })
	addr, steve, ws, _ := gateway(t)
	ws.down = true
	c, _, err := connect(t, addr, "world.alpha", steve)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, errs, code := run(t, c, "say hello")
	if code != 1 || !strings.Contains(errs, "The workspace for this server is not reachable right now.") {
		t.Errorf("%q, exit %d", errs, code)
	}
}
