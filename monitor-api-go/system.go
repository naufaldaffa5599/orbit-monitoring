package main

import (
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"
)

// ── In-memory metrics history (kept for 1 hour, sampled ~30s) ───────────────

const maxHistory = 120 // 120 samples × 30s = 1 hour

type sample struct {
	TS string  `json:"ts"`
	V  float64 `json:"v"`
}

// ring is a fixed-capacity FIFO standing in for collections.deque(maxlen=N).
type ring struct {
	mu   sync.Mutex
	buf  []sample
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]sample, n)} }

func (r *ring) push(s sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot returns the samples oldest-first, which is the order the chart
// draws them in.
func (r *ring) snapshot() []sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sample, 0, len(r.buf))
	if r.full {
		out = append(out, r.buf[r.next:]...)
	}
	return append(out, r.buf[:r.next]...)
}

var (
	cpuHistory = newRing(maxHistory)
	ramHistory = newRing(maxHistory)
)

// collectMetrics samples CPU/RAM every 30 seconds for the history chart.
func collectMetrics() {
	for {
		// A 1s blocking sample, like psutil.cpu_percent(interval=1): a
		// zero-interval read would report usage since process start on the
		// first call and since the last call after that, which is not what
		// a 30s-spaced series wants.
		cpuPct, err := cpu.Percent(time.Second, false)
		v := 0.0
		if err == nil && len(cpuPct) > 0 {
			v = round1(cpuPct[0])
		}
		ts := nowWIB()
		cpuHistory.push(sample{TS: ts, V: v})

		if vm, err := mem.VirtualMemory(); err == nil {
			_, pct := memUsed(vm)
			ramHistory.push(sample{TS: ts, V: round1(pct)})
		}
		time.Sleep(30 * time.Second)
	}
}

// memUsed derives used bytes and used-percent from MemAvailable, which is what
// the dashboard has always displayed.
//
// gopsutil's own Used/UsedPercent use the older total-free-buffers-cached
// heuristic and read ~5 points lower on this host. MemAvailable is the kernel's
// own estimate of what a new allocation could actually get, so it is both the
// more accurate number and the one that keeps historical readings comparable.
func memUsed(vm *mem.VirtualMemoryStat) (uint64, float64) {
	if vm.Total == 0 {
		return 0, 0
	}
	if vm.Available == 0 || vm.Available > vm.Total {
		return vm.Used, vm.UsedPercent // no MemAvailable (very old kernels)
	}
	used := vm.Total - vm.Available
	return used, float64(used) / float64(vm.Total) * 100
}

// ── /api/system ─────────────────────────────────────────────────────────────

type tempReading struct {
	Current  float64  `json:"current"`
	High     *float64 `json:"high"`
	Critical *float64 `json:"critical"`
}

func handleSystem(w http.ResponseWriter, r *http.Request) error {
	cpuPct := 0.0
	if pcts, err := cpu.Percent(500*time.Millisecond, false); err == nil && len(pcts) > 0 {
		cpuPct = round1(pcts[0])
	}
	cpuCount, err := cpu.Counts(true)
	if err != nil || cpuCount == 0 {
		cpuCount = 1
	}

	loadAvg := []float64{0, 0, 0}
	if l, err := load.Avg(); err == nil {
		loadAvg = []float64{l.Load1, l.Load5, l.Load15}
	}

	memory := map[string]any{}
	if vm, err := mem.VirtualMemory(); err == nil {
		used, pct := memUsed(vm)
		memory = map[string]any{
			"total_gb": round2(float64(vm.Total) / (1 << 30)),
			"used_gb":  round2(float64(used) / (1 << 30)),
			"percent":  round1(pct),
		}
	}

	diskInfo := map[string]any{}
	if du, err := disk.Usage("/"); err == nil {
		diskInfo = map[string]any{
			"total_gb": round2(float64(du.Total) / (1 << 30)),
			"used_gb":  round2(float64(du.Used) / (1 << 30)),
			"percent":  round1(du.UsedPercent),
		}
	}

	// Temperatures are keyed by label so the frontend can pattern-match
	// "package"/"core 0"/"cpu" to pick the headline reading.
	//
	// When TEMP_DEVICE is set the readings come from that device over SSH
	// (see temperature.go) instead of from this machine, because this one is a
	// VM and has no thermal sensors of its own. tempSource names whichever
	// machine the numbers describe, so the UI can say so rather than implying
	// they belong to the host in the header.
	temps := map[string]tempReading{}
	tempSource := ""
	if tempDevice != "" {
		remote, source, fresh := snapshotRemoteTemps()
		tempSource = source
		if fresh {
			temps = remote
		}
	} else if readings, err := sensors.SensorsTemperatures(); err == nil {
		for _, s := range readings {
			if s.Temperature == 0 {
				continue
			}
			reading := tempReading{Current: s.Temperature}
			if s.High != 0 {
				h := s.High
				reading.High = &h
			}
			if s.Critical != 0 {
				c := s.Critical
				reading.Critical = &c
			}
			temps[s.SensorKey] = reading
		}
	}

	uptime := 0
	if bt, err := host.BootTime(); err == nil {
		uptime = int(time.Now().Unix() - int64(bt))
	}

	network := map[string]uint64{"bytes_sent": 0, "bytes_recv": 0}
	if counters, err := net.IOCounters(false); err == nil && len(counters) > 0 {
		network["bytes_sent"] = counters[0].BytesSent
		network["bytes_recv"] = counters[0].BytesRecv
	}

	hostname, _ := os.Hostname()

	writeJSON(w, 200, map[string]any{
		"hostname": hostname,
		"cpu": map[string]any{
			"percent":  cpuPct,
			"count":    cpuCount,
			"load_avg": loadAvg,
		},
		"memory":             memory,
		"disk":               diskInfo,
		"temperature":        temps,
		"temperature_source": tempSource,
		"uptime_seconds":     uptime,
		"network":            network,
		"timestamp":          nowWIB(),
	})
	return nil
}

func handleSystemHistory(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, map[string]any{
		"cpu": cpuHistory.snapshot(),
		"ram": ramHistory.snapshot(),
	})
	return nil
}
