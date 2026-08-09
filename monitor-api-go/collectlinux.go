package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// NOTE: this file is deliberately NOT named collect_linux.go. Go treats a
// trailing _linux/_windows in a filename as an implicit build constraint, so
// that name would compile the file only when building FOR that OS. The OS
// that matters here is the target being monitored, not the one we run on.
// Linux collection: plain shell, no agent, one SSH round trip per view.

// The CPU figure needs two /proc/stat samples. They are taken a second apart
// inside the same command rather than across polls, so the number is correct
// even after the dashboard has been closed for an hour.
const linuxSummaryScript = `echo '#s1'; head -1 /proc/stat; sleep 1; echo '#s2'; head -1 /proc/stat
echo '#mem'; grep -E '^(MemTotal|MemAvailable):' /proc/meminfo
echo '#disk'; df -B1 -P / | tail -1
echo '#up'; cut -d' ' -f1 /proc/uptime
echo '#nproc'; nproc
echo '#load'; cut -d' ' -f1-3 /proc/loadavg
echo '#host'; hostname`

// parseStatLine turns a "cpu  61277 347 16950 2137919 …" line into the totals
// the percentage is derived from.
func parseStatLine(line string) (idle, total uint64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
		return 0, 0, false
	}
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += v
		// Fields are user, nice, system, idle, iowait, … — idle and iowait are
		// both time the CPU had nothing to do.
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return idle, total, total > 0
}

func cpuPercentFromSamples(s1, s2 string) float64 {
	idle1, total1, ok1 := parseStatLine(strings.TrimSpace(s1))
	idle2, total2, ok2 := parseStatLine(strings.TrimSpace(s2))
	if !ok1 || !ok2 || total2 <= total1 {
		return 0
	}
	dIdle := float64(idle2 - idle1)
	dTotal := float64(total2 - total1)
	return round1((1 - dIdle/dTotal) * 100)
}

func linuxSummary(d *Device) (NodeSummary, error) {
	_, out, err := runRemote(d, linuxSummaryScript, remoteCmdTimeout)
	if err != nil {
		return NodeSummary{}, err
	}
	sec := sections(out)
	s := NodeSummary{Source: "ssh", Hostname: strings.TrimSpace(sec["host"])}

	s.CPUPercent = cpuPercentFromSamples(sec["s1"], sec["s2"])
	s.CPUCount, _ = strconv.Atoi(strings.TrimSpace(sec["nproc"]))

	for _, f := range strings.Fields(strings.TrimSpace(sec["load"])) {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			s.LoadAvg = append(s.LoadAvg, v)
		}
	}

	// MemAvailable, not MemFree: free memory on Linux is mostly cache, and
	// reporting it as "used" makes every healthy box look full.
	var total, available uint64
	for _, line := range strings.Split(sec["mem"], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kb := parseUint(strings.TrimSuffix(strings.TrimSpace(value), " kB"))
		switch key {
		case "MemTotal":
			total = kb * 1024
		case "MemAvailable":
			available = kb * 1024
		}
	}
	if total > 0 {
		s.MemTotal = total
		s.MemUsed = total - available
		s.MemPercent = round1(float64(s.MemUsed) / float64(total) * 100)
	}

	if fields := strings.Fields(strings.TrimSpace(sec["disk"])); len(fields) >= 3 {
		s.DiskTotal = parseUint(fields[1])
		s.DiskUsed = parseUint(fields[2])
		if s.DiskTotal > 0 {
			s.DiskPercent = round1(float64(s.DiskUsed) / float64(s.DiskTotal) * 100)
		}
	}

	if up, err := strconv.ParseFloat(strings.TrimSpace(sec["up"]), 64); err == nil {
		s.UptimeSeconds = int64(up)
	}
	return s, nil
}

// localSummary is the same shape gathered with gopsutil, for the host actually
// running this API. Going out over SSH to 127.0.0.1 would work but costs a
// handshake and depends on sshd being up to monitor the machine sshd runs on.
func localSummary() (NodeSummary, error) {
	s := NodeSummary{Source: "local"}
	s.Hostname, _ = os.Hostname()

	if pcts, err := cpu.Percent(500e6, false); err == nil && len(pcts) > 0 {
		s.CPUPercent = round1(pcts[0])
	}
	if n, err := cpu.Counts(true); err == nil {
		s.CPUCount = n
	}
	if l, err := load.Avg(); err == nil {
		s.LoadAvg = []float64{l.Load1, l.Load5, l.Load15}
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		used, pct := memUsed(vm)
		s.MemTotal, s.MemUsed, s.MemPercent = vm.Total, used, round1(pct)
	}
	if du, err := disk.Usage("/"); err == nil {
		s.DiskTotal, s.DiskUsed = du.Total, du.Used
		s.DiskPercent = round1(du.UsedPercent)
	}
	if bt, err := host.BootTime(); err == nil {
		s.UptimeSeconds = time.Now().Unix() - int64(bt)
	}
	return s, nil
}

// ── Services ────────────────────────────────────────────────────────────────

const linuxServicesScript = `echo '#units'; systemctl list-units --type=service --all --no-pager --plain --no-legend
echo '#files'; systemctl list-unit-files --type=service --no-pager --plain --no-legend`

// systemUnitPrefixes and systemUnitNames mark the units that ship with a
// distribution. They are still returned, only flagged, so the UI can default to
// "what I installed" while keeping the rest one toggle away.
var systemUnitPrefixes = []string{
	"systemd-", "user@", "user-runtime-dir@", "getty@", "serial-getty@",
	"snap.", "cloud-", "e2scrub", "lvm2-", "plymouth-", "dev-", "sys-",
	"blk-", "grub-", "apt-", "dpkg-", "networkd-", "ssh-agent",
	"modprobe@", "ifup@", "console-", "keyboard-", "kmod-", "logrotate",
	"man-db", "motd-", "packagekit", "polkit", "rsync", "setvtrgb",
	"udisks", "upower", "uuidd", "whoopsie", "kerneloops",
}

var systemUnitNames = map[string]bool{
	"cron.service": true, "dbus.service": true, "rsyslog.service": true,
	"snapd.service": true, "snapd.seeded.service": true, "apparmor.service": true,
	"apport.service": true, "multipathd.service": true, "irqbalance.service": true,
	"thermald.service": true, "unattended-upgrades.service": true,
	"wpa_supplicant.service": true, "ModemManager.service": true,
	"accounts-daemon.service": true, "gpu-manager.service": true,
	"finalrd.service": true, "dmesg.service": true, "atd.service": true,
	"gdm.service": true, "gdm3.service": true, "colord.service": true,
	"switcheroo-control.service": true, "power-profiles-daemon.service": true,
	"rtkit-daemon.service": true, "bluetooth.service": true, "avahi-daemon.service": true,
	"emergency.service": true, "rescue.service": true, "cups-browsed.service": true,
}

func isSystemUnit(name string) bool {
	if systemUnitNames[name] {
		return true
	}
	for _, p := range systemUnitPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func linuxServices(d *Device) ([]ServiceUnit, error) {
	_, out, err := runRemote(d, linuxServicesScript, remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	sec := sections(out)

	startup := map[string]string{}
	for _, line := range strings.Split(sec["files"], "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			startup[fields[0]] = fields[1]
		}
	}

	var units []ServiceUnit
	for _, line := range strings.Split(sec["units"], "\n") {
		fields := strings.Fields(line)
		// UNIT LOAD ACTIVE SUB DESCRIPTION…
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		name, active, sub := fields[0], fields[2], fields[3]
		// Templates (getty@.service) are not runnable instances.
		if strings.Contains(name, "@.") {
			continue
		}
		state := "stopped"
		switch active {
		case "active":
			state = "running"
		case "failed":
			state = "failed"
		case "activating", "deactivating":
			state = active
		}
		start := startup[name]
		// A "static"/"generated" unit that is not running is a one-shot helper
		// pulled in by something else (apport-autoreport, e2scrub_reap …), not
		// a service anyone manages. Group it with the OS noise.
		housekeeping := state != "running" &&
			(start == "static" || start == "generated" || start == "transient")

		units = append(units, ServiceUnit{
			Name:    name,
			Display: strings.Join(fields[4:], " "),
			State:   state,
			Sub:     sub,
			Startup: start,
			System:  isSystemUnit(name) || housekeeping,
		})
	}
	return units, nil
}

// ── Processes ───────────────────────────────────────────────────────────────

// pcpu is the average over the process's whole lifetime rather than a live
// sample, which is what `top` shows in its first frame too. Good enough for
// spotting what is heavy, and it costs no extra round trip.
const linuxProcessScript = `ps -eo pid,pcpu,rss,user:32,comm --sort=-pcpu --no-headers | head -40`

func linuxProcesses(d *Device) ([]ProcRow, error) {
	_, out, err := runRemote(d, linuxProcessScript, remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	var rows []ProcRow
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		cpuPct, _ := strconv.ParseFloat(fields[1], 64)
		rows = append(rows, ProcRow{
			PID:      pid,
			CPU:      cpuPct,
			MemBytes: parseUint(fields[2]) * 1024, // ps reports RSS in KiB
			User:     fields[3],
			Name:     strings.Join(fields[4:], " "),
		})
	}
	return rows, nil
}

// ── Control ─────────────────────────────────────────────────────────────────

// linuxServiceAction runs systemctl on the target. `sudo -n` never prompts, so
// a box without the sudoers rule fails immediately with a message saying so
// rather than hanging on a password prompt nobody can answer.
func linuxServiceAction(d *Device, unit, action string) error {
	cmd := fmt.Sprintf("sudo -n systemctl %s %s 2>&1", action, unit)
	rc, out, err := runRemote(d, cmd, remoteCmdTimeout)
	if err != nil {
		return err
	}
	if rc != 0 {
		if strings.Contains(out, "password is required") || strings.Contains(out, "sudo:") {
			return fmt.Errorf("perlu aturan sudoers di %s — lihat sudoers-remote-nodes.md", d.Label)
		}
		return fmt.Errorf("%s", firstLine(strings.TrimSpace(out)))
	}
	return nil
}

func linuxKillProcess(d *Device, pid int) error {
	rc, out, err := runRemote(d, fmt.Sprintf("kill -TERM %d 2>&1", pid), remoteCmdTimeout)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("%s", firstLine(strings.TrimSpace(out)))
	}
	return nil
}
