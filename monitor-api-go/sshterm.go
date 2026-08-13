package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// Sessions survive the browser tab closing/backgrounding: the SSH channel
// lives here, decoupled from any one websocket, and only gets torn down after
// sshIdleTimeout with nobody attached. Reconnecting a websocket to the same
// device re-attaches to the same shell and replays the scrollback.
const (
	sshIdleTimeout = 6 * time.Hour
	sshBufferMax   = 200_000 // bytes of scrollback kept for replay on reattach
)

// ...but that only covers the *client* going away. Anything running in the
// shell still dies with this process, so restarting monitor-api (deploying a
// fix, the Services tab, a reboot) kills every long-running job on every
// device. Wrapping the shell in tmux moves its lifetime onto the target
// machine instead: we attach on connect and detach on disconnect, so the API
// becomes disposable.
//
// Windows has no tmux, but it does have a shell worth asking for by name:
// OpenSSH there hands out cmd.exe unless HKLM\SOFTWARE\OpenSSH\DefaultShell
// says otherwise, which means no PowerShell — no `irm`, no `iex`. Launching
// powershell.exe ourselves gets that without touching the target's registry,
// and leaves scp/sftp on those boxes alone (setting DefaultShell is known to
// break scp, since PowerShell writes a banner into the transfer stream).
//
// Anything we can't identify falls back to whatever the account's default
// shell is.
var shellKind = struct {
	sync.RWMutex
	m map[string]string // device_id → "tmux" | "powershell" | "plain"
}{m: map[string]string{}}

func getShellKind(deviceID string) (string, bool) {
	shellKind.RLock()
	defer shellKind.RUnlock()
	k, ok := shellKind.m[deviceID]
	return k, ok
}

func setShellKind(deviceID, kind string) {
	shellKind.Lock()
	shellKind.m[deviceID] = kind
	shellKind.Unlock()
}

// tmuxCapable answers "can this device keep sessions alive?". The `unknown`
// argument is what to assume when we have never probed it — listing wants to
// try anyway, the sessions API wants to report honestly.
func tmuxCapable(deviceID string, unknown bool) bool {
	kind, ok := getShellKind(deviceID)
	if !ok {
		return unknown
	}
	return kind == "tmux"
}

// The tmux session list on the target is the authoritative record of which
// terminals exist for a device — it outlives this process and every browser,
// which is what lets you pick a session up from a different phone/laptop. That
// only works if the name round-trips back to a session id, so ids are
// restricted to a charset tmux is happy with (no '.' or ':') and short enough
// that nothing gets truncated. Anything else is rejected rather than mangled.
const tmuxPrefix = "mh-"

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func tmuxSessionName(sessionID string) (string, error) {
	// This gets interpolated into a shell command, so the charset check is
	// load-bearing, not cosmetic — enforce it here so no caller can skip it.
	if !sessionIDRe.MatchString(sessionID) {
		return "", fmt.Errorf("invalid session id: %q", sessionID)
	}
	return tmuxPrefix + sessionID, nil
}

func sessionIDFromTmux(name string) string {
	sid, ok := strings.CutPrefix(name, tmuxPrefix)
	if !ok || !sessionIDRe.MatchString(sid) {
		return ""
	}
	return sid
}

// ── Connecting ──────────────────────────────────────────────────────────────

// sshConnect dials a device using its stored credentials.
//
// The host key is accepted unconditionally, matching the AutoAddPolicy the
// Python version used: these are LAN boxes registered by hand, and there is no
// known-hosts store to check against.
func sshConnect(d *Device) (*ssh.Client, error) {
	if d.Auth == nil {
		return nil, errors.New("device tidak punya kredensial SSH")
	}

	var auths []ssh.AuthMethod
	if d.Auth.Type == "key" {
		pem, err := os.ReadFile(d.Auth.Path)
		if err != nil {
			return nil, fmt.Errorf("gagal baca private key: %w", err)
		}
		var signer ssh.Signer
		if d.Auth.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(d.Auth.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("private key tidak bisa dibaca: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	} else {
		pw := d.Auth.Value
		auths = append(auths, ssh.Password(pw))
		// Some sshd configs advertise only keyboard-interactive for passwords
		// (notably PAM setups), where a plain password auth silently fails.
		auths = append(auths, ssh.KeyboardInteractive(
			func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = pw
				}
				return answers, nil
			}))
	}

	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", d.Host, d.sshPort()), &ssh.ClientConfig{
		User:            d.Username,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
}

// syncBuf is a mutex-guarded sink. The timeout path in sshRun reads the buffer
// while the SSH goroutine may still be writing to it.
type syncBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.b = append(s.b, p...)
	s.mu.Unlock()
	return len(p), nil
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

// sshRun runs a short command, returning (exitCode, stdout). -1 on timeout.
//
// session.Wait() has no deadline of its own and will block for as long as the
// remote keeps the channel open, so this races it against a timer — probing a
// device must never wedge the connect path.
func sshRun(client *ssh.Client, command string, timeout time.Duration) (int, string) {
	session, err := client.NewSession()
	if err != nil {
		return -1, ""
	}
	defer session.Close()

	out := &syncBuf{}
	session.Stdout = out
	session.Stderr = io.Discard

	if err := session.Start(command); err != nil {
		return -1, ""
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			return 0, out.String()
		}
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			return ee.ExitStatus(), out.String()
		}
		return -1, out.String()
	case <-time.After(timeout):
		_ = session.Close()
		return -1, out.String()
	}
}

// probeShellKind picks the best shell we can get on this target. Probed once
// per device.
//
// `where` is a cmd.exe builtin and not a Linux command, so a Linux box with no
// tmux still falls through to "plain" rather than misidentifying.
func probeShellKind(client *ssh.Client) string {
	if code, _ := sshRun(client, "command -v tmux", 8*time.Second); code == 0 {
		return "tmux"
	}
	if code, _ := sshRun(client, "where powershell", 8*time.Second); code == 0 {
		return "powershell"
	}
	return "plain"
}

// ── Session registry ────────────────────────────────────────────────────────

type sessionKey struct {
	deviceID  string
	sessionID string // what lets a device run multiple independent terminal
	// tabs instead of every connection sharing (mirroring) one shell
}

type sshSession struct {
	key     sessionKey
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser

	mu        sync.Mutex
	buffer    []byte
	clients   map[*wsClient]bool
	idleTimer *time.Timer
	closed    bool
}

var sessions = struct {
	sync.Mutex
	m map[sessionKey]*sshSession
}{m: map[sessionKey]*sshSession{}}

// Write receives shell output. It doubles as the ssh.Session's Stdout/Stderr,
// which is why the fan-out to websockets lives here rather than in a reader
// goroutine.
func (s *sshSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buffer = append(s.buffer, p...)
	if len(s.buffer) > sshBufferMax {
		s.buffer = trimToRuneStart(s.buffer[len(s.buffer)-sshBufferMax:])
	}
	targets := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		targets = append(targets, c)
	}
	s.mu.Unlock()

	text := string(p)
	for _, c := range targets {
		if err := c.writeText(text); err != nil {
			s.detach(c)
		}
	}
	return len(p), nil
}

// trimToRuneStart drops any leading continuation bytes left behind by a
// byte-level cut, so replayed scrollback never begins mid-character.
func trimToRuneStart(b []byte) []byte {
	for i := 0; i < len(b) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(b[i]) {
			return b[i:]
		}
	}
	return b
}

func (s *sshSession) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buffer)
}

func (s *sshSession) attach(c *wsClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.clients[c] = true
}

// detach removes a client and, when it was the last one, starts the idle
// countdown rather than tearing down immediately — that is what makes closing
// a tab and reopening it later land back in the same shell.
func (s *sshSession) detach(c *wsClient) {
	s.mu.Lock()
	delete(s.clients, c)
	empty := len(s.clients) == 0
	if empty && !s.closed && s.idleTimer == nil {
		s.idleTimer = time.AfterFunc(sshIdleTimeout, func() { s.idleTeardown() })
	}
	s.mu.Unlock()
}

func (s *sshSession) idleTeardown() {
	s.mu.Lock()
	stillEmpty := len(s.clients) == 0
	s.mu.Unlock()
	if !stillEmpty {
		return
	}
	sessions.Lock()
	if sessions.m[s.key] == s {
		delete(sessions.m, s.key)
	}
	sessions.Unlock()
	s.close()
}

func (s *sshSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.mu.Unlock()

	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.session != nil {
		_ = s.session.Close()
	}
	if s.client != nil {
		_ = s.client.Close()
	}
}

func (s *sshSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// closeDeviceSessions tears down every terminal for a device — used when the
// device is deleted, so nothing keeps talking to a host we no longer know.
func closeDeviceSessions(deviceID string) {
	sessions.Lock()
	var doomed []*sshSession
	for k, s := range sessions.m {
		if k.deviceID == deviceID {
			doomed = append(doomed, s)
			delete(sessions.m, k)
		}
	}
	sessions.Unlock()
	for _, s := range doomed {
		s.close()
	}
}

// openShell connects and opens an interactive shell for one session id.
func openShell(d *Device, deviceID, sessionID string) (*sshSession, error) {
	client, err := sshConnect(d)
	if err != nil {
		return nil, err
	}
	if _, known := getShellKind(deviceID); !known {
		setShellKind(deviceID, probeShellKind(client))
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}

	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		session.Close()
		client.Close()
		return nil, err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}

	s := &sshSession{
		key:     sessionKey{deviceID, sessionID},
		client:  client,
		session: session,
		stdin:   stdin,
		clients: map[*wsClient]bool{},
	}
	// Both streams land in the same buffer: with a PTY the remote usually
	// folds stderr into stdout, but not every server does, and a terminal that
	// silently drops stderr is worse than one that interleaves it.
	session.Stdout = s
	session.Stderr = s

	kind, _ := getShellKind(deviceID)
	switch kind {
	case "tmux":
		// -A attaches to the session if it already exists and creates it
		// otherwise, which is exactly the reconnect semantics we want: same
		// session_id → same shell, whether we're reconnecting after a network
		// blip or after the API process was restarted out from under it.
		name, err := tmuxSessionName(sessionID)
		if err != nil {
			s.close()
			return nil, err
		}
		err = session.Start("tmux new-session -A -s " + name)
		if err != nil {
			s.close()
			return nil, err
		}
	case "powershell":
		if err := session.Start("powershell.exe -NoLogo"); err != nil {
			s.close()
			return nil, err
		}
	default:
		if err := session.Shell(); err != nil {
			s.close()
			return nil, err
		}
	}

	// When the remote shell exits, drop the session from the registry so the
	// next connect starts fresh instead of writing into a dead channel.
	go func() {
		_ = session.Wait()
		sessions.Lock()
		if sessions.m[s.key] == s {
			delete(sessions.m, s.key)
		}
		sessions.Unlock()
		s.close()
	}()

	return s, nil
}

func tmuxKill(d *Device, sessionID string, client *ssh.Client) {
	name, err := tmuxSessionName(sessionID)
	if err != nil {
		return
	}
	ownClient := client == nil
	if ownClient {
		client, err = sshConnect(d)
		if err != nil {
			fmt.Printf("⚠️  tmux kill-session failed for %s/%s: %v\n", d.ID, sessionID, err)
			return
		}
		defer client.Close()
	}
	sshRun(client, "tmux kill-session -t "+name, 8*time.Second)
}

// ── Websocket terminal ──────────────────────────────────────────────────────

type resizeMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func handleSSHWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &wsClient{conn: conn}

	deviceID := r.PathValue("device_id")
	sessionID := r.PathValue("session_id")

	d := devices.get(deviceID)
	if d == nil {
		client.close(4404, "device not found")
		return
	}
	if !sessionIDRe.MatchString(sessionID) {
		_ = client.writeText("\r\n\x1b[31mInvalid session id.\x1b[0m\r\n")
		client.close(4400, "invalid session id")
		return
	}

	key := sessionKey{deviceID, sessionID}

	sessions.Lock()
	s := sessions.m[key]
	if s != nil && s.isClosed() {
		delete(sessions.m, key)
		s = nil
	}
	sessions.Unlock()

	if s == nil {
		s, err = openShell(d, deviceID, sessionID)
		if err != nil {
			_ = client.writeText("\r\n\x1b[31mConnection failed: " + err.Error() + "\x1b[0m\r\n")
			client.close(4500, "connection failed")
			return
		}
		sessions.Lock()
		// A concurrent connect for the same key may have won the race; keep
		// the established one so both tabs share a shell rather than orphaning
		// a channel nobody will ever read.
		if existing := sessions.m[key]; existing != nil && !existing.isClosed() {
			sessions.Unlock()
			s.close()
			s = existing
		} else {
			sessions.m[key] = s
			sessions.Unlock()
		}
	} else if buffered := s.snapshot(); buffered != "" {
		if err := client.writeText(buffered); err != nil {
			return
		}
	}

	s.attach(client)
	defer s.detach(client)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var rm resizeMsg
		if json.Unmarshal(data, &rm) == nil && rm.Type == "resize" && rm.Cols > 0 && rm.Rows > 0 {
			_ = s.session.WindowChange(rm.Rows, rm.Cols)
			continue
		}
		if _, err := s.stdin.Write(data); err != nil {
			return
		}
	}
}

// ── Session listing ─────────────────────────────────────────────────────────

// liveClient returns an already-open SSH connection to this device, if any —
// listing sessions shouldn't pay for a fresh handshake when a terminal is open.
func liveClient(deviceID string) *ssh.Client {
	sessions.Lock()
	defer sessions.Unlock()
	for k, s := range sessions.m {
		if k.deviceID == deviceID && !s.isClosed() {
			return s.client
		}
	}
	return nil
}

const tmuxListCmd = "tmux list-sessions -F " +
	"'#{session_name}\t#{session_created}\t#{session_attached}'"

type sessionInfo struct {
	ID         string `json:"id"`
	CreatedAt  *int64 `json:"created_at"`
	Attached   bool   `json:"attached"`
	Persistent bool   `json:"persistent"`
}

// collectSessions lists the terminals that exist for this device, newest last.
//
// tmux sessions on the target are the durable ones; the in-memory registry is
// folded in too so devices without tmux (Windows) still list whatever is
// currently open instead of nothing.
func collectSessions(d *Device, deviceID string) []sessionInfo {
	found := map[string]sessionInfo{}

	if tmuxCapable(deviceID, true) {
		client := liveClient(deviceID)
		ownClient := client == nil
		var err error
		if ownClient {
			client, err = sshConnect(d)
		}
		if err != nil {
			fmt.Printf("⚠️  tmux list-sessions failed for %s: %v\n", deviceID, err)
		} else {
			rc, out := sshRun(client, tmuxListCmd, 8*time.Second)
			// rc != 0 is the normal "no server running" case, not an error.
			if rc == 0 {
				setShellKind(deviceID, "tmux")
				for _, line := range strings.Split(out, "\n") {
					parts := strings.Split(strings.TrimSpace(line), "\t")
					if len(parts) != 3 {
						continue
					}
					sid := sessionIDFromTmux(parts[0])
					if sid == "" {
						continue // someone else's tmux session, not ours
					}
					info := sessionInfo{ID: sid, Attached: parts[2] == "1", Persistent: true}
					if ts, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
						info.CreatedAt = &ts
					}
					found[sid] = info
				}
			}
			if ownClient {
				client.Close()
			}
		}
	}

	sessions.Lock()
	for k, s := range sessions.m {
		if k.deviceID != deviceID || s.isClosed() {
			continue
		}
		if _, ok := found[k.sessionID]; !ok {
			found[k.sessionID] = sessionInfo{ID: k.sessionID, Attached: true, Persistent: false}
		}
	}
	sessions.Unlock()

	out := make([]sessionInfo, 0, len(found))
	for _, info := range found {
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		var a, b int64
		if out[i].CreatedAt != nil {
			a = *out[i].CreatedAt
		}
		if out[j].CreatedAt != nil {
			b = *out[j].CreatedAt
		}
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func handleListSessions(w http.ResponseWriter, r *http.Request) error {
	deviceID := r.PathValue("device_id")
	d := devices.get(deviceID)
	if d == nil {
		return errf(404, "device not found")
	}
	list := collectSessions(d, deviceID)
	writeJSON(w, 200, map[string]any{
		"persistent": tmuxCapable(deviceID, false),
		"sessions":   list,
	})
	return nil
}

// handleKillSession explicitly ends a terminal session (the tab's ✕ button).
//
// Detaching is the default, so without this every closed tab would leave a
// detached tmux session running on the target forever.
func handleKillSession(w http.ResponseWriter, r *http.Request) error {
	deviceID := r.PathValue("device_id")
	sessionID := r.PathValue("session_id")

	d := devices.get(deviceID)
	if d == nil {
		return errf(404, "device not found")
	}
	if !sessionIDRe.MatchString(sessionID) {
		return errf(400, "invalid session id")
	}

	key := sessionKey{deviceID, sessionID}
	sessions.Lock()
	s := sessions.m[key]
	delete(sessions.m, key)
	sessions.Unlock()

	// Unknown support (nothing connected since the last restart) still gets an
	// attempt — the probe result is cached, so a non-tmux device only pays for
	// the extra connection once.
	if tmuxCapable(deviceID, true) {
		var reuse *ssh.Client
		if s != nil && !s.isClosed() {
			reuse = s.client
		}
		tmuxKill(d, sessionID, reuse)
	}

	if s != nil {
		// Boot any other device still attached to this session, so it sees a
		// clean close instead of a channel that silently stopped working.
		s.mu.Lock()
		attached := make([]*wsClient, 0, len(s.clients))
		for c := range s.clients {
			attached = append(attached, c)
		}
		s.mu.Unlock()
		for _, c := range attached {
			c.close(4410, "session killed")
		}
		s.close()
	}

	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
