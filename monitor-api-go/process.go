package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// projectLabel derives a human-friendly project name from a process's working
// dir, so three different Next.js apps show as 'project-cctv' / '9router' /
// 'Pocket-Asik' instead of all collapsing into one 'Next.js' row.
func projectLabel(cwd string) string {
	if cwd == "" {
		return ""
	}
	router := homeDir + "/.9router"
	if cwd == router || strings.HasPrefix(cwd, router+"/") {
		return "9router"
	}
	if _, rest, ok := strings.Cut(cwd, "/projects/"); ok {
		first, _, _ := strings.Cut(rest, "/")
		return first
	}
	return ""
}

// gopsutil reports process state with its own vocabulary ("sleep", "stop"),
// while the frontend's statusMap is keyed on psutil's ("sleeping", "stopped").
// An unmapped value falls through to the raw string in the UI, so translating
// here is what keeps the detail modal showing "Idle/nunggu" instead of "sleep".
var statusNames = map[string]string{
	process.Running: "running",
	process.Sleep:   "sleeping",
	process.Blocked: "disk_sleep", // Linux 'D' — uninterruptible, usually disk I/O
	process.Stop:    "stopped",
	process.Zombie:  "zombie",
	process.Idle:    "idle",
	process.Wait:    "waiting",
	process.Lock:    "locked",
}

func psutilStatus(states []string) string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		if mapped, ok := statusNames[s]; ok {
			out = append(out, mapped)
		} else if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

func procCwd(p *process.Process) string {
	cwd, err := p.Cwd()
	if err != nil {
		return ""
	}
	return cwd
}

type procGroup struct {
	Label      string  `json:"label"`
	Name       string  `json:"name"`
	Project    *string `json:"project"` // null, not "", to match what the UI has always received
	User       string  `json:"user"`
	Count      int     `json:"count"`
	TopPID     int32   `json:"top_pid"`
	CPU        float64 `json:"cpu"`
	MemBytes   uint64  `json:"mem_bytes"`
	MemPercent float64 `json:"mem_percent"`

	topMetric float64 // sort key for choosing which PID represents the group
}

// nullable renders an absent project as JSON null rather than "".
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// handleProcesses returns top processes by CPU or RAM (task-manager style).
//
// Grouped by project (folder they run from) when we can tell, else by process
// name — like Windows Task Manager's per-app grouping. CPU% is normalized to
// the whole machine (divided by core count) so values sum to ~100% across all
// cores; the raw per-process reading is per-core.
func handleProcesses(w http.ResponseWriter, r *http.Request) error {
	sortKey := r.URL.Query().Get("sort")
	if sortKey != "cpu" && sortKey != "ram" {
		sortKey = "cpu"
	}
	limit := 8
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	limit = clampInt(limit, 1, 30)

	ncpu, err := cpu.Counts(true)
	if err != nil || ncpu == 0 {
		ncpu = 1
	}

	procs, err := process.Processes()
	if err != nil {
		return errf(500, "gagal baca daftar proses: "+err.Error())
	}

	// Percent(0) measures usage since the previous call on the same Process
	// value, so prime every process, wait a moment, then read the delta. The
	// slice must be reused — a fresh Processes() call would reset that state.
	for _, p := range procs {
		_, _ = p.Percent(0)
	}
	time.Sleep(400 * time.Millisecond)

	groups := map[string]*procGroup{}
	for _, p := range procs {
		name, err := p.Name()
		if err != nil {
			continue // process exited between the two passes
		}
		rawCPU, err := p.Percent(0)
		if err != nil {
			continue
		}
		cpuPct := rawCPU / float64(ncpu)

		mi, err := p.MemoryInfo()
		if err != nil || mi == nil {
			continue
		}
		memPct32, _ := p.MemoryPercent()
		memPct := float64(memPct32)
		user, _ := p.Username()

		// Only bother resolving cwd for processes worth showing (cheap-ish
		// readlink, but skip the long idle tail).
		project := ""
		if cpuPct >= 0.1 || mi.RSS >= 5*1024*1024 {
			project = projectLabel(procCwd(p))
		}

		// Group key: project if known (keeps distinct apps separate), else name.
		label, gkey := name, "name:"+name
		if project != "" {
			label, gkey = project, "proj:"+project
		}

		metric := cpuPct
		if sortKey == "ram" {
			metric = float64(mi.RSS)
		}

		g := groups[gkey]
		if g == nil {
			groups[gkey] = &procGroup{
				Label: label, Name: name, Project: nullable(project), User: user,
				Count: 1, CPU: cpuPct, MemBytes: mi.RSS, MemPercent: memPct,
				TopPID: p.Pid, topMetric: metric,
			}
			continue
		}
		g.Count++
		g.CPU += cpuPct
		g.MemBytes += mi.RSS
		g.MemPercent += memPct
		if metric > g.topMetric {
			g.topMetric = metric
			g.TopPID = p.Pid
			g.User = user
		}
	}

	rows := make([]procGroup, 0, len(groups))
	for _, g := range groups {
		// Drop kernel threads / truly idle noise (0% CPU and near-0 RAM)
		if g.CPU < 0.1 && g.MemBytes < 5*1024*1024 {
			continue
		}
		g.CPU = round1(g.CPU)
		g.MemPercent = round1(g.MemPercent)
		rows = append(rows, *g)
	}

	// Ties are common — on an idle box most rows sit at exactly 0% CPU — and Go
	// randomizes map iteration, so without an explicit tiebreak the bottom of
	// the list would reshuffle on every poll. That is visible churn in the UI,
	// and it would defeat the frontend's row cache, which is keyed on top_pid.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if sortKey == "cpu" {
			if a.CPU != b.CPU {
				return a.CPU > b.CPU
			}
			if a.MemBytes != b.MemBytes {
				return a.MemBytes > b.MemBytes
			}
		} else {
			if a.MemBytes != b.MemBytes {
				return a.MemBytes > b.MemBytes
			}
			if a.CPU != b.CPU {
				return a.CPU > b.CPU
			}
		}
		return a.Label < b.Label
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}

	writeJSON(w, 200, map[string]any{"sort": sortKey, "ncpu": ncpu, "processes": rows})
	return nil
}

// listeningPorts returns the TCP ports this PID is LISTENing on (best-effort;
// needs no root for the caller's own processes).
func listeningPorts(pid int32) []uint32 {
	conns, err := net.ConnectionsPid("tcp", pid)
	if err != nil {
		return []uint32{}
	}
	seen := map[uint32]bool{}
	ports := []uint32{}
	for _, c := range conns {
		if c.Status == "LISTEN" && c.Laddr.Port != 0 && !seen[c.Laddr.Port] {
			seen[c.Laddr.Port] = true
			ports = append(ports, c.Laddr.Port)
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}

// handleProcessDetail returns full detail for one process — command, working
// dir, ports, uptime — so you can tell what an app actually is (which project
// a Next.js is serving).
func handleProcessDetail(w http.ResponseWriter, r *http.Request) error {
	pid64, err := strconv.ParseInt(r.PathValue("pid"), 10, 32)
	if err != nil {
		return errf(400, "pid tidak valid")
	}
	pid := int32(pid64)

	p, err := process.NewProcess(pid)
	if err != nil {
		return errf(404, "proses sudah tidak ada (mungkin sudah mati)")
	}

	name, err := p.Name()
	if err != nil {
		return errf(403, "tidak punya akses ke detail proses ini")
	}
	cmdline, _ := p.Cmdline()
	if cmdline == "" {
		cmdline = name
	}
	statuses, _ := p.Status()
	status := psutilStatus(statuses)
	createTime, _ := p.CreateTime() // milliseconds since epoch
	numThreads, _ := p.NumThreads()
	username, _ := p.Username()

	var rss uint64
	if mi, err := p.MemoryInfo(); err == nil && mi != nil {
		rss = mi.RSS
	}
	memPct32, _ := p.MemoryPercent()

	ppid, _ := p.Ppid()
	parentName := ""
	if ppid > 0 {
		if parent, err := process.NewProcess(ppid); err == nil {
			parentName, _ = parent.Name()
		}
	}

	cwd := procCwd(p)
	ncpu, err := cpu.Counts(true)
	if err != nil || ncpu == 0 {
		ncpu = 1
	}
	_, _ = p.Percent(0)
	time.Sleep(300 * time.Millisecond)
	cpuPct := 0.0
	if raw, err := p.Percent(0); err == nil {
		cpuPct = round1(raw / float64(ncpu))
	}

	uptime := 0
	if createTime > 0 {
		uptime = int(time.Now().Unix() - createTime/1000)
	}

	writeJSON(w, 200, map[string]any{
		"pid":            pid,
		"name":           name,
		"project":        nullable(projectLabel(cwd)),
		"user":           username,
		"status":         status,
		"cmdline":        cmdline,
		"cwd":            cwd,
		"ports":          listeningPorts(pid),
		"uptime_seconds": uptime,
		"num_threads":    numThreads,
		"cpu":            cpuPct,
		"mem_bytes":      rss,
		"mem_percent":    round1(float64(memPct32)),
		"ppid":           ppid,
		"parent_name":    parentName,
	})
	return nil
}
