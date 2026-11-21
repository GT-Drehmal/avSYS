package mojang

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIs(t *testing.T) {
	for host, want := range map[string]bool{
		"api.mojang.com":            true,
		"sessionserver.mojang.com":  true,
		"piston-meta.mojang.com":    true,
		"api.minecraftservices.com": true,
		"textures.minecraft.net":    true,
		"API.Mojang.com":            true,
		"mojang.com.example":        false,
		"api.modrinth.com":          false,
		"minecraft.net":             false,
	} {
		if Is(host) != want {
			t.Errorf("%s: %v", host, !want)
		}
	}
	for status, want := range map[int]bool{0: true, 200: false, 204: false, 400: false, 403: false, 404: false, 408: true, 429: true, 500: true, 503: true} {
		if Again(status) != want {
			t.Errorf("%d: again %v", status, !want)
		}
	}
}

// A try is made again until it is done, a moment longer apart each time,
// each with Try to finish; not past Tries, nor once the one asking gives up.
func TestDo(t *testing.T) {
	was := struct {
		tries        int
		try, backoff time.Duration
	}{Tries, Try, Backoff}
	defer func() { Tries, Try, Backoff = was.tries, was.try, was.backoff }()
	Tries, Try, Backoff = 4, 50*time.Millisecond, 10*time.Millisecond
	failed := errors.New("no answer")

	// Done on the third.
	var at []time.Time
	err := Do(context.Background(), func(ctx context.Context, n int) (bool, error) {
		at = append(at, time.Now())
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > Try {
			t.Errorf("try %d has no deadline of its own", n)
		}
		if n < 2 {
			return true, failed
		}
		return false, nil
	})
	if err != nil || len(at) != 3 || at[2].Sub(at[1]) < 2*Backoff {
		t.Errorf("%v after %d tries", err, len(at))
	}

	// An answer that is not worth asking again for is given at once.
	tries := 0
	err = Do(context.Background(), func(context.Context, int) (bool, error) { tries++; return false, failed })
	if !errors.Is(err, failed) || tries != 1 {
		t.Errorf("%v after %d tries", err, tries)
	}

	// Never answered: the last error, after Tries.
	tries = 0
	err = Do(context.Background(), func(ctx context.Context, _ int) (bool, error) { tries++; <-ctx.Done(); return true, ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) || tries != Tries {
		t.Errorf("%v after %d tries", err, tries)
	}

	// Given up by the one asking: no more tries.
	ctx, cancel := context.WithTimeout(context.Background(), Try+Backoff/2)
	defer cancel()
	tries = 0
	start := time.Now()
	_ = Do(ctx, func(ctx context.Context, _ int) (bool, error) { tries++; <-ctx.Done(); return true, ctx.Err() })
	if tries != 1 || time.Since(start) > Try+Backoff+20*time.Millisecond {
		t.Errorf("%d tries in %v", tries, time.Since(start))
	}
}
