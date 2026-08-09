package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The gate is the whole point of the feature, and every one of these cases is
// a way it could be wrong while still looking like it works: guarding nothing,
// guarding the login endpoint itself, or leaving the terminal socket open
// because it is not under /api/.
func TestRequireAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})

	prev := dashboardPassword
	t.Cleanup(func() { dashboardPassword = prev })

	token, err := newSession(time.Minute)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	cases := []struct {
		name     string
		password string
		path     string
		cookie   string
		want     int
	}{
		{"no password configured leaves the API open", "", "/api/system", "", 200},
		{"guarded without a cookie", "hunter2", "/api/system", "", 401},
		{"guarded with a bogus cookie", "hunter2", "/api/system", "not-a-token", 401},
		{"guarded with a live session", "hunter2", "/api/system", token, 200},
		{"login endpoint stays reachable", "hunter2", "/api/auth/login", "", 200},
		{"status endpoint stays reachable", "hunter2", "/api/auth/me", "", 200},
		{"terminal socket is guarded too", "hunter2", "/ws/ssh/dev/sess", "", 401},
		{"terminal socket with a session", "hunter2", "/ws/ssh/dev/sess", token, 200},
		{"the bundle itself is not guarded", "hunter2", "/", "", 200},
		{"static assets are not guarded", "hunter2", "/assets/main.js", "", 200},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dashboardPassword = c.password
			r := httptest.NewRequest("GET", c.path, nil)
			if c.cookie != "" {
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
			}
			w := httptest.NewRecorder()
			requireAuth(ok).ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("%s → %d, want %d", c.path, w.Code, c.want)
			}
		})
	}
}

func TestSessionExpiry(t *testing.T) {
	live, err := newSession(time.Minute)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	if !validSession(live) {
		t.Error("a fresh session should be valid")
	}

	// Negative TTL rather than a sleep: the expiry is a timestamp comparison,
	// so there is nothing to wait for.
	dead, err := newSession(-time.Minute)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	if validSession(dead) {
		t.Error("an expired session should be rejected")
	}

	dropSession(live)
	if validSession(live) {
		t.Error("a dropped session should be rejected")
	}
	if validSession("") {
		t.Error("an empty token should never validate")
	}
}

func TestLoginThrottle(t *testing.T) {
	const ip = "10.0.0.7"
	t.Cleanup(func() { clearLoginFails(ip) })

	for i := 0; i < loginMaxFails-1; i++ {
		noteLoginFail(ip)
	}
	if loginBlocked(ip) {
		t.Errorf("blocked after %d failures, limit is %d", loginMaxFails-1, loginMaxFails)
	}

	noteLoginFail(ip)
	if !loginBlocked(ip) {
		t.Error("should be blocked once the limit is reached")
	}

	// A successful login has to clear the record, or one bad night would lock
	// the owner out for the rest of the window.
	clearLoginFails(ip)
	if loginBlocked(ip) {
		t.Error("a successful login should reset the counter")
	}
}

// Without this check any page on the internet could open a socket to the SSH
// endpoint in a visitor's browser and ride their cookie in.
func TestSameOriginWS(t *testing.T) {
	cases := []struct {
		origin string
		host   string
		want   bool
	}{
		{"", "192.168.100.105:8585", true}, // non-browser client, no cookie to steal
		{"http://192.168.100.105:8585", "192.168.100.105:8585", true},
		{"https://192.168.100.105:8585", "192.168.100.105:8585", true}, // scheme is not the boundary
		{"http://evil.example", "192.168.100.105:8585", false},
		{"http://192.168.100.105:9999", "192.168.100.105:8585", false}, // a different port is a different origin
		{"://nonsense", "192.168.100.105:8585", false},
	}

	for _, c := range cases {
		r := httptest.NewRequest("GET", "/ws/ssh/dev/sess", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := sameOriginWS(r); got != c.want {
			t.Errorf("origin %q on host %q → %v, want %v", c.origin, c.host, got, c.want)
		}
	}
}
