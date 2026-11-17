package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mustProxies(t *testing.T, list string) *Server {
	t.Helper()
	trusted, err := ParseTrustedProxies(list)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Config: Config{TrustedProxies: trusted}}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 127.0.0.1, 100.64.0.0/10 ,::1,fd7a:115c:a1e0::/48,::ffff:10.0.0.0/104,10.1.2.3/8,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1/32", "100.64.0.0/10", "::1/128", "fd7a:115c:a1e0::/48", "10.0.0.0/8", "10.0.0.0/8"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("entry %d is %s, want %s", i, got[i], want[i])
		}
	}
	if got, err := ParseTrustedProxies(""); err != nil || len(got) != 0 {
		t.Errorf("empty: got %v, %v; want none", got, err)
	}
	for _, bad := range []string{"localhost", "10.0.0.0/33", "1.2.3", "127.0.0.1:80", "fe80::1%eth0/64"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestClientAddress(t *testing.T) {
	const proxies = "127.0.0.1,100.64.0.0/10,fd7a:115c:a1e0::/48"
	cases := []struct {
		name    string
		trusted string
		remote  string
		xff     []string // each a header of its own
		realIP  string
		want    string
	}{
		// None trusted: the connection's own, whatever the headers say.
		{name: "nothing trusted", remote: "203.0.113.7:5000", xff: []string{"1.1.1.1"}, realIP: "2.2.2.2", want: "203.0.113.7"},
		{name: "nothing trusted, a proxy", remote: "127.0.0.1:5000", xff: []string{"1.1.1.1"}, want: "127.0.0.1"},
		{name: "no port", remote: "203.0.113.7", want: "203.0.113.7"},
		{name: "not an address", trusted: proxies, remote: "pipe", xff: []string{"1.1.1.1"}, want: "pipe"},

		// An untrusted peer's headers are its own say-so.
		{name: "untrusted peer, spoofed", trusted: proxies, remote: "203.0.113.7:5000", xff: []string{"127.0.0.1"}, want: "203.0.113.7"},
		{name: "untrusted peer, real ip", trusted: proxies, remote: "203.0.113.7:5000", realIP: "198.51.100.1", want: "203.0.113.7"},

		// A trusted peer: the right-most entry that is not a proxy.
		{name: "one hop", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "client's own entry left of it", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"6.6.6.6, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "two proxies", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"6.6.6.6, 198.51.100.1, 100.101.102.103"}, want: "198.51.100.1"},
		{name: "spoofed proxy left of client", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"100.64.0.1, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "several headers are one list", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"6.6.6.6", "198.51.100.1, 100.64.0.9"}, want: "198.51.100.1"},
		{name: "every entry a proxy", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"100.64.0.5, 100.64.0.9"}, want: "100.64.0.5"},
		{name: "with a port", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"198.51.100.1:443"}, want: "198.51.100.1"},
		{name: "empty header", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{""}, want: "127.0.0.1"},

		// Not an address: the walk stops at the last one that was.
		{name: "garbage right-most", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"198.51.100.1, nonsense"}, want: "127.0.0.1"},
		{name: "garbage past a proxy", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"nonsense, 100.64.0.9"}, want: "100.64.0.9"},
		{name: "garbage left of client", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"<script>, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "empty entry", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"198.51.100.1,,"}, want: "127.0.0.1"},
		{name: "a range is not an address", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"10.0.0.0/8"}, want: "127.0.0.1"},

		// IPv6.
		{name: "v6 peer and client", trusted: proxies, remote: "[fd7a:115c:a1e0::1]:443", xff: []string{"2001:db8::5"}, want: "2001:db8::5"},
		{name: "v6 client bracketed with port", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"[2001:db8::5]:1234"}, want: "2001:db8::5"},
		{name: "v6 client bracketed", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"[2001:db8::5]"}, want: "2001:db8::5"},
		{name: "v6 zone dropped", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"fe80::1%eth0"}, want: "fe80::1"},
		{name: "v6 untrusted peer", trusted: proxies, remote: "[2001:db8::9]:443", xff: []string{"2001:db8::5"}, want: "2001:db8::9"},
		{name: "v4 as v6 peer", trusted: proxies, remote: "[::ffff:127.0.0.1]:5000", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "v4 as v6 entry", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"198.51.100.1, ::ffff:100.64.0.9"}, want: "198.51.100.1"},

		// X-Real-IP, only with no X-Forwarded-For.
		{name: "real ip", trusted: proxies, remote: "127.0.0.1:5000", realIP: "198.51.100.1", want: "198.51.100.1"},
		{name: "forwarded-for goes first", trusted: proxies, remote: "127.0.0.1:5000", xff: []string{"198.51.100.1"}, realIP: "6.6.6.6", want: "198.51.100.1"},
		{name: "real ip not an address", trusted: proxies, remote: "127.0.0.1:5000", realIP: "nonsense", want: "127.0.0.1"},
		{name: "no headers", trusted: proxies, remote: "127.0.0.1:5000", want: "127.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustProxies(t, c.trusted)
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if c.realIP != "" {
				r.Header.Set("X-Real-IP", c.realIP)
			}
			if got := s.clientAddress(r); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestSignInLimitsBehindAProxy: behind a trusted proxy, wrong passwords are
// counted per client the proxy forwards for, not for everyone at once; and
// a forwarded-for header from anyone else does not get around the count.
func TestSignInLimitsBehindAProxy(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	trusted, err := ParseTrustedProxies("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p.srv.Config.TrustedProxies = trusted

	login := func(remote, xff, password string) int {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(mustJSON(t, map[string]string{"username": "viewer", "password": password})))
		r.RemoteAddr = remote
		r.Header.Set("X-Avsys", "1")
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, r)
		return rec.Code
	}
	const attacker, someone = "198.51.100.66", "198.51.100.7"
	t.Cleanup(func() {
		for _, a := range []string{attacker, someone, "127.0.0.1", "203.0.113.9"} {
			forgetGuesses(a + "|viewer")
		}
	})
	for i := range 8 {
		if got := login("127.0.0.1:40000", attacker, "wrong"); got != 401 {
			t.Fatalf("guess %d: got %d, want 401", i+1, got)
		}
	}
	if got := login("127.0.0.1:40001", attacker, testPassword); got != 429 {
		t.Errorf("the ninth from the attacker: got %d, want 429", got)
	}
	// Someone else through the same proxy is not held off with them.
	if got := login("127.0.0.1:40002", someone, testPassword); got != 200 {
		t.Errorf("someone else through the proxy: got %d, want 200", got)
	}

	// From an address not trusted, the header is not read: the guesses
	// are counted against the connection, whatever it claims.
	for i := range 8 {
		claim := fmt.Sprintf("192.0.2.%d", i+1)
		if got := login("203.0.113.9:5000", claim, "wrong"); got != 401 {
			t.Fatalf("spoofed guess %d: got %d, want 401", i+1, got)
		}
	}
	if got := login("203.0.113.9:5000", "192.0.2.200", testPassword); got != 429 {
		t.Errorf("a fresh forwarded-for from an untrusted address: got %d, want 429", got)
	}
}

// TestSessionsShowTheForwardedAddress: Account's list of sessions shows the
// client's address, not the proxy's.
func TestSessionsShowTheForwardedAddress(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	trusted, err := ParseTrustedProxies("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p.srv.Config.TrustedProxies = trusted
	b := p.tab("Firefox")
	b.addr = "127.0.0.1:40000"
	b.header = map[string]string{"X-Forwarded-For": "198.51.100.7"}
	t.Cleanup(func() { forgetGuesses("198.51.100.7|viewer") })
	b.signIn("viewer", "")
	if list := b.sessions(); len(list) != 1 || list[0].Address != "198.51.100.7" {
		t.Errorf("sessions %+v, want one at 198.51.100.7", list)
	}
}
