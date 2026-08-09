package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Agentless collection over SSH.
//
// Nothing is installed on the targets: every metric is a shell command whose
// output is parsed here. Two things make that affordable.
//
//   - Connections are pooled per device. An SSH handshake costs far more than
//     the command itself, and every open dashboard polls every few seconds.
//   - Results are cached behind a TTL and refreshed by whoever asks first, so
//     five dashboards watching one node still produce one command per cycle.
//
// This is the same shape as the temperature poller that already existed, just
// generalised to any node and any payload.

const (
	// How long a collected snapshot stays servable before a request triggers a
	// refresh. Roughly matches the frontend's own poll interval.
	remoteTTL = 12 * time.Second
	// A node that is off or unreachable must not make the whole dashboard wait.
	remoteCmdTimeout = 12 * time.Second
	// After a failed connect, don't retry on every single request.
	remoteBackoff = 20 * time.Second
)

// ── Connection pool ─────────────────────────────────────────────────────────

type pooledClient struct {
	client   *ssh.Client
	lastUsed time.Time
}

var sshPool = struct {
	sync.Mutex
	m        map[string]*pooledClient
	failedAt map[string]time.Time
	lastErr  map[string]string
}{
	m:        map[string]*pooledClient{},
	failedAt: map[string]time.Time{},
	lastErr:  map[string]string{},
}

// poolClient returns a live SSH client for the device, dialing if needed.
//
// Deliberately separate from the terminal's session registry: closing a shell
// tab must not tear down monitoring, and a monitoring hiccup must not disturb
// an open terminal.
func poolClient(d *Device) (*ssh.Client, error) {
	sshPool.Lock()
	if pc := sshPool.m[d.ID]; pc != nil {
		pc.lastUsed = time.Now()
		client := pc.client
		sshPool.Unlock()
		return client, nil
	}
	if at, ok := sshPool.failedAt[d.ID]; ok && time.Since(at) < remoteBackoff {
		err := sshPool.lastErr[d.ID]
		sshPool.Unlock()
		return nil, fmt.Errorf("%s", err)
	}
	sshPool.Unlock()

	client, err := sshConnect(d)

	sshPool.Lock()
	defer sshPool.Unlock()
	if err != nil {
		sshPool.failedAt[d.ID] = time.Now()
		sshPool.lastErr[d.ID] = err.Error()
		return nil, err
	}
	delete(sshPool.failedAt, d.ID)
	delete(sshPool.lastErr, d.ID)
	sshPool.m[d.ID] = &pooledClient{client: client, lastUsed: time.Now()}
	return client, nil
}

// dropClient forgets a connection so the next call redials. Called whenever a
// command fails, since the usual cause is a connection that died quietly
// (target rebooted, network dropped, sshd restarted).
func dropClient(deviceID string) {
	sshPool.Lock()
	pc := sshPool.m[deviceID]
	delete(sshPool.m, deviceID)
	sshPool.Unlock()
	if pc != nil {
		_ = pc.client.Close()
	}
}

// closeDeviceMonitoring drops a device's monitoring connection and cached
// data. Called when a device is deleted so nothing keeps dialing a host that
// is no longer in the registry.
func closeDeviceMonitoring(deviceID string) {
	dropClient(deviceID)
	remoteCache.Lock()
	for key := range remoteCache.m {
		if strings.HasPrefix(key, deviceID+"\x00") {
			delete(remoteCache.m, key)
		}
	}
	remoteCache.Unlock()
}

// runRemote executes one command on a device, redialing once if the pooled
// connection turned out to be stale.
func runRemote(d *Device, cmd string, timeout time.Duration) (int, string, error) {
	client, err := poolClient(d)
	if err != nil {
		return -1, "", err
	}
	rc, out := sshRun(client, cmd, timeout)
	if rc == -1 {
		// -1 means the session could not even start: almost always a dead
		// connection rather than a failing command.
		dropClient(d.ID)
		client, err = poolClient(d)
		if err != nil {
			return -1, "", err
		}
		rc, out = sshRun(client, cmd, timeout)
		if rc == -1 {
			return -1, "", fmt.Errorf("perintah gagal dijalankan di %s", d.Label)
		}
	}
	return rc, out, nil
}

// psCommand wraps a PowerShell one-liner for a Windows target. -NoProfile
// keeps a slow user profile from adding a second to every poll.
func psCommand(script string) string {
	return `powershell -NoProfile -NonInteractive -Command "` +
		strings.ReplaceAll(script, `"`, `\"`) + `"`
}

// ── Snapshot cache ──────────────────────────────────────────────────────────

type cacheEntry struct {
	mu      sync.Mutex // serialises refreshes so N readers cause 1 command
	value   any
	at      time.Time
	err     string
	loading bool
}

var remoteCache = struct {
	sync.Mutex
	m map[string]*cacheEntry
}{m: map[string]*cacheEntry{}}

func entryFor(key string) *cacheEntry {
	remoteCache.Lock()
	defer remoteCache.Unlock()
	e := remoteCache.m[key]
	if e == nil {
		e = &cacheEntry{}
		remoteCache.m[key] = e
	}
	return e
}

// cached returns a snapshot no older than ttl, calling collect only when the
// cached copy has expired. Concurrent callers for the same key share one call.
//
// A failed refresh does NOT discard the previous value: a node that blips for
// one cycle keeps showing its last known state, with the error attached, which
// is far more useful than a dashboard that empties itself.
func cached[T any](key string, ttl time.Duration, collect func() (T, error)) (T, string) {
	e := entryFor(key)
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.at.IsZero() && time.Since(e.at) < ttl {
		if v, ok := e.value.(T); ok {
			return v, e.err
		}
	}

	value, err := collect()
	if err != nil {
		e.err = err.Error()
		if v, ok := e.value.(T); ok {
			return v, e.err // keep serving the stale snapshot
		}
		var zero T
		return zero, e.err
	}
	e.value = value
	e.err = ""
	e.at = time.Now()
	return value, ""
}

func cacheKey(deviceID, what string) string { return deviceID + "\x00" + what }
