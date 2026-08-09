package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// HTTP health checks for the apps running on these machines.
//
// The Services view answers "is the process alive". That is not the same
// question as "does the app work": a unit sits happily in `active` while its
// port refuses connections, its database is down, or it answers 502. This
// closes that gap by actually asking the app.
//
// Checks run FROM this host, not from the node they are tagged with. That is
// deliberate — it measures what a client sees, which is the thing that matters,
// and it needs no curl or agent on the target. Tagging a check with a node is
// organisational: it says which machine to go and look at when it breaks.

const (
	checkInterval   = 30 * time.Second
	checkMaxHistory = 60 // ~30 min of 30s samples, enough for a status strip
	checkMaxWorkers = 8
)

type Check struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id,omitempty"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	// ExpectStatus of 0 accepts any 2xx or 3xx.
	ExpectStatus int  `json:"expect_status,omitempty"`
	TimeoutMS    int  `json:"timeout_ms,omitempty"`
	InsecureTLS  bool `json:"insecure_tls,omitempty"`
	Enabled      bool `json:"enabled"`
}

type CheckResult struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
}

// CheckState is a check plus what has happened to it lately.
type CheckState struct {
	Check
	Last *CheckResult `json:"last,omitempty"`
	// History is oldest-first and lives only in memory: a restart loses it.
	// Persisting it would mean a time-series store, which this does not have
	// yet — and a strip of the last half hour is what the view actually uses.
	History []CheckResult `json:"history"`
	// ChangedAt is when OK last flipped, i.e. how long it has been in this state.
	ChangedAt  *time.Time `json:"changed_at,omitempty"`
	FailStreak int        `json:"fail_streak"`
}

// ── Registry ────────────────────────────────────────────────────────────────

type checkRegistry struct {
	mu      sync.RWMutex
	list    []*Check
	history map[string][]CheckResult
	changed map[string]time.Time
	streak  map[string]int
}

var checks = &checkRegistry{
	history: map[string][]CheckResult{},
	changed: map[string]time.Time{},
	streak:  map[string]int{},
}

func checksFile() string { return filepath.Join(baseDir, "checks.json") }

func (r *checkRegistry) load() {
	data, err := os.ReadFile(checksFile())
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️  Gagal baca checks.json: %v\n", err)
		}
		return
	}
	var list []*Check
	if err := json.Unmarshal(data, &list); err != nil {
		fmt.Printf("⚠️  checks.json rusak: %v\n", err)
		return
	}
	r.mu.Lock()
	r.list = list
	r.mu.Unlock()
}

func (r *checkRegistry) save() error {
	r.mu.RLock()
	data, err := json.MarshalIndent(r.list, "", "  ")
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := checksFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, checksFile())
}

func (r *checkRegistry) all() []*Check {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Check, len(r.list))
	copy(out, r.list)
	return out
}

func (r *checkRegistry) get(id string) *Check {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.list {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (r *checkRegistry) uniqueID(base string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	taken := map[string]bool{}
	for _, c := range r.list {
		taken[c.ID] = true
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

// update replaces a check wholesale under the write lock.
//
// The poller holds the same *Check pointers that all() handed out, so editing
// the pointed-to struct from an HTTP handler is a genuine data race on every
// field — proven with -race, not assumed. Swapping the pointer means a poll in
// flight keeps reading a consistent older value instead of a half-written one.
func (r *checkRegistry) update(id string, apply func(Check) Check) *Check {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.list {
		if c.ID == id {
			next := apply(*c)
			next.ID = id // the id is the identity; never let an edit move it
			r.list[i] = &next
			return &next
		}
	}
	return nil
}

func (r *checkRegistry) add(c *Check) {
	r.mu.Lock()
	r.list = append(r.list, c)
	r.mu.Unlock()
}

func (r *checkRegistry) remove(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.list {
		if c.ID == id {
			r.list = append(r.list[:i], r.list[i+1:]...)
			delete(r.history, id)
			delete(r.changed, id)
			delete(r.streak, id)
			return true
		}
	}
	return false
}

// record appends a result and maintains the derived state.
func (r *checkRegistry) record(id string, res CheckResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	hist := r.history[id]
	if len(hist) > 0 && hist[len(hist)-1].OK != res.OK {
		r.changed[id] = res.At
	} else if len(hist) == 0 {
		r.changed[id] = res.At
	}

	hist = append(hist, res)
	if len(hist) > checkMaxHistory {
		hist = hist[len(hist)-checkMaxHistory:]
	}
	r.history[id] = hist

	if res.OK {
		r.streak[id] = 0
	} else {
		r.streak[id]++
	}
}

func (r *checkRegistry) state(c *Check) CheckState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st := CheckState{Check: *c, FailStreak: r.streak[c.ID]}
	hist := r.history[c.ID]
	if len(hist) > 0 {
		st.History = append([]CheckResult(nil), hist...)
		last := hist[len(hist)-1]
		st.Last = &last
	} else {
		st.History = []CheckResult{}
	}
	if at, ok := r.changed[c.ID]; ok {
		st.ChangedAt = &at
	}
	return st
}

// ── Running a check ─────────────────────────────────────────────────────────

// Two clients so the common case keeps certificate verification. Skipping it is
// opt-in per check, for things like Proxmox's own self-signed UI.
var (
	checkClient = &http.Client{
		// Redirects are followed: an app answering 301 → 200 is up.
		Timeout: 20 * time.Second,
	}
	checkClientInsecure = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
)

func runCheck(c *Check) CheckResult {
	timeout := time.Duration(c.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 20*time.Second {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	res := CheckResult{At: time.Now()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		res.Error = "URL tidak valid"
		return res
	}
	req.Header.Set("User-Agent", "orbit-healthcheck/1")

	client := checkClient
	if c.InsecureTLS {
		client = checkClientInsecure
	}

	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = friendlyCheckError(err)
		return res
	}
	defer resp.Body.Close()
	// The body is irrelevant, but it has to be drained for the connection to
	// be reusable rather than torn down on every poll.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	res.Status = resp.StatusCode
	if c.ExpectStatus > 0 {
		res.OK = resp.StatusCode == c.ExpectStatus
		if !res.OK {
			res.Error = fmt.Sprintf("status %d, harusnya %d", resp.StatusCode, c.ExpectStatus)
		}
	} else {
		res.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
		if !res.OK {
			res.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
	}
	return res
}

// friendlyCheckError turns Go's transport errors into something readable in a
// dashboard row. The full error is useless noise for the common cases.
func friendlyCheckError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "context deadline exceeded"), strings.Contains(s, "Client.Timeout"):
		return "timeout"
	case strings.Contains(s, "connection refused"):
		return "connection refused — port nggak ada yang dengerin"
	case strings.Contains(s, "no such host"):
		return "host nggak ketemu"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "network is unreachable"):
		return "nggak ada rute ke host"
	case strings.Contains(s, "certificate"):
		return "sertifikat TLS ditolak — centang 'abaikan TLS' kalau self-signed"
	}
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

// pollChecks runs every enabled check on a fixed interval.
//
// A background loop rather than on-request: the latency figure would otherwise
// include whatever the dashboard was doing at the time, and a fail streak only
// means something if the spacing between samples is regular.
func pollChecks() {
	run := func() {
		list := checks.all()
		sem := make(chan struct{}, checkMaxWorkers)
		var wg sync.WaitGroup
		for _, c := range list {
			if !c.Enabled {
				continue
			}
			wg.Add(1)
			go func(c *Check) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				checks.record(c.ID, runCheck(c))
			}(c)
		}
		wg.Wait()

		// Decide and send after the whole cycle, not per check: a burst of
		// failures can then be recognised as one event rather than twenty.
		notifyTransitions(evaluateTransitions(checkStates("")))
	}

	run() // don't make the first dashboard load wait a full interval
	for range time.Tick(checkInterval) {
		run()
	}
}

// ── Validation ──────────────────────────────────────────────────────────────

// validCheckURL keeps the fetch to http(s).
//
// No attempt is made to block private addresses: reaching internal services is
// the entire point of this feature, so an SSRF guard would defeat it. What this
// does prevent is handing the client a file:// or gopher:// scheme.
func validCheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errf(400, "URL tidak valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errf(400, "URL harus diawali http:// atau https://")
	}
	if u.Host == "" {
		return errf(400, "URL harus punya host")
	}
	return nil
}

// ── Handlers ────────────────────────────────────────────────────────────────

func checkStates(nodeID string) []CheckState {
	out := []CheckState{}
	for _, c := range checks.all() {
		if nodeID != "" && c.NodeID != nodeID {
			continue
		}
		out = append(out, checks.state(c))
	}
	// Failing first — they are why anyone opens this view — then by name.
	sort.SliceStable(out, func(i, j int) bool {
		fi := out[i].Last != nil && !out[i].Last.OK
		fj := out[j].Last != nil && !out[j].Last.OK
		if fi != fj {
			return fi
		}
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}

func handleListChecks(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, map[string]any{"checks": checkStates("")})
	return nil
}

func handleNodeChecks(w http.ResponseWriter, r *http.Request) error {
	node, _, err := resolveNode(r, false)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"checks": checkStates(node.ID)})
	return nil
}

type checkPayload struct {
	NodeID       string `json:"node_id"`
	Label        string `json:"label"`
	URL          string `json:"url"`
	ExpectStatus int    `json:"expect_status"`
	TimeoutMS    int    `json:"timeout_ms"`
	InsecureTLS  bool   `json:"insecure_tls"`
	Enabled      *bool  `json:"enabled"`
}

func handleCreateCheck(w http.ResponseWriter, r *http.Request) error {
	var p checkPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return errf(400, "body tidak valid")
	}
	p.Label = strings.TrimSpace(p.Label)
	p.URL = strings.TrimSpace(p.URL)
	if err := validLabel(p.Label); err != nil {
		return err
	}
	if err := validCheckURL(p.URL); err != nil {
		return err
	}

	c := &Check{
		ID:           checks.uniqueID(slugify(p.Label)),
		NodeID:       p.NodeID,
		Label:        p.Label,
		URL:          p.URL,
		ExpectStatus: p.ExpectStatus,
		TimeoutMS:    p.TimeoutMS,
		InsecureTLS:  p.InsecureTLS,
		Enabled:      true,
	}
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	checks.add(c)
	if err := checks.save(); err != nil {
		checks.remove(c.ID)
		return errf(500, "gagal simpan checks.json: "+err.Error())
	}
	// Answer with a real result rather than an empty row nobody can read yet.
	if c.Enabled {
		checks.record(c.ID, runCheck(c))
	}
	writeJSON(w, 200, checks.state(c))
	return nil
}

func handleUpdateCheck(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("check_id")
	if checks.get(id) == nil {
		return errf(404, "check tidak ditemukan")
	}
	var p checkPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return errf(400, "body tidak valid")
	}
	p.Label = strings.TrimSpace(p.Label)
	p.URL = strings.TrimSpace(p.URL)
	if p.Label != "" {
		if err := validLabel(p.Label); err != nil {
			return err
		}
	}
	if p.URL != "" {
		if err := validCheckURL(p.URL); err != nil {
			return err
		}
	}

	c := checks.update(id, func(next Check) Check {
		if p.Label != "" {
			next.Label = p.Label
		}
		if p.URL != "" {
			next.URL = p.URL
		}
		if p.NodeID != "" {
			next.NodeID = p.NodeID
		}
		next.ExpectStatus = p.ExpectStatus
		next.TimeoutMS = p.TimeoutMS
		next.InsecureTLS = p.InsecureTLS
		if p.Enabled != nil {
			next.Enabled = *p.Enabled
		}
		return next
	})
	if c == nil {
		return errf(404, "check tidak ditemukan")
	}
	if err := checks.save(); err != nil {
		return errf(500, "gagal simpan checks.json: "+err.Error())
	}
	if c.Enabled {
		checks.record(c.ID, runCheck(c))
	}
	writeJSON(w, 200, checks.state(c))
	return nil
}

func handleDeleteCheck(w http.ResponseWriter, r *http.Request) error {
	if !checks.remove(r.PathValue("check_id")) {
		return errf(404, "check tidak ditemukan")
	}
	if err := checks.save(); err != nil {
		return errf(500, "gagal simpan checks.json: "+err.Error())
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// handleRunCheck fires one check immediately, for the "cek sekarang" button.
func handleRunCheck(w http.ResponseWriter, r *http.Request) error {
	c := checks.get(r.PathValue("check_id"))
	if c == nil {
		return errf(404, "check tidak ditemukan")
	}
	checks.record(c.ID, runCheck(c))
	writeJSON(w, 200, checks.state(c))
	return nil
}

// ── Port discovery ──────────────────────────────────────────────────────────

// ListeningPort is something on a node that a check could point at.
type ListeningPort struct {
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
	// Local is true when it only listens on loopback, which means a check from
	// this host can reach it only if the node IS this host.
	Local bool `json:"local"`
}

// Ports that are never a web app; offering them would just be noise.
var boringPorts = map[int]bool{
	22: true, 53: true, 111: true, 123: true, 137: true, 138: true,
	139: true, 445: true, 631: true, 5037: true, 5353: true,
}

const linuxPortsScript = `ss -lptnH 2>/dev/null || ss -lptn 2>/dev/null | tail -n +2`

var (
	portLineRe = regexp.MustCompile(`\s(\S+):(\d+)\s`)
	portProcRe = regexp.MustCompile(`users:\(\("([^"]+)"`)
)

// collectPorts reads listening TCP sockets off a node.
//
// Purely a convenience for the "add check" form: typing eight URLs by hand for
// eight apps is the kind of chore that stops people setting checks up at all.
func collectPorts(n *Node, d *Device) ([]ListeningPort, error) {
	if d == nil {
		return nil, fmt.Errorf("node ini belum punya kredensial SSH")
	}
	if strings.EqualFold(d.OS, "windows") {
		return windowsPorts(d)
	}
	_, out, err := runRemote(d, linuxPortsScript, remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	return parseSSOutput(out), nil
}

func parseSSOutput(out string) []ListeningPort {
	seen := map[int]*ListeningPort{}
	for _, line := range strings.Split(out, "\n") {
		m := portLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		port := int(parseUint(m[2]))
		if port == 0 || boringPorts[port] {
			continue
		}
		addr := m[1]
		lp := seen[port]
		if lp == nil {
			lp = &ListeningPort{Port: port, Local: true}
			seen[port] = lp
		}
		// A socket on 0.0.0.0/[::] is reachable from off-box; loopback is not.
		if addr != "127.0.0.1" && addr != "[::1]" && addr != "::1" {
			lp.Local = false
		}
		if p := portProcRe.FindStringSubmatch(line); p != nil && lp.Process == "" {
			lp.Process = p[1]
		}
	}
	out2 := make([]ListeningPort, 0, len(seen))
	for _, lp := range seen {
		out2 = append(out2, *lp)
	}
	sort.Slice(out2, func(i, j int) bool { return out2[i].Port < out2[j].Port })
	return out2
}

const windowsPortsScript = `Get-NetTCPConnection -State Listen | ` +
	`Select-Object LocalAddress,LocalPort,OwningProcess | ConvertTo-Json -Compress`

type winPortRaw struct {
	LocalAddress  string `json:"LocalAddress"`
	LocalPort     int    `json:"LocalPort"`
	OwningProcess int    `json:"OwningProcess"`
}

func windowsPorts(d *Device) ([]ListeningPort, error) {
	_, out, err := runRemote(d, psCommand(windowsPortsScript), remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	var raw []winPortRaw
	if err := unmarshalList(strings.TrimSpace(out), &raw); err != nil {
		return nil, err
	}
	seen := map[int]*ListeningPort{}
	for _, r := range raw {
		if r.LocalPort == 0 || boringPorts[r.LocalPort] {
			continue
		}
		lp := seen[r.LocalPort]
		if lp == nil {
			lp = &ListeningPort{Port: r.LocalPort, Local: true}
			seen[r.LocalPort] = lp
		}
		if r.LocalAddress != "127.0.0.1" && r.LocalAddress != "::1" {
			lp.Local = false
		}
	}
	list := make([]ListeningPort, 0, len(seen))
	for _, lp := range seen {
		list = append(list, *lp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })
	return list, nil
}

func handleNodePorts(w http.ResponseWriter, r *http.Request) error {
	node, dev, err := resolveNode(r, true)
	if err != nil {
		return err
	}
	ports, cacheErr := cached(cacheKey(node.ID, "ports"), 60*time.Second,
		func() ([]ListeningPort, error) { return collectPorts(node, dev) })

	// The host to build a suggested URL from: loopback for this machine,
	// otherwise the address the node is reached at.
	host := node.Host
	if node.IsLocal || host == "" {
		host = "127.0.0.1"
	}
	writeJSON(w, 200, map[string]any{
		"ports": ports, "host": host, "is_local": node.IsLocal, "error": cacheErr,
	})
	return nil
}
