package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// This host is a VM with no thermal sensors, so its own psutil-style reading is
// always empty. TEMP_DEVICE points at a device from the registry whose
// `sensors -j` output is used for the dashboard's temperature card instead.
//
// The reading is collected by a background poller rather than on request:
// /api/system is polled every few seconds by every open dashboard, and an SSH
// handshake per request would be both slow and a needless login storm on the
// target.
const (
	tempPollInterval = 30 * time.Second
	// After this long with no successful read, the cache reports empty rather
	// than a stale number. A temperature card silently showing a five-minute-old
	// value is worse than one honestly showing N/A.
	tempMaxAge = 3 * tempPollInterval
)

var remoteTemp = struct {
	sync.RWMutex
	readings map[string]tempReading
	source   string
	at       time.Time
	lastErr  string
}{readings: map[string]tempReading{}}

// snapshotRemoteTemps returns the cached readings, the source label, and
// whether the cache is fresh enough to show.
//
// The label is resolved even when nothing has been read yet: a source that has
// never answered is exactly the case where the UI most needs to name which
// machine to go and look at.
func snapshotRemoteTemps() (map[string]tempReading, string, bool) {
	remoteTemp.RLock()
	fresh := !remoteTemp.at.IsZero() && time.Since(remoteTemp.at) < tempMaxAge
	source := remoteTemp.source
	out := make(map[string]tempReading, len(remoteTemp.readings))
	if fresh {
		for k, v := range remoteTemp.readings {
			out[k] = v
		}
	}
	remoteTemp.RUnlock()

	if source == "" {
		source = tempDevice // last resort: the id from the config
		if d := devices.get(tempDevice); d != nil && d.Label != "" {
			source = d.Label
		}
	}
	return out, source, fresh
}

func pollRemoteTemps() {
	for {
		fetchRemoteTemps()
		time.Sleep(tempPollInterval)
	}
}

// fetchRemoteTemps opens a connection, reads sensors, and drops the connection.
//
// Reconnecting every 30s costs one handshake and sidesteps the whole class of
// stale-connection bugs; holding a permanent root session open to the
// hypervisor to save 300ms is a bad trade.
func fetchRemoteTemps() {
	d := devices.get(tempDevice)
	if d == nil {
		setTempErr(fmt.Sprintf("TEMP_DEVICE=%q tidak ada di devices.json", tempDevice))
		return
	}
	if !d.hasSSH() {
		setTempErr(fmt.Sprintf("device %q tidak punya kredensial SSH", tempDevice))
		return
	}

	client, err := sshConnect(d)
	if err != nil {
		setTempErr("connect: " + err.Error())
		return
	}
	defer client.Close()

	rc, out := sshRun(client, "sensors -j", 10*time.Second)
	if rc != 0 {
		// lm-sensors emits warnings on stderr for unreadable chips and still
		// exits non-zero, so only give up when there is no usable JSON at all.
		if !strings.Contains(out, "{") {
			setTempErr(fmt.Sprintf("`sensors -j` keluar dengan kode %d (lm-sensors terpasang?)", rc))
			return
		}
	}

	readings, err := parseSensorsJSON(out)
	if err != nil {
		setTempErr("parse: " + err.Error())
		return
	}
	if len(readings) == 0 {
		setTempErr("`sensors -j` tidak mengembalikan pembacaan suhu satupun")
		return
	}

	remoteTemp.Lock()
	remoteTemp.readings = readings
	remoteTemp.source = d.Label
	remoteTemp.at = time.Now()
	remoteTemp.lastErr = ""
	remoteTemp.Unlock()
}

func setTempErr(msg string) {
	remoteTemp.Lock()
	// Only log on change: a device that is off would otherwise write a line
	// to the journal every 30 seconds, forever.
	if remoteTemp.lastErr != msg {
		fmt.Printf("⚠️  suhu dari %s: %s\n", tempDevice, msg)
		remoteTemp.lastErr = msg
	}
	remoteTemp.Unlock()
}

// parseSensorsJSON converts lm-sensors' JSON into the shape the frontend has
// always consumed: {label: {current, high, critical}}.
//
// The input nests chip → feature → subfeature, where subfeature keys carry an
// arbitrary index ("temp1_input", "temp5_input"), so readings are located by
// suffix rather than by exact name. Non-temperature features (fan speeds,
// voltages, clocks) simply have no *_input temperature key and drop out.
func parseSensorsJSON(raw string) (map[string]tempReading, error) {
	// `sensors -j` on some versions prints warnings before the JSON body.
	start := strings.Index(raw, "{")
	if start < 0 {
		return nil, fmt.Errorf("tidak ada JSON di output")
	}

	var chips map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw[start:]), &chips); err != nil {
		return nil, err
	}

	out := map[string]tempReading{}
	for chip, features := range chips {
		for feature, rawFeature := range features {
			// "Adapter" is a string, every real feature is an object.
			var sub map[string]float64
			if json.Unmarshal(rawFeature, &sub) != nil {
				continue
			}

			var reading tempReading
			found := false
			for key, val := range sub {
				switch {
				case strings.HasSuffix(key, "_input"):
					// Voltage and clock features also use _input; only a
					// temp*_input is a temperature.
					if strings.HasPrefix(key, "temp") {
						reading.Current = val
						found = true
					}
				case strings.HasSuffix(key, "_max"):
					v := val
					reading.High = &v
				case strings.HasSuffix(key, "_crit"):
					v := val
					reading.Critical = &v
				}
			}
			if found {
				out[sensorLabel(chip, feature)] = reading
			}
		}
	}
	return out, nil
}

// sensorLabel turns "k10temp-pci-00c3" + "Tctl" into "CPU Tctl".
//
// The naming is load-bearing, not cosmetic: the frontend picks the headline
// reading by matching /package|core 0|cpu/i against the label, and falls back
// to whichever entry happens to be first otherwise. Without "CPU" in the name,
// an AMD box would show its SSD temperature as the machine temperature.
func sensorLabel(chip, feature string) string {
	base := chip
	for _, sep := range []string{"-pci-", "-virtual-", "-isa-", "-acpi-", "-i2c-", "-usb-"} {
		if i := strings.Index(base, sep); i >= 0 {
			base = base[:i]
			break
		}
	}

	var prefix string
	switch {
	case containsAny(base, "k10temp", "coretemp", "zenpower", "cpu"):
		prefix = "CPU"
	case containsAny(base, "amdgpu", "nouveau", "radeon", "i915"):
		prefix = "GPU"
	case containsAny(base, "nvme"):
		prefix = "NVMe"
	case containsAny(base, "drivetemp"):
		prefix = "Disk"
	default:
		prefix = base
	}

	if strings.EqualFold(prefix, feature) {
		return prefix
	}
	return prefix + " " + feature
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
