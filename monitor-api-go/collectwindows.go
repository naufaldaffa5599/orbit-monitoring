package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// NOTE: this file is deliberately NOT named collect_windows.go. Go treats a
// trailing _linux/_windows in a filename as an implicit build constraint, so
// that name would compile the file only when building FOR that OS. The OS
// that matters here is the target being monitored, not the one we run on.
// Windows collection over SSH, through PowerShell.
//
// Every query ends in ConvertTo-Json rather than formatted text: Get-Process
// output columns shift with window width and locale, JSON does not. Scripts use
// single quotes throughout because the whole line is handed to cmd.exe wrapped
// in double quotes (see psCommand).

const windowsSummaryScript = `$os=Get-CimInstance Win32_OperatingSystem; ` +
	`$cpu=@(Get-CimInstance Win32_Processor)[0]; ` +
	`$d=@(Get-CimInstance Win32_LogicalDisk -Filter 'DeviceID=''C:''')[0]; ` +
	`[pscustomobject]@{host=$os.CSName;memTotalKB=[uint64]$os.TotalVisibleMemorySize;` +
	`memFreeKB=[uint64]$os.FreePhysicalMemory;uptime=[int64]((Get-Date)-$os.LastBootUpTime).TotalSeconds;` +
	`cpuLoad=[int]$cpu.LoadPercentage;cpuCount=[int]$cpu.NumberOfLogicalProcessors;` +
	`diskSize=[uint64]$d.Size;diskFree=[uint64]$d.FreeSpace} | ConvertTo-Json -Compress`

type winSummaryRaw struct {
	Host       string `json:"host"`
	MemTotalKB uint64 `json:"memTotalKB"`
	MemFreeKB  uint64 `json:"memFreeKB"`
	Uptime     int64  `json:"uptime"`
	CPULoad    int    `json:"cpuLoad"`
	CPUCount   int    `json:"cpuCount"`
	DiskSize   uint64 `json:"diskSize"`
	DiskFree   uint64 `json:"diskFree"`
}

func windowsSummary(d *Device) (NodeSummary, error) {
	_, out, err := runRemote(d, psCommand(windowsSummaryScript), remoteCmdTimeout)
	if err != nil {
		return NodeSummary{}, err
	}
	var raw winSummaryRaw
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return NodeSummary{}, fmt.Errorf("output PowerShell tak terbaca: %s", firstLine(out))
	}

	s := NodeSummary{
		Source:        "ssh",
		Hostname:      raw.Host,
		CPUPercent:    float64(raw.CPULoad),
		CPUCount:      raw.CPUCount,
		UptimeSeconds: raw.Uptime,
		MemTotal:      raw.MemTotalKB * 1024,
		DiskTotal:     raw.DiskSize,
	}
	if raw.MemTotalKB > 0 {
		s.MemUsed = (raw.MemTotalKB - raw.MemFreeKB) * 1024
		s.MemPercent = round1(float64(s.MemUsed) / float64(s.MemTotal) * 100)
	}
	if raw.DiskSize > 0 {
		s.DiskUsed = raw.DiskSize - raw.DiskFree
		s.DiskPercent = round1(float64(s.DiskUsed) / float64(s.DiskTotal) * 100)
	}
	return s, nil
}

// ── Services ────────────────────────────────────────────────────────────────

// Win32_Service rather than Get-Service: it carries PathName, which is the only
// dependable way to tell a service that came with Windows from one that was
// installed afterwards.
const windowsServicesScript = `Get-CimInstance Win32_Service | ` +
	`Select-Object Name,DisplayName,State,StartMode,PathName | ConvertTo-Json -Compress`

type winServiceRaw struct {
	Name        string `json:"Name"`
	DisplayName string `json:"DisplayName"`
	State       string `json:"State"`
	StartMode   string `json:"StartMode"`
	PathName    string `json:"PathName"`
}

func windowsServices(d *Device) ([]ServiceUnit, error) {
	_, out, err := runRemote(d, psCommand(windowsServicesScript), remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	var raw []winServiceRaw
	if err := unmarshalList(strings.TrimSpace(out), &raw); err != nil {
		return nil, err
	}

	units := make([]ServiceUnit, 0, len(raw))
	for _, r := range raw {
		state := "stopped"
		switch strings.ToLower(r.State) {
		case "running":
			state = "running"
		case "start pending", "stop pending":
			state = "activating"
		}
		units = append(units, ServiceUnit{
			Name:    r.Name,
			Display: r.DisplayName,
			State:   state,
			Startup: strings.ToLower(r.StartMode),
			System:  isWindowsSystemService(r.PathName),
		})
	}
	return units, nil
}

// isWindowsSystemService flags anything running out of the Windows directory.
// Not perfect — a few vendor drivers install there too — but it cuts a 250-line
// list down to the handful of services someone actually chose to install.
func isWindowsSystemService(pathName string) bool {
	p := strings.ToLower(pathName)
	p = strings.TrimPrefix(p, `"`)
	return strings.Contains(p, `\windows\system32\`) ||
		strings.Contains(p, `\windows\syswow64\`) ||
		strings.Contains(p, `\windows\servicing\`) ||
		strings.Contains(p, `\windows\microsoft.net\`)
}

// ── Processes ───────────────────────────────────────────────────────────────

// Get-Process reports CPU as total seconds consumed since the process started,
// so a percentage only exists as a difference between two samples. Task
// Manager does the same thing; it just takes its samples a second apart.
const windowsProcessScript = `Get-Process | Select-Object Id,ProcessName,WS,CPU | ConvertTo-Json -Compress`

type winProcRaw struct {
	ID          int      `json:"Id"`
	ProcessName string   `json:"ProcessName"`
	WS          uint64   `json:"WS"`
	CPU         *float64 `json:"CPU"` // null for processes we may not query
}

// Previous CPU-seconds per node, so the next poll can turn them into a rate.
var winCPUPrev = struct {
	sync.Mutex
	m  map[string]map[int]float64
	at map[string]time.Time
}{m: map[string]map[int]float64{}, at: map[string]time.Time{}}

func windowsProcesses(d *Device) ([]ProcRow, error) {
	_, out, err := runRemote(d, psCommand(windowsProcessScript), remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	var raw []winProcRaw
	if err := unmarshalList(strings.TrimSpace(out), &raw); err != nil {
		return nil, err
	}

	now := time.Now()
	winCPUPrev.Lock()
	prev := winCPUPrev.m[d.ID]
	elapsed := now.Sub(winCPUPrev.at[d.ID]).Seconds()
	current := make(map[int]float64, len(raw))
	winCPUPrev.Unlock()

	// Without a previous sample there is no rate to compute yet; the first view
	// of a machine shows 0% and the next poll fills it in.
	usable := prev != nil && elapsed > 0.5 && elapsed < 600

	rows := make([]ProcRow, 0, len(raw))
	for _, r := range raw {
		var cpuSec float64
		if r.CPU != nil {
			cpuSec = *r.CPU
		}
		current[r.ID] = cpuSec

		pct := 0.0
		if usable {
			if before, ok := prev[r.ID]; ok && cpuSec >= before {
				pct = round1((cpuSec - before) / elapsed * 100)
			}
		}
		rows = append(rows, ProcRow{
			PID:      r.ID,
			Name:     r.ProcessName,
			CPU:      pct,
			MemBytes: r.WS,
		})
	}

	winCPUPrev.Lock()
	winCPUPrev.m[d.ID] = current
	winCPUPrev.at[d.ID] = now
	winCPUPrev.Unlock()

	return rows, nil
}

// ── Control ─────────────────────────────────────────────────────────────────

func windowsServiceAction(d *Device, name, action string) error {
	verb := map[string]string{
		"start": "Start-Service", "stop": "Stop-Service", "restart": "Restart-Service",
	}[action]
	if verb == "" {
		return fmt.Errorf("aksi tidak dikenal: %s", action)
	}
	// -ErrorAction Stop turns PowerShell's non-terminating errors into a
	// non-zero exit, which is the only signal that reaches us over SSH.
	script := fmt.Sprintf(`%s -Name '%s' -ErrorAction Stop`, verb, escapePS(name))
	rc, out, err := runRemote(d, psCommand(script), 30*time.Second)
	if err != nil {
		return err
	}
	if rc != 0 {
		if strings.Contains(out, "PermissionDenied") || strings.Contains(out, "Access is denied") {
			return fmt.Errorf("akses ditolak — user SSH di %s perlu hak admin", d.Label)
		}
		return fmt.Errorf("%s", firstLine(strings.TrimSpace(out)))
	}
	return nil
}

func windowsKillProcess(d *Device, pid int) error {
	script := fmt.Sprintf(`Stop-Process -Id %d -Force -ErrorAction Stop`, pid)
	rc, out, err := runRemote(d, psCommand(script), 30*time.Second)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("%s", firstLine(strings.TrimSpace(out)))
	}
	return nil
}

// escapePS doubles single quotes, PowerShell's escape inside a single-quoted
// string. Service names come from the target itself, but they still pass
// through a request parameter on the way back.
func escapePS(s string) string { return strings.ReplaceAll(s, "'", "''") }
