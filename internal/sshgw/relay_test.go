package sshgw

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand/v2"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// stream is n bytes of noise from seed, the same every time it is made.
type stream struct {
	rng  *rand.ChaCha8
	left int64
}

func newStream(seed byte, n int64) *stream {
	var s [32]byte
	s[0] = seed
	return &stream{rand.NewChaCha8(s), n}
}

func (s *stream) Read(p []byte) (int, error) {
	if s.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, _ := s.rng.Read(p)
	s.left -= int64(n)
	return n, nil
}

func sum(r io.Reader) ([]byte, int64) {
	h := sha256.New()
	n, _ := io.Copy(h, r)
	return h.Sum(nil), n
}

// Lots both ways at once through the gateway, as rsync sends: the workspace
// echoes what it is sent, says things on stderr, and asks the person's side
// whether it is still there, while that side resizes its window and pings the
// gateway. All of it arrives whole, in good time.
func TestGatewayCarriesLotsBothWays(t *testing.T) {
	size := int64(512 << 20)
	if testing.Short() {
		size = 64 << 20
	}
	addr, steve, _, _ := gateway(t)
	c, _, err := connect(t, addr, "world.alpha", steve)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	// The person's side answers what the workspace asks, as OpenSSH does.
	go func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()
	if ok, err := ch.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"echo"})); !ok || err != nil {
		t.Fatalf("exec: %v %v", ok, err)
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
			_, _ = ch.SendRequest("window-change", false, ssh.Marshal(struct{ W, H, PW, PH uint32 }{80, 24, 0, 0}))
			_, _ = ch.SendRequest("keepalive@openssh.com", true, nil)
			_, _, _ = c.SendRequest("keepalive@openssh.com", true, nil)
		}
	}()

	var wg sync.WaitGroup
	var sent, got, errs []byte
	var gotN int64
	wg.Add(3)
	go func() {
		defer wg.Done()
		h := sha256.New()
		_, _ = io.Copy(io.MultiWriter(ch, h), newStream(1, size))
		sent = h.Sum(nil)
		_ = ch.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		got, gotN = sum(ch)
	}()
	go func() {
		defer wg.Done()
		errs, _ = io.ReadAll(ch.Stderr())
	}()
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("the transfer stalled")
	}
	if gotN != size || !bytes.Equal(sent, got) {
		t.Errorf("sent %d bytes, got back %d, alike %v", size, gotN, bytes.Equal(sent, got))
	}
	if len(errs) == 0 {
		t.Error("nothing came on stderr")
	}
}

// echo is the workspace running a command that sends back all it is sent, as
// rsync's two ends keep each other busy, while saying things on stderr and
// asking the other side whether it is still there, as sshd does with
// ClientAliveInterval.
func echo(ch ssh.Channel, reqs <-chan *ssh.Request) {
	go func() {
		for req := range reqs {
			if req.WantReply {
				_ = req.Reply(req.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
			}
			_, _ = ch.SendRequest("keepalive@openssh.com", true, nil)
			_, _ = io.WriteString(ch.Stderr(), "still going\n")
		}
	}()
	_, _ = io.Copy(ch, ch)
	close(done)
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
}

// hold is a command that takes nothing it is sent and never ends by itself,
// as `sleep` or a local copy do, and tells seen of the signals it is sent and
// of its session being closed.
func hold(ch ssh.Channel, reqs <-chan *ssh.Request, seen chan<- string) {
	for req := range reqs {
		if req.Type == "signal" {
			var sig struct{ Signal string }
			_ = ssh.Unmarshal(req.Payload, &sig)
			seen <- "signal " + sig.Signal
		}
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
	seen <- "closed"
}

// orphan is a command that says something and exits, leaving behind
// something that holds its output open, as `sleep 600 &` does: the session
// goes on until it is closed.
func orphan(ch ssh.Channel, reqs <-chan *ssh.Request, seen chan<- string) {
	_, _ = io.WriteString(ch, "started\n")
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	for req := range reqs {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
	seen <- "closed"
}

// cuttable is a connection that can be made to go silent without closing, as
// one to a pod that was taken away without a word does: nothing more comes
// from it, and what is written to it goes nowhere.
type cuttable struct {
	net.Conn
	cut       chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newCuttable(c net.Conn) *cuttable {
	return &cuttable{Conn: c, cut: make(chan struct{}), closed: make(chan struct{})}
}

func (c *cuttable) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	select {
	case <-c.cut:
		<-c.closed
		return 0, net.ErrClosed
	default:
		return n, err
	}
}

func (c *cuttable) Write(p []byte) (int, error) {
	select {
	case <-c.cut:
		return len(p), nil
	default:
		return c.Conn.Write(p)
	}
}

func (c *cuttable) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

var quick = timing{askEvery: 200 * time.Millisecond, quietMost: time.Second, closeWait: 500 * time.Millisecond, leftQuiet: 300 * time.Millisecond}

// within waits for what fn waits for, failing the test after limit.
func within(t *testing.T, limit time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s: still waiting after %s", what, limit)
	}
}

// expectSeen waits for the workspace to tell of each of want, in order.
func expectSeen(t *testing.T, ws *workspace, want ...string) {
	t.Helper()
	for _, w := range want {
		select {
		case got := <-ws.seen:
			if got != w {
				t.Fatalf("the workspace saw %q, expected %q", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the workspace never saw %q", w)
		}
	}
}

// start runs command on a session channel of its own, which the test drives.
func start(t *testing.T, c *ssh.Client, command string) ssh.Channel {
	t.Helper()
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, err := ch.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{command})); !ok || err != nil {
		t.Fatalf("exec %s: %v %v", command, ok, err)
	}
	return ch
}

// A workspace that stops answering, as one whose pod was taken away can, is
// let go of, and so is the person: they are not left waiting on nothing.
func TestGatewayLetsGoOfAWorkspaceGoneSilent(t *testing.T) {
	addr, steve, ws, _ := gateway(t, quick)
	c, _, err := connect(t, addr, "world.alpha", steve)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ch := start(t, c, "hold")
	defer ch.Close()
	ws.mu.Lock()
	close(ws.conns[0].cut)
	ws.mu.Unlock()
	within(t, 5*time.Second, "the person's connection to end", func() { _ = c.Wait() })
}

// What someone leaves running when they go is told they have gone, and its
// session ends: nothing they started is left behind, though it was not
// reading what they sent.
func TestGatewayHangsUpWhenThePersonGoes(t *testing.T) {
	for _, how := range []string{"connection", "channel"} {
		t.Run(how, func(t *testing.T) {
			addr, steve, ws, _ := gateway(t, quick)
			c, _, err := connect(t, addr, "world.alpha", steve)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ch := start(t, c, "hold")
			// More than the workspace would take, were it reading: the
			// gateway is left holding some of it.
			go func() { _, _ = io.Copy(ch, newStream(1, 8<<20)) }()
			time.Sleep(200 * time.Millisecond)
			if how == "connection" {
				c.Close()
			} else {
				ch.Close()
			}
			expectSeen(t, ws, "signal HUP", "closed")
		})
	}
}

// A command that has exited ends its session, with its output and exit
// status, though what it started still holds its output open.
func TestGatewayEndsASessionWhenItsCommandExits(t *testing.T) {
	addr, steve, ws, _ := gateway(t, quick)
	c, _, err := connect(t, addr, "world.alpha", steve)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	within(t, 5*time.Second, "the command to end", func() {
		if out, _, code := run(t, c, "orphan"); out != "started\n" || code != 0 {
			t.Errorf("%q, exit %d", out, code)
		}
	})
	expectSeen(t, ws, "closed")
	// The connection goes on as before.
	if out, _, code := run(t, c, "say again"); out != "again to steve\n" || code != 0 {
		t.Errorf("after: %q, exit %d", out, code)
	}
}
