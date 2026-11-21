// Package mojang is how the panel and the router ask Mojang's sites
// anything: its version lists, who a name or UUID is, skins, and whether a
// player joined.
//
// From some networks, a new connection to Mojang's sites (Azure Front
// Door's 150.171.110.x) now and then gets no answer at all: about 1 in 6
// from a home line, and from a cluster's pods, whose NAT picks source ports
// at random, 1 in 4 to 7. It is the source port: one that fails fails every
// time, and a new connection, from another port, almost always works. So a
// connection that does not answer is given up soon, and the request is made
// again on a new one, a few times, a moment apart.
package mojang

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	// Dial is how long connecting may take: a connection with no answer
	// is given up after this, and the next try makes a new one.
	Dial = 4 * time.Second
	// Tries is how many times a request is made at most, Try how long each
	// may take (connecting, Dial of it), and Backoff how long is waited
	// after the first that fails, twice that after each next: 5 tries in
	// under 40 seconds, about 28 where it is connecting that fails.
	Tries   = 5
	Try     = 6 * time.Second
	Backoff = 500 * time.Millisecond
)

// Transport is a transport for asking Mojang, and any site besides: it
// gives up connecting after Dial. Each request it cannot put on a
// connection that is already open makes a new one; one that has stopped
// answering is found out by a ping and closed, so the next try does not
// wait on it.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: Dial, KeepAlive: 30 * time.Second}).DialContext
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: 5 * time.Second, PingTimeout: 3 * time.Second}
	return t
}

// Is says whether a host is one of Mojang's.
func Is(host string) bool {
	host = strings.ToLower(host)
	return strings.HasSuffix(host, ".mojang.com") || strings.HasSuffix(host, ".minecraftservices.com") || host == "textures.minecraft.net"
}

// Again says whether an answer of this status is worth asking for again:
// none at all (0), or one that says to come back later. Anything else,
// such as 404 for a name nobody has, is Mojang's answer.
func Again(status int) bool {
	return status == 0 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// Do makes a request up to Tries times, until try says it is done: with no
// error, or with one not worth trying again for (again false). Each try has
// Try to finish, and n is which it is, from 0. Do gives up when ctx ends,
// with the last try's error.
func Do(ctx context.Context, try func(ctx context.Context, n int) (again bool, err error)) error {
	wait := Backoff
	var err error
	for n := range Tries {
		if n > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(wait):
			}
			wait *= 2
		}
		one, cancel := context.WithTimeout(ctx, Try)
		var again bool
		again, err = try(one, n)
		cancel()
		if err == nil || !again || ctx.Err() != nil {
			return err
		}
	}
	return err
}
