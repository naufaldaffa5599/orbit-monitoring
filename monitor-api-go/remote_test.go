package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The point of the cache is that a reader never waits on a collect it could
// have been spared. These check the three cases that matter: the first reader
// waits, a later one does not, and an expired entry is refreshed behind the
// caller rather than in front of it.

func TestCachedFirstCallCollects(t *testing.T) {
	key := "\x00test-first"
	invalidate(key)

	got, err := cached(key, time.Minute, func() (int, error) { return 7, nil })
	if got != 7 || err != "" {
		t.Fatalf("cached() = %d, %q; want 7, \"\"", got, err)
	}

	// Second call inside the TTL must not run collect at all.
	got, _ = cached(key, time.Minute, func() (int, error) {
		t.Error("collect ran again while the snapshot was still fresh")
		return 0, nil
	})
	if got != 7 {
		t.Fatalf("cached() = %d; want the stored 7", got)
	}
}

func TestCachedServesStaleWhileRefreshing(t *testing.T) {
	key := "\x00test-stale"
	invalidate(key)

	// Past the TTL but well inside staleGrace, which is where the snapshot is
	// handed over on the spot and the refresh runs behind the caller.
	const ttl = 10 * time.Millisecond
	cached(key, ttl, func() (int, error) { return 1, nil })
	time.Sleep(2 * ttl)

	release := make(chan struct{})
	var calls atomic.Int32
	slow := func() (int, error) {
		calls.Add(1)
		<-release
		return 2, nil
	}

	// The value is stale, so this must come back with the old number now
	// rather than block on the collect that is about to run.
	start := time.Now()
	got, _ := cached(key, ttl, slow)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("stale read waited %v; it should not wait at all", elapsed)
	}
	if got != 1 {
		t.Fatalf("cached() = %d; want the stale 1", got)
	}

	// A second reader arriving mid-refresh must not start another collect.
	if got, _ = cached(key, ttl, slow); got != 1 {
		t.Fatalf("second stale read = %d; want the stale 1", got)
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if v, _ := cached(key, time.Minute, func() (int, error) { return 0, nil }); v == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never stored the new value")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("collect ran %d times; want exactly 1", n)
	}
}

func TestCachedConcurrentColdReadersShareOneCollect(t *testing.T) {
	key := "\x00test-thundering"
	invalidate(key)

	var calls atomic.Int32
	collect := func() (int, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return 42, nil
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, _ := cached(key, time.Minute, collect); got != 42 {
				t.Errorf("cached() = %d; want 42", got)
			}
		}()
	}
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Fatalf("collect ran %d times for 8 cold readers; want 1", n)
	}
}

func TestCachedKeepsValueWhenRefreshFails(t *testing.T) {
	key := "\x00test-blip"
	invalidate(key)

	const ttl = 10 * time.Millisecond
	cached(key, ttl, func() (int, error) { return 5, nil })
	time.Sleep(2 * ttl)

	// The refresh happens behind the reader, so the failure shows up on the
	// call after it — with the last good value still attached.
	cached(key, ttl, func() (int, error) { return 0, errors.New("node ngambek") })

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := cached(key, time.Minute, func() (int, error) { return 0, nil })
		if err != "" {
			if got != 5 {
				t.Fatalf("cached() = %d alongside the error; want the last good 5", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the failed refresh never surfaced its error")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Serving stale hides a refresh; it must not pass an old reading off as the
// current one. Past staleGrace multiples of the TTL the caller waits.
func TestCachedWaitsOnceTooStale(t *testing.T) {
	key := "\x00test-too-stale"
	invalidate(key)

	ttl := 10 * time.Millisecond
	cached(key, ttl, func() (int, error) { return 1, nil })
	time.Sleep(ttl * staleGrace)

	got, _ := cached(key, ttl, func() (int, error) {
		time.Sleep(30 * time.Millisecond)
		return 2, nil
	})
	if got != 2 {
		t.Fatalf("cached() = %d; want 2 — past staleGrace the caller waits for a real collect", got)
	}
}

// A stale value would be exactly the wrong answer right after a service was
// stopped, so invalidate has to drop it rather than only age it out.
func TestInvalidateForcesACollect(t *testing.T) {
	key := "\x00test-invalidate"
	invalidate(key)

	cached(key, time.Minute, func() (int, error) { return 1, nil })
	invalidate(key)

	got, _ := cached(key, time.Minute, func() (int, error) { return 2, nil })
	if got != 2 {
		t.Fatalf("cached() = %d after invalidate; want the recollected 2", got)
	}
}
