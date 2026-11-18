package sshgw

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// relayRequests forwards connection-level requests (keepalives, remote
// forwarding setup) to dst. allow, if set, filters by request type.
func relayRequests(dst ssh.Conn, reqs <-chan *ssh.Request, allow func(reqType string) bool) {
	for req := range reqs {
		if allow != nil && !allow(req.Type) {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		ok, payload, err := dst.SendRequest(req.Type, req.WantReply, req.Payload)
		if req.WantReply {
			_ = req.Reply(ok && err == nil, payload)
		}
	}
}

// relayChannels opens a matching channel on dst for every channel offered in
// chans and splices the two together. env is set on session channels before
// anything else is forwarded, and is only given for channels the person
// opens, whose sessions run commands in the workspace. sessions, if set,
// counts the channels being relayed, and holds one count for this, which it
// gives up once chans has ended.
func relayChannels(dst ssh.Conn, chans <-chan ssh.NewChannel, env map[string]string, sessions *sync.WaitGroup, waits timing) {
	if sessions != nil {
		defer sessions.Done()
	}
	for nc := range chans {
		if sessions != nil {
			sessions.Add(1)
		}
		go func() {
			if sessions != nil {
				defer sessions.Done()
			}
			out, outReqs, err := dst.OpenChannel(nc.ChannelType(), nc.ExtraData())
			if err != nil {
				var open *ssh.OpenChannelError
				if errors.As(err, &open) {
					_ = nc.Reject(open.Reason, open.Message)
				} else {
					_ = nc.Reject(ssh.ConnectionFailed, err.Error())
				}
				return
			}
			in, inReqs, err := nc.Accept()
			if err != nil {
				out.Close()
				return
			}
			command := env != nil && nc.ChannelType() == "session"
			if command {
				for name, value := range env {
					_, _ = out.SendRequest("env", false, ssh.Marshal(struct{ Name, Value string }{name, value}))
				}
			}
			splice(in, inReqs, out, outReqs, command, waits)
		}()
	}
}

// splice joins two channels until either side closes, taking care that
// everything the closing side sent (output, stderr, exit status) is delivered
// before the other side is closed. a is the side that opened the channel. With
// command set, a is the person and b a session in the workspace: when the
// person goes, the command is told to hang up, and once the command has exited
// the session ends when its output goes quiet, as it does with a terminal,
// though what it left running in the background still holds that output open.
func splice(a ssh.Channel, aReqs <-chan *ssh.Request, b ssh.Channel, bReqs <-chan *ssh.Request, command bool, waits timing) {
	run := func(dst, src ssh.Channel, srcReqs <-chan *ssh.Request) *half {
		h := &half{data: make(chan struct{}), stderr: make(chan struct{}), sent: make(chan struct{}), reqs: make(chan struct{}),
			busy: make(chan struct{}, 1), exited: make(chan struct{})}
		go func() {
			_, _ = io.Copy(counted{dst, h}, src)
			close(h.data)
		}()
		go func() {
			_, _ = io.Copy(counted{dst.Stderr(), h}, src.Stderr())
			close(h.stderr)
		}()
		// The end of what was sent only once both are through: after it,
		// nothing more can be written, and stderr that came after the
		// output ended would be lost.
		go func() {
			<-h.data
			<-h.stderr
			_ = dst.CloseWrite()
			close(h.sent)
		}()
		go func() {
			exited := false
			for req := range srcReqs {
				h.busy <- struct{}{}
				ok, err := dst.SendRequest(req.Type, req.WantReply, req.Payload)
				if req.WantReply {
					_ = req.Reply(ok && err == nil, nil)
				}
				<-h.busy
				if !exited && (req.Type == "exit-status" || req.Type == "exit-signal") {
					exited = true
					close(h.exited)
				}
			}
			close(h.reqs)
		}()
		return h
	}
	fromA, fromB := run(b, a, aReqs), run(a, b, bReqs)
	defer b.Close()
	defer a.Close()

	// The command's exit, which only matters for a command.
	var exited <-chan struct{}
	if command {
		exited = fromB.exited
	}
	var quiet <-chan time.Time
	var lastMoved int64
	for {
		select {
		case <-fromA.reqs:
			// The side that opened the channel closed it: nothing more can
			// reach it. What it sent last is passed on, if the other side
			// takes it.
			select {
			case <-fromA.sent:
			case <-time.After(waits.closeWait):
			}
			if command {
				select {
				case <-fromB.exited:
				default:
					// Nobody is there for it any more: as when a terminal
					// closes, the command is told to hang up.
					_, _ = b.SendRequest("signal", false, ssh.Marshal(struct{ Signal string }{"HUP"}))
				}
			}
			return
		case <-fromB.reqs:
			// A channel's request stream ends when that side closes the
			// channel: everything it sent is passed on first.
			<-fromB.sent
			// A reply the closing side gave just before it closed is passed
			// back first: a command that ends at once has its exec answered,
			// and the person's side does not see the channel end before it
			// started.
			select {
			case fromA.busy <- struct{}{}:
			case <-time.After(waits.closeWait):
			}
			return
		case <-exited:
			// The command has exited, and its output is passed on until it
			// ends or goes quiet.
			exited = nil
			lastMoved = fromB.moved.Load()
			quiet = time.After(waits.leftQuiet)
		case <-quiet:
			moved := fromB.moved.Load()
			if moved != lastMoved || fromB.writing.Load() > 0 {
				lastMoved = moved
				quiet = time.After(waits.leftQuiet)
				continue
			}
			// Only what the command left behind holds its output open: the
			// person's side is told it has all there is, and the session in
			// the workspace is closed, which closes that output.
			_ = a.CloseWrite()
			return
		}
	}
}

// half is one direction of a splice.
type half struct {
	data, stderr, sent, reqs chan struct{}
	// busy is held while a request is passed on and its reply passed back.
	busy chan struct{}
	// exited is closed once an exit status has been passed on.
	exited chan struct{}
	// moved counts what has been passed on, and writing is how many writes
	// to the other side are waiting to go through.
	moved   atomic.Int64
	writing atomic.Int32
}

// counted is a writer that keeps a half's count of what it passed on and of
// writes still waiting to go through.
type counted struct {
	io.Writer
	h *half
}

func (c counted) Write(p []byte) (int, error) {
	c.h.writing.Add(1)
	defer c.h.writing.Add(-1)
	n, err := c.Writer.Write(p)
	c.h.moved.Add(int64(n))
	return n, err
}
