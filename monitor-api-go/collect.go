package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Shapes returned to the frontend, identical whether they were collected
// locally with gopsutil, over SSH from a Linux box, or over SSH from a Windows
// box via PowerShell.

type NodeSummary struct {
	Hostname      string    `json:"hostname"`
	CPUPercent    float64   `json:"cpu_percent"`
	CPUCount      int       `json:"cpu_count"`
	LoadAvg       []float64 `json:"load_avg,omitempty"`
	MemTotal      uint64    `json:"mem_total"`
	MemUsed       uint64    `json:"mem_used"`
	MemPercent    float64   `json:"mem_percent"`
	DiskTotal     uint64    `json:"disk_total"`
	DiskUsed      uint64    `json:"disk_used"`
	DiskPercent   float64   `json:"disk_percent"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	// Where the numbers came from: "local", "ssh", or "hypervisor" when the
	// guest could not be logged into and Proxmox's own figures are shown.
	Source string `json:"source"`
}

type ServiceUnit struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	State   string `json:"state"` // running | stopped | failed
	Sub     string `json:"sub,omitempty"`
	Startup string `json:"startup,omitempty"` // enabled | disabled | manual | auto
	// System marks units that ship with the OS. They are returned rather than
	// dropped, so the UI can hide them by default but still offer a way in —
	// a service list that silently omits things is worse than a long one.
	System bool `json:"system"`
}

type ProcRow struct {
	PID      int     `json:"pid"`
	Name     string  `json:"name"`
	CPU      float64 `json:"cpu"`
	MemBytes uint64  `json:"mem_bytes"`
	User     string  `json:"user,omitempty"`
}

// unmarshalList decodes PowerShell's ConvertTo-Json output into a slice.
//
// ConvertTo-Json emits a bare object rather than a one-element array when a
// query matches exactly one item, and Windows PowerShell 5.1 has no -AsArray
// to force it. Trying the slice first and falling back to a single object is
// the only shape-proof way to read it.
func unmarshalList[T any](raw string, out *[]T) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), out); err == nil {
		return nil
	}
	var single T
	if err := json.Unmarshal([]byte(raw), &single); err != nil {
		return fmt.Errorf("output bukan JSON yang dikenali: %s", firstLine(raw))
	}
	*out = []T{single}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// sections splits marker-delimited command output. One SSH round trip carries
// several commands, each announced by `echo '#name'`.
func sections(out string) map[string]string {
	result := map[string]string{}
	name := ""
	var buf []string
	flush := func() {
		if name != "" {
			result[name] = strings.Join(buf, "\n")
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "#") {
			flush()
			name = strings.TrimSpace(line[1:])
			buf = buf[:0]
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return result
}

// ── Dispatch ────────────────────────────────────────────────────────────────

// collectSummary picks the right collector for a node. A guest with no
// credentials still gets a summary — the hypervisor knows its memory and
// uptime even when nobody can log in.
func collectSummary(n *Node, d *Device) (NodeSummary, error) {
	switch {
	case d == nil:
		return hypervisorSummary(n), nil
	case n.IsLocal:
		return localSummary()
	case strings.EqualFold(d.OS, "windows"):
		return windowsSummary(d)
	default:
		return linuxSummary(d)
	}
}

func collectServices(n *Node, d *Device) ([]ServiceUnit, error) {
	if d == nil {
		return nil, fmt.Errorf("node ini belum punya kredensial SSH")
	}
	if strings.EqualFold(d.OS, "windows") {
		return windowsServices(d)
	}
	return linuxServices(d)
}

func collectProcesses(n *Node, d *Device) ([]ProcRow, error) {
	if d == nil {
		return nil, fmt.Errorf("node ini belum punya kredensial SSH")
	}
	if strings.EqualFold(d.OS, "windows") {
		return windowsProcesses(d)
	}
	return linuxProcesses(d)
}

// hypervisorSummary reports what Proxmox knows about a guest we cannot log
// into. Disk is deliberately left at zero: `maxdisk` is the size of the
// virtual disk, not how full the guest's filesystem is, and showing an
// allocation as if it were usage would be a lie.
func hypervisorSummary(n *Node) NodeSummary {
	s := NodeSummary{
		Hostname:      n.Label,
		CPUCount:      n.GuestCPUs,
		MemTotal:      n.GuestMemMax,
		MemUsed:       n.GuestMemUsed,
		UptimeSeconds: n.GuestUptime,
		Source:        "hypervisor",
	}
	if s.MemTotal > 0 {
		s.MemPercent = round1(float64(s.MemUsed) / float64(s.MemTotal) * 100)
	}
	return s
}
