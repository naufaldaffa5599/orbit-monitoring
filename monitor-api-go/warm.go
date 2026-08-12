package main

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Keeping the cache warm.
//
// cached serves a stale snapshot instantly, which fixes the stall — but only
// once there is something stale to serve. Left to the request path alone, a
// node's snapshot exists only while its own page is open: switch to another
// machine, spend a minute there, come back, and the first request is a cold
// collect again, waiting on an SSH round trip and the summary script's own
// `sleep 1`.
//
// So a loop keeps every node that can answer refreshed on its own clock. It is
// the same work the dashboard was going to ask for anyway, moved off the click.
// Clicking through the tree then costs a cache read.

const (
	// How often the loop looks for snapshots worth refreshing.
	warmInterval = 10 * time.Second
	// The age it refreshes at. Below remoteTTL on purpose: a request landing
	// between two ticks still finds a snapshot the request path calls fresh,
	// so it never even has to kick a refresh of its own.
	warmAge = 9 * time.Second
	// How long warming outlives the last request. Nothing is watching a closed
	// dashboard, and SSHing into the whole fleet forever to answer nobody is
	// exactly the cost this is meant to avoid. Comfortably longer than the
	// slowest poll in the frontend (the tree, at 30s).
	warmWindow = 3 * time.Minute
)

// lastRequest is when an API handler last ran, in Unix nanoseconds.
var lastRequest atomic.Int64

// markActive records that somebody is using the dashboard. Called from the
// handler wrapper, so every /api route counts and nothing has to opt in.
func markActive() { lastRequest.Store(time.Now().UnixNano()) }

func activeWithin(window time.Duration) bool {
	at := lastRequest.Load()
	return at != 0 && time.Since(time.Unix(0, at)) < window
}

// warmSummaries refreshes the fleet's snapshots while the dashboard is in use.
func warmSummaries() {
	for range time.Tick(warmInterval) {
		if !activeWithin(warmWindow) {
			continue
		}
		warmOnce()
	}
}

func warmOnce() {
	// buildTree has caches of its own — the guest list and the reachability
	// probe — and this call refreshes those too, which is half the point: they
	// sit on the path of every per-node request.
	var wg sync.WaitGroup
	for _, n := range buildTree() {
		if !collectible(&n) {
			continue
		}
		// A machine already known to be off has nothing to collect, and dialing
		// it every ten seconds only to time out helps nobody. It comes back on
		// its own: the reachability probe flips it to online first.
		if n.Status == "offline" {
			continue
		}
		wg.Add(1)
		go func(n Node) {
			defer wg.Done()
			dev := devices.get(n.DeviceID)
			cached(cacheKey(n.ID, "summary"), warmAge,
				func() (NodeSummary, error) { return collectSummary(&n, dev) })
		}(n)
	}
	wg.Wait()
}

// withActivity marks the dashboard as in use around a handler.
func withActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markActive()
		next.ServeHTTP(w, r)
	})
}
