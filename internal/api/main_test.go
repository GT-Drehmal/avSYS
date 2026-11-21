package api

import (
	"os"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/mojang"
)

// mojangAsSet is how Mojang is asked again as the panel ships, before the
// tests change it.
var mojangAsSet = struct {
	tries              int
	try, backoff, dial time.Duration
}{mojang.Tries, mojang.Try, mojang.Backoff, mojang.Dial}

func TestMain(m *testing.M) {
	// Tests whose panels ask Mojang's real site, where it cannot be
	// reached, do not wait between tries.
	mojang.Backoff = time.Millisecond
	os.Exit(m.Run())
}

// askMojang has Mojang asked again with these tries and waits until the
// test ends.
func askMojang(t *testing.T, try, backoff, dial time.Duration) {
	t.Helper()
	was := struct{ try, backoff, dial time.Duration }{mojang.Try, mojang.Backoff, mojang.Dial}
	mojang.Try, mojang.Backoff, mojang.Dial = try, backoff, dial
	t.Cleanup(func() { mojang.Try, mojang.Backoff, mojang.Dial = was.try, was.backoff, was.dial })
}
