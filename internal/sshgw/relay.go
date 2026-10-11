package sshgw

import (
	"errors"
	"io"

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
// anything else is forwarded.
func relayChannels(dst ssh.Conn, chans <-chan ssh.NewChannel, env map[string]string) {
	for nc := range chans {
		go func() {
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
			if nc.ChannelType() == "session" {
				for name, value := range env {
					_, _ = out.SendRequest("env", false, ssh.Marshal(struct{ Name, Value string }{name, value}))
				}
			}
			splice(in, inReqs, out, outReqs)
		}()
	}
}

// splice joins two channels until either side closes, taking care that
// everything the closing side sent (output, stderr, exit status) is delivered
// before the other side is closed.
func splice(a ssh.Channel, aReqs <-chan *ssh.Request, b ssh.Channel, bReqs <-chan *ssh.Request) {
	type half struct{ data, stderr, reqs chan struct{} }
	run := func(dst, src ssh.Channel, srcReqs <-chan *ssh.Request) half {
		h := half{make(chan struct{}), make(chan struct{}), make(chan struct{})}
		go func() {
			_, _ = io.Copy(dst, src)
			_ = dst.CloseWrite()
			close(h.data)
		}()
		go func() {
			_, _ = io.Copy(dst.Stderr(), src.Stderr())
			close(h.stderr)
		}()
		go func() {
			for req := range srcReqs {
				ok, err := dst.SendRequest(req.Type, req.WantReply, req.Payload)
				if req.WantReply {
					_ = req.Reply(ok && err == nil, nil)
				}
			}
			close(h.reqs)
		}()
		return h
	}
	fromA, fromB := run(b, a, aReqs), run(a, b, bReqs)

	// A channel's request stream ends when that side closes the channel.
	closing := fromA
	select {
	case <-fromA.reqs:
	case <-fromB.reqs:
		closing = fromB
	}
	<-closing.data
	<-closing.stderr
	a.Close()
	b.Close()
}
