package api

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// stopsAnswering is a workspace's end of a connection that, once stopped,
// takes what is sent and never answers: a pod on a node that froze.
type stopsAnswering struct {
	net.Conn
	stopped atomic.Bool
	gone    chan struct{}
}

func (s *stopsAnswering) Read(p []byte) (int, error) {
	n, err := s.Conn.Read(p)
	if s.stopped.Load() {
		<-s.gone
		return 0, net.ErrClosed
	}
	return n, err
}

// A kept connection to a server's files whose pod stopped answering, as on
// a node that is down, is given up on within a few seconds, and does not
// hold up the files of any other server meanwhile.
func TestAKeptConnectionThatStopsAnsweringIsGivenUp(t *testing.T) {
	p := newPanel(t)
	was := probeWait
	probeWait = 300 * time.Millisecond
	t.Cleanup(func() { probeWait = was })

	c, end := net.Pipe()
	frozen := &stopsAnswering{Conn: end, gone: make(chan struct{})}
	server := sftp.NewRequestServer(frozen, sftp.InMemHandler())
	go func() { _ = server.Serve() }()
	sc, err := sftp.NewClientPipe(c, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(frozen.gone); c.Close(); end.Close() })
	used := &atomic.Int64{}
	used.Store(time.Now().UnixNano())
	p.srv.workspaces.mu.Lock()
	p.srv.workspaces.conns[p.srv.Namespace("alpha")+"/world"] = &pooledSFTP{sftp: sc, lastUsed: used}
	p.srv.workspaces.mu.Unlock()
	frozen.stopped.Store(true)

	answered := make(chan int, 2)
	go func() {
		code, _ := filesCall(t, p, "GET", "/content?path=/server.properties", "")
		answered <- code
	}()
	// Another server's files meanwhile: not held up by this one.
	go func() {
		_, err := p.srv.workspaces.get(t.Context(), p.srv.Namespace("alpha"), "other")
		if err == nil {
			t.Error("reached a workspace with no gateway")
		}
		answered <- 0
	}()
	for range 2 {
		select {
		case code := <-answered:
			if code != 0 && code != 502 {
				t.Errorf("answered %d, want 502: the files are not reachable", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a file request waited on a connection to a pod that does not answer")
		}
	}
	p.srv.workspaces.mu.Lock()
	defer p.srv.workspaces.mu.Unlock()
	if p.srv.workspaces.conns[p.srv.Namespace("alpha")+"/world"] != nil {
		t.Error("the connection that does not answer is still kept")
	}
}
