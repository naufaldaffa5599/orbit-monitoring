package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A password gate in front of everything.
//
// This dashboard opens SSH terminals, restarts services, kills processes and
// reboots machines. Until now every one of those was a bare HTTP call anyone
// on the LAN could make. A login screen in the frontend alone would not have
// changed that — the gate has to be here, where the requests actually land.
//
// One password, no user accounts: there is exactly one person using this, and
// a user table would be ceremony around a single row.

const (
	sessionCookie = "orbit_session"

	// A session outlives a working day by a wide margin, because the thing
	// this protects is checked from a phone at odd hours and re-typing a
	// password on a phone keyboard is how people end up picking short ones.
	sessionTTL = 12 * time.Hour
	// "Remember me" trades that for a fortnight on trusted devices.
	sessionTTLRemembered = 14 * 24 * time.Hour

	// Brute-force ceiling. The delay alone would cap an attacker at a few
	// guesses a second; the lockout is what makes an overnight run pointless.
	loginFailWindow = 10 * time.Minute
	loginMaxFails   = 8
	loginFailDelay  = 400 * time.Millisecond
)

var dashboardPassword string // DASHBOARD_PASSWORD

// authEnabled reports whether a password was configured at all. With none set
// the gate stands aside completely: an operator who deploys this build without
// touching .env gets the behaviour they had yesterday rather than a dashboard
// they are locked out of. initConfig prints a warning in that case.
func authEnabled() bool { return dashboardPassword != "" }

// Sessions live in memory only. A restart signs everyone out, which is the
// same trade the notification state makes: persisting it would mean a store
// this app does not have, and re-typing a password after a deploy is cheap.
var authSessions = struct {
	sync.Mutex
	m map[string]time.Time // token → expiry
}{m: map[string]time.Time{}}

func newSession(ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	authSessions.Lock()
	defer authSessions.Unlock()
	// Sweep on write rather than on a timer: the map only grows when someone
	// logs in, so that is the only moment it can need pruning.
	now := time.Now()
	for t, exp := range authSessions.m {
		if now.After(exp) {
			delete(authSessions.m, t)
		}
	}
	authSessions.m[token] = now.Add(ttl)
	return token, nil
}

func validSession(token string) bool {
	if token == "" {
		return false
	}
	authSessions.Lock()
	defer authSessions.Unlock()
	exp, ok := authSessions.m[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(authSessions.m, token)
		return false
	}
	return true
}

func dropSession(token string) {
	authSessions.Lock()
	delete(authSessions.m, token)
	authSessions.Unlock()
}

// ── Brute-force throttle ────────────────────────────────────────────────────

var loginFails = struct {
	sync.Mutex
	m map[string]*failRecord // client IP → recent failures
}{m: map[string]*failRecord{}}

type failRecord struct {
	count int
	first time.Time
}

// clientIP is the throttle key. RemoteAddr is used rather than any forwarded
// header: nothing sits in front of this server, and trusting X-Forwarded-For
// without a proxy to set it would let an attacker rotate the key at will.
func clientIP(r *http.Request) string {
	if host, _, ok := strings.Cut(r.RemoteAddr, ":"); ok {
		return host
	}
	return r.RemoteAddr
}

func loginBlocked(ip string) bool {
	loginFails.Lock()
	defer loginFails.Unlock()
	rec, ok := loginFails.m[ip]
	if !ok {
		return false
	}
	if time.Since(rec.first) > loginFailWindow {
		delete(loginFails.m, ip)
		return false
	}
	return rec.count >= loginMaxFails
}

func noteLoginFail(ip string) {
	loginFails.Lock()
	defer loginFails.Unlock()
	rec, ok := loginFails.m[ip]
	if !ok || time.Since(rec.first) > loginFailWindow {
		loginFails.m[ip] = &failRecord{count: 1, first: time.Now()}
		return
	}
	rec.count++
}

func clearLoginFails(ip string) {
	loginFails.Lock()
	delete(loginFails.m, ip)
	loginFails.Unlock()
}

// ── Middleware ──────────────────────────────────────────────────────────────

// publicPaths are reachable without a session. The auth endpoints themselves
// have to be, or logging in would require being logged in.
func publicPath(p string) bool {
	switch p {
	case "/api/auth/login", "/api/auth/logout", "/api/auth/me":
		return true
	}
	return false
}

// requireAuth guards the API and the WebSockets. Static files are left open on
// purpose: the bundle is only the UI, and it has to load for the login form to
// exist at all. Everything it can *ask for* is behind this.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		guarded := strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ws/")
		if !authEnabled() || !guarded || publicPath(p) {
			next.ServeHTTP(w, r)
			return
		}

		c, err := r.Cookie(sessionCookie)
		if err != nil || !validSession(c.Value) {
			// A WebSocket client never reads a JSON body — it only sees the
			// handshake fail — so the status code has to carry the meaning.
			if strings.HasPrefix(p, "/ws/") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeJSON(w, 401, map[string]string{"detail": "belum login"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true, // JavaScript can never read it, so an XSS cannot steal it
		// Lax, not None: this is what stops another site from making your
		// browser POST /api/devices/x/power/shutdown with your cookie attached.
		SameSite: http.SameSiteLaxMode,
		// Deliberately not Secure. This is served over plain HTTP on a LAN, and
		// a Secure cookie would simply never be sent back. Behind TLS this
		// should be flipped on.
		Secure: false,
	})
}

// ── HTTP ────────────────────────────────────────────────────────────────────

type loginReq struct {
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

func handleLogin(w http.ResponseWriter, r *http.Request) error {
	if !authEnabled() {
		return errf(409, "DASHBOARD_PASSWORD belum diisi di .env")
	}

	ip := clientIP(r)
	if loginBlocked(ip) {
		return errf(429, "kebanyakan percobaan gagal, tunggu beberapa menit")
	}

	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	// Constant-time, so the comparison cannot be timed to recover the password
	// one character at a time.
	ok := subtle.ConstantTimeCompare([]byte(req.Password), []byte(dashboardPassword)) == 1
	if !ok {
		noteLoginFail(ip)
		// A fixed delay on the failure path only. Slow enough to make guessing
		// tedious, short enough that a typo does not feel like a hang.
		time.Sleep(loginFailDelay)
		return errf(401, "password salah")
	}

	ttl := sessionTTL
	if req.Remember {
		ttl = sessionTTLRemembered
	}
	token, err := newSession(ttl)
	if err != nil {
		return errf(500, "gagal bikin sesi")
	}
	clearLoginFails(ip)
	setSessionCookie(w, token, ttl)
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func handleLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		dropSession(c.Value)
	}
	setSessionCookie(w, "", -1)
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// handleAuthMe is what the frontend asks before rendering anything: whether a
// password is configured at all, and whether this browser already holds a
// session. It answers 200 in every case — a 401 here would be indistinguishable
// from the session having expired mid-poll, which the client handles
// differently.
func handleAuthMe(w http.ResponseWriter, r *http.Request) error {
	authed := false
	if c, err := r.Cookie(sessionCookie); err == nil {
		authed = validSession(c.Value)
	}
	writeJSON(w, 200, map[string]bool{
		"required":      authEnabled(),
		"authenticated": authed || !authEnabled(),
	})
	return nil
}
