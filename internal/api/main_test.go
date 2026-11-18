package api

import (
	"os"
	"testing"
	"time"
)

// mojangAsSet is how Mojang is asked again as the panel ships, before the
// tests change it.
var mojangAsSet = struct {
	tries        int
	try, backoff time.Duration
}{mojangTries, mojangTry, mojangBackoff}

func TestMain(m *testing.M) {
	// Tests whose panels ask Mojang's real site, where it cannot be
	// reached, do not wait between tries.
	mojangBackoff = time.Millisecond
	os.Exit(m.Run())
}
