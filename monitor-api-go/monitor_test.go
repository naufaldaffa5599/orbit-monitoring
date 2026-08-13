package main

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Mirrors the frontend's headline picker in app.js:
//
//	if (/package|core 0|cpu/i.test(label))
var regexpHeadline = regexp.MustCompile(`(?i)package|core 0|cpu`)

func TestNormalizeMAC(t *testing.T) {
	cases := []struct{ in, want string }{
		{"AA:BB:CC:DD:EE:FF", "aabbccddeeff"},
		{"AA-BB-CC-DD-EE-FF", "aabbccddeeff"},
		{"aabbccddeeff", "aabbccddeeff"},
		{"AA:BB:CC:DD:EE", ""},       // too short
		{"AA:BB:CC:DD:EE:FF:00", ""}, // too long
		{"ZZ:BB:CC:DD:EE:FF", ""},    // non-hex stripped, leaves 10 chars
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeMAC(c.in); got != c.want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Laptop Rumah", "laptop-rumah"},
		{"PC Kamar!!", "pc-kamar"},
		{"  Pocket-Asik!  ", "pocket-asik"},
		{"!!!", "device"}, // never produce an empty id
		{"", "device"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWOLPacket(t *testing.T) {
	pkt, err := wolPacket("aabbccddeeff")
	if err != nil {
		t.Fatalf("wolPacket: %v", err)
	}
	if len(pkt) != 102 {
		t.Fatalf("packet is %d bytes, want 102", len(pkt))
	}
	if !bytes.Equal(pkt[:6], []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("header = % x, want six 0xFF", pkt[:6])
	}
	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	for i := 0; i < 16; i++ {
		off := 6 + i*6
		if !bytes.Equal(pkt[off:off+6], mac) {
			t.Fatalf("repetition %d = % x, want % x", i, pkt[off:off+6], mac)
		}
	}
	if _, err := wolPacket("aabbcc"); err == nil {
		t.Error("wolPacket accepted a 3-byte MAC, want error")
	}
}

func TestSessionIDRoundTrip(t *testing.T) {
	for _, id := range []string{"a", "tab-1", "GoTest_9", strings.Repeat("x", 32)} {
		name, err := tmuxSessionName(id)
		if err != nil {
			t.Fatalf("tmuxSessionName(%q): %v", id, err)
		}
		if got := sessionIDFromTmux(name); got != id {
			t.Errorf("round trip of %q gave %q", id, got)
		}
	}

	// Rejected ids are what keep the value safe to interpolate into a shell
	// command, so this is a security check, not a formatting one.
	for _, bad := range []string{
		"", "bad.id", "bad:id", "a b", "x;rm -rf /", "$(whoami)", "`id`",
		strings.Repeat("x", 33),
	} {
		if _, err := tmuxSessionName(bad); err == nil {
			t.Errorf("tmuxSessionName(%q) succeeded, want rejection", bad)
		}
	}

	// Someone else's tmux session must not be mistaken for one of ours.
	if got := sessionIDFromTmux("my-own-session"); got != "" {
		t.Errorf("foreign session parsed as %q, want empty", got)
	}
}

func TestProjectLabel(t *testing.T) {
	homeDir = "/home/daffa"
	cases := []struct{ cwd, want string }{
		{"/home/daffa/projects/Pocket-Asik!/server", "Pocket-Asik!"},
		{"/home/daffa/projects/monitoring", "monitoring"},
		{"/home/daffa/.9router", "9router"},
		{"/home/daffa/.9router/sub", "9router"},
		{"/usr/lib/systemd", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := projectLabel(c.cwd); got != c.want {
			t.Errorf("projectLabel(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

func TestTrimToRuneStart(t *testing.T) {
	// A byte-level cut through "é" (0xC3 0xA9) must not leave the orphaned
	// continuation byte at the front, or the replayed scrollback opens with a
	// replacement char.
	full := []byte("héllo")
	cut := full[2:] // drops 'h' and the lead byte of 'é'
	got := string(trimToRuneStart(cut))
	if got != "llo" {
		t.Errorf("trimToRuneStart = %q, want %q", got, "llo")
	}
	if s := string(trimToRuneStart([]byte("plain"))); s != "plain" {
		t.Errorf("trimToRuneStart mangled ASCII: %q", s)
	}
}

func TestRingKeepsNewestInOrder(t *testing.T) {
	r := newRing(3)
	for _, v := range []float64{1, 2, 3, 4, 5} {
		r.push(sample{TS: "t", V: v})
	}
	got := r.snapshot()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []float64{3, 4, 5} {
		if got[i].V != want {
			t.Errorf("sample %d = %v, want %v (oldest first)", i, got[i].V, want)
		}
	}

	// Before wrapping, only what was pushed should come back.
	r2 := newRing(3)
	r2.push(sample{V: 9})
	if s := r2.snapshot(); len(s) != 1 || s[0].V != 9 {
		t.Errorf("partial ring = %v, want one sample of 9", s)
	}
}

func TestPsutilStatusNames(t *testing.T) {
	// The frontend's statusMap is keyed on psutil's vocabulary; an unmapped
	// value silently degrades to raw text in the UI.
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"sleep"}, "sleeping"},
		{[]string{"running"}, "running"},
		{[]string{"stop"}, "stopped"},
		{[]string{"blocked"}, "disk_sleep"},
		{[]string{"zombie"}, "zombie"},
		{[]string{"unheard-of"}, "unheard-of"},
		{[]string{""}, ""},
	}
	for _, c := range cases {
		if got := psutilStatus(c.in); got != c.want {
			t.Errorf("psutilStatus(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Verbatim `sensors -j` from the pve host — including the parts that must be
// ignored: a wifi chip reporting an empty feature, GPU voltage rails, and a
// clock feature whose subkey is also named *_input.
const pveSensors = `{"iwlwifi_1-virtual-0":{"Adapter":"Virtual device","temp1":{}},` +
	`"nvme-pci-0300":{"Adapter":"PCI adapter","Composite":{"temp1_input":46.850000,` +
	`"temp1_max":74.850000,"temp1_min":-0.150000,"temp1_crit":79.850000,"temp1_alarm":0.000000},` +
	`"Sensor 2":{"temp3_input":46.850000},"Sensor 3":{"temp4_input":49.850000}},` +
	`"amdgpu-pci-0400":{"Adapter":"PCI adapter","vddgfx":{},"vddnb":{},` +
	`"edge":{"temp1_input":55.000000},"sclk":{"freq1_input":400000000.000000}},` +
	`"k10temp-pci-00c3":{"Adapter":"PCI adapter","Tctl":{"temp1_input":55.625000}}}`

func TestParseSensorsJSON(t *testing.T) {
	got, err := parseSensorsJSON(pveSensors)
	if err != nil {
		t.Fatalf("parseSensorsJSON: %v", err)
	}

	want := map[string]float64{
		"CPU Tctl":       55.625,
		"GPU edge":       55.0,
		"NVMe Composite": 46.85,
		"NVMe Sensor 2":  46.85,
		"NVMe Sensor 3":  49.85,
	}
	for label, temp := range want {
		r, ok := got[label]
		if !ok {
			t.Errorf("missing reading %q (got keys %v)", label, keysOf(got))
			continue
		}
		if r.Current != temp {
			t.Errorf("%s = %v, want %v", label, r.Current, temp)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d readings %v, want %d", len(got), keysOf(got), len(want))
	}

	// A clock is not a temperature, however much its subkey looks like one.
	if _, ok := got["GPU sclk"]; ok {
		t.Error("GPU sclk (a frequency) was parsed as a temperature")
	}
	// Empty features must not become 0 °C readings.
	for _, absent := range []string{"iwlwifi_1 temp1", "GPU vddgfx", "GPU vddnb"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%q should have been skipped", absent)
		}
	}

	// high/critical carry through for the drive that reports them.
	nvme := got["NVMe Composite"]
	if nvme.High == nil || *nvme.High != 74.85 {
		t.Errorf("NVMe high = %v, want 74.85", nvme.High)
	}
	if nvme.Critical == nil || *nvme.Critical != 79.85 {
		t.Errorf("NVMe critical = %v, want 79.85", nvme.Critical)
	}
	if cpu := got["CPU Tctl"]; cpu.High != nil || cpu.Critical != nil {
		t.Error("CPU reported high/critical it does not have")
	}
}

// The frontend picks the headline reading with /package|core 0|cpu/i and
// otherwise falls back to whichever key sorts first — which on this box is the
// GPU. Losing the "CPU" prefix would silently show the wrong number as "the"
// machine temperature.
func TestHeadlineTemperatureIsTheCPU(t *testing.T) {
	got, err := parseSensorsJSON(pveSensors)
	if err != nil {
		t.Fatal(err)
	}
	headline := ""
	for label := range got {
		if regexpHeadline.MatchString(label) {
			if headline != "" {
				t.Errorf("more than one label matches the headline regex: %q and %q", headline, label)
			}
			headline = label
		}
	}
	if headline != "CPU Tctl" {
		t.Errorf("headline label = %q, want %q", headline, "CPU Tctl")
	}
}

func TestSensorLabel(t *testing.T) {
	cases := []struct{ chip, feature, want string }{
		{"k10temp-pci-00c3", "Tctl", "CPU Tctl"},
		{"coretemp-isa-0000", "Package id 0", "CPU Package id 0"},
		{"amdgpu-pci-0400", "edge", "GPU edge"},
		{"nvme-pci-0300", "Composite", "NVMe Composite"},
		{"drivetemp-scsi-0-0", "temp1", "Disk temp1"},
		{"acpitz-acpi-0", "temp1", "acpitz temp1"}, // unknown chip keeps its name
	}
	for _, c := range cases {
		if got := sensorLabel(c.chip, c.feature); got != c.want {
			t.Errorf("sensorLabel(%q, %q) = %q, want %q", c.chip, c.feature, got, c.want)
		}
	}
}

func TestParseSensorsRejectsGarbage(t *testing.T) {
	if _, err := parseSensorsJSON("bash: sensors: command not found"); err == nil {
		t.Error("accepted non-JSON output, want error")
	}
	// A leading warning line before the JSON body must not break parsing.
	got, err := parseSensorsJSON("ERROR: Can't get value of subfeature\n" + pveSensors)
	if err != nil {
		t.Fatalf("leading warning broke parsing: %v", err)
	}
	if _, ok := got["CPU Tctl"]; !ok {
		t.Error("lost readings when output had a leading warning")
	}
}

func keysOf(m map[string]tempReading) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDeviceDefaults(t *testing.T) {
	// devices.json entries written before the protocol field existed have no
	// port and no protocol; the probe and connect paths must still work.
	d := &Device{ID: "legacy", Username: "daffa", Auth: &sshAuth{Type: "password"}}
	if d.sshPort() != 22 {
		t.Errorf("sshPort() = %d, want 22", d.sshPort())
	}
	if d.adbPort() != 5555 {
		t.Errorf("adbPort() = %d, want 5555", d.adbPort())
	}
	if !d.hasSSH() {
		t.Error("hasSSH() = false for a device with username + auth")
	}
	if (&Device{ID: "wol-only", MACAddress: "aabbccddeeff"}).hasSSH() {
		t.Error("hasSSH() = true for a WOL device with no credentials")
	}
}
