package main

import (
	"net"
	"sync"
	"testing"
	"time"
)

// Fixtures are verbatim output captured from the real machines, so a change in
// the parsers is checked against what the boxes actually print rather than
// against what the format was assumed to be.

const fixtureQMList = `      VMID NAME                 STATUS     MEM(MB)    BOOTDISK(GB) PID
       100 NAS.100              running    2048             100.00 1352
       200 project              running    6144             110.00 1432
       555 cf                   stopped    1024             100.00 0
`

func TestParseQMList(t *testing.T) {
	guests := parseQMList(fixtureQMList)
	if len(guests) != 3 {
		t.Fatalf("got %d guests, want 3", len(guests))
	}
	want := []pveGuest{
		{VMID: 100, Name: "NAS.100", Status: "running"},
		{VMID: 200, Name: "project", Status: "running"},
		{VMID: 555, Name: "cf", Status: "stopped"},
	}
	for i, w := range want {
		if guests[i].VMID != w.VMID || guests[i].Name != w.Name || guests[i].Status != w.Status {
			t.Errorf("guest %d = %+v, want %+v", i, guests[i], w)
		}
	}
}

// The real output nests block-device statistics under indented keys; only
// top-level ones must be read, or `status:` inside a nested block would win.
const fixtureQMStatus = `balloon: 2147483648
ballooninfo:
	actual: 2147483648
	free_mem: 108273664
	max_mem: 2147483648
	total_mem: 2063577088
blockstat:
	scsi0:
		account_failed: 1
		status: bogus
cpus: 1
disk: 0
maxdisk: 107374182400
maxmem: 2147483648
mem: 1955303424
name: NAS.100
qmpstatus: running
status: running
uptime: 22226
vmid: 100
`

func TestParseQMStatus(t *testing.T) {
	g := pveGuest{VMID: 100}
	parseQMStatus(fixtureQMStatus, &g)

	if g.Status != "running" {
		t.Errorf("Status = %q, want running (a nested key must not win)", g.Status)
	}
	if g.MaxMem != 2147483648 {
		t.Errorf("MaxMem = %d, want 2147483648", g.MaxMem)
	}
	if g.Mem != 1955303424 {
		t.Errorf("Mem = %d, want 1955303424", g.Mem)
	}
	if g.CPUs != 1 {
		t.Errorf("CPUs = %d, want 1", g.CPUs)
	}
	if g.Uptime != 22226 {
		t.Errorf("Uptime = %d, want 22226", g.Uptime)
	}
	if g.MaxDisk != 107374182400 {
		t.Errorf("MaxDisk = %d, want 107374182400", g.MaxDisk)
	}
}

func TestCPUPercentFromSamples(t *testing.T) {
	// 100 jiffies elapse, 75 of them idle → 25% busy.
	s1 := "cpu  1000 0 500 8000 100 0 50 0 0 0"
	s2 := "cpu  1015 0 510 8070 105 0 50 0 0 0"
	if got := cpuPercentFromSamples(s1, s2); got != 25 {
		t.Errorf("cpuPercentFromSamples = %v, want 25", got)
	}
	// A counter reset (target rebooted between samples) must not go negative.
	if got := cpuPercentFromSamples(s2, s1); got != 0 {
		t.Errorf("reversed samples = %v, want 0", got)
	}
	if got := cpuPercentFromSamples("garbage", s2); got != 0 {
		t.Errorf("unparseable sample = %v, want 0", got)
	}
}

func TestSections(t *testing.T) {
	out := "#s1\ncpu 1 2 3\n#mem\nMemTotal: 100 kB\nMemAvailable: 40 kB\n#host\nnas"
	sec := sections(out)
	if sec["host"] != "nas" {
		t.Errorf("host = %q, want nas", sec["host"])
	}
	if sec["s1"] != "cpu 1 2 3" {
		t.Errorf("s1 = %q", sec["s1"])
	}
	if sec["mem"] != "MemTotal: 100 kB\nMemAvailable: 40 kB" {
		t.Errorf("mem = %q", sec["mem"])
	}
}

func TestIsSystemUnit(t *testing.T) {
	// Taken from the NAS: 27 running units, of which these are noise.
	system := []string{
		"systemd-journald.service", "systemd-logind.service", "dbus.service",
		"cron.service", "getty@tty1.service", "polkit.service", "snapd.service",
		"snap.cups.cupsd.service", "user@1000.service", "ModemManager.service",
		"rsyslog.service", "udisks2.service", "upower.service",
		"unattended-upgrades.service", "multipathd.service", "wpa_supplicant.service",
	}
	for _, name := range system {
		if !isSystemUnit(name) {
			t.Errorf("isSystemUnit(%q) = false, want true", name)
		}
	}
	// These are the ones actually worth showing.
	interesting := []string{
		"filebrowser.service", "smbd.service", "nmbd.service", "pm2-daffa.service",
		"tailscaled.service", "ssh.service", "monitor-api.service",
		"discord-bot.service", "docker.service", "jellyfin.service",
	}
	for _, name := range interesting {
		if isSystemUnit(name) {
			t.Errorf("isSystemUnit(%q) = true, want false", name)
		}
	}
}

func TestIsWindowsSystemService(t *testing.T) {
	if !isWindowsSystemService(`C:\Windows\system32\svchost.exe -k netsvcs`) {
		t.Error("svchost should be flagged as a system service")
	}
	if isWindowsSystemService(`"C:\Program Files\Docker\Docker\resources\dockerd.exe"`) {
		t.Error("dockerd should not be flagged as a system service")
	}
}

// ConvertTo-Json emits a bare object when exactly one item matches, which is
// the shape that breaks a naive []T decode.
func TestUnmarshalList(t *testing.T) {
	var single []winSummaryRaw
	if err := unmarshalList(`{"host":"PC-DAFFA","cpuCount":4}`, &single); err != nil {
		t.Fatalf("single object: %v", err)
	}
	if len(single) != 1 || single[0].Host != "PC-DAFFA" {
		t.Errorf("single = %+v", single)
	}

	var many []winSummaryRaw
	if err := unmarshalList(`[{"host":"a"},{"host":"b"}]`, &many); err != nil {
		t.Fatalf("array: %v", err)
	}
	if len(many) != 2 {
		t.Errorf("array len = %d, want 2", len(many))
	}

	var empty []winSummaryRaw
	if err := unmarshalList("", &empty); err != nil || len(empty) != 0 {
		t.Errorf("empty = %+v, err = %v", empty, err)
	}

	var bad []winSummaryRaw
	if err := unmarshalList("Get-Process : Access denied", &bad); err == nil {
		t.Error("an error message should not parse as JSON")
	}
}

func TestNormalizeAndLinkVM(t *testing.T) {
	devs := []*Device{
		{ID: "nas", Label: "NAS", Host: "192.168.100.100", Protocol: "ssh", Auth: &sshAuth{Type: "password"}},
		{ID: "ubuntu-host", Label: "This Machine (Ubuntu)", Host: "127.0.0.1", Auth: &sshAuth{Type: "key"}},
		{ID: "pc-kamar", Label: "PC Kamar", Protocol: "wol", Auth: &sshAuth{Type: "password"}},
		{ID: "explicit", Label: "Whatever", VMID: 555, Protocol: "ssh", Auth: &sshAuth{Type: "password"}},
	}

	// "NAS.100" → strip the trailing VMID → "nas" matches the device id.
	if got := linkVM(pveGuest{VMID: 100, Name: "NAS.100"}, devs); got == nil || got.ID != "nas" {
		t.Errorf("NAS.100 linked to %v, want nas", got)
	}
	// An explicit VMID wins over any name similarity.
	if got := linkVM(pveGuest{VMID: 555, Name: "cf"}, devs); got == nil || got.ID != "explicit" {
		t.Errorf("vmid 555 linked to %v, want explicit", got)
	}
	// Nothing resembles this, and a WOL entry is not a way in.
	if got := linkVM(pveGuest{VMID: 999, Name: "zzz-unknown"}, devs); got != nil {
		t.Errorf("unknown guest linked to %v, want nil", got)
	}
}

func TestValidUnitName(t *testing.T) {
	ok := []string{"docker.service", "pm2-daffa.service", "getty@tty1.service", "MSSQL$SQLEXPRESS"}
	for _, s := range ok {
		if s == "MSSQL$SQLEXPRESS" {
			continue // '$' is intentionally rejected; see below
		}
		if !validUnitName(s) {
			t.Errorf("validUnitName(%q) = false, want true", s)
		}
	}
	bad := []string{"", "a;rm -rf /", "$(whoami)", "a b", "svc$name", "`id`"}
	for _, s := range bad {
		if validUnitName(s) {
			t.Errorf("validUnitName(%q) = true, want false", s)
		}
	}
}

// The poller holds the *Check pointers that all() handed out, so an edit that
// mutated the pointed-to struct raced every field of it — confirmed with
// -race before this was fixed. update() swaps a whole value under the write
// lock instead. Run with -race or this test proves nothing.
func TestCheckUpdateIsRaceFree(t *testing.T) {
	checks.list = []*Check{{ID: "x", Label: "a", URL: "http://a", Enabled: true}}
	t.Cleanup(func() { checks.list = nil })

	var wg sync.WaitGroup
	wg.Add(2)

	go func() { // what pollChecks does every 30s
		defer wg.Done()
		for range 2000 {
			for _, c := range checks.all() {
				_ = c.URL
				_ = checks.state(c)
			}
		}
	}()

	go func() { // what an edit arriving over HTTP does
		defer wg.Done()
		for range 2000 {
			checks.update("x", func(next Check) Check {
				next.URL = "http://b"
				next.Label = "bbbbbbbb"
				return next
			})
		}
	}()

	wg.Wait()

	// The id is identity: an edit must never be able to move a check.
	if got := checks.get("x"); got == nil {
		t.Fatal("check hilang setelah update")
	}
}

// A notification is worth sending only on a real transition. These are the
// cases that make the difference between a useful alert and noise.
func TestEvaluateTransitions(t *testing.T) {
	alerted.m = map[string]time.Time{}
	t.Cleanup(func() { alerted.m = map[string]time.Time{} })

	start := time.Now()
	down := func(id string, streak int) CheckState {
		at := start
		return CheckState{
			Check:      Check{ID: id, Label: id, Enabled: true},
			Last:       &CheckResult{OK: false, Error: "timeout", At: start},
			ChangedAt:  &at,
			FailStreak: streak,
		}
	}
	up := func(id string) CheckState {
		// Recovery arrives five minutes later, and record() has by then moved
		// ChangedAt to *this* moment — exactly the case that used to lose the
		// downtime from the message.
		at := start.Add(5 * time.Minute)
		return CheckState{
			Check:     Check{ID: id, Label: id, Enabled: true},
			Last:      &CheckResult{OK: true, Status: 200, At: at},
			ChangedAt: &at,
		}
	}

	// One failure is a blip, not an outage.
	if got := evaluateTransitions([]CheckState{down("a", 1)}); len(got) != 0 {
		t.Errorf("streak 1 alerted %d kali, harusnya diam", len(got))
	}

	// Two in a row is worth saying.
	got := evaluateTransitions([]CheckState{down("a", 2)})
	if len(got) != 1 || !got[0].down {
		t.Fatalf("streak 2 = %+v, harusnya satu alert down", got)
	}

	// Still down on the next cycle: already said, stay quiet.
	if got := evaluateTransitions([]CheckState{down("a", 3)}); len(got) != 0 {
		t.Errorf("down berkelanjutan alerted lagi %d kali", len(got))
	}

	// Recovery is announced exactly once.
	got = evaluateTransitions([]CheckState{up("a")})
	if len(got) != 1 || got[0].down {
		t.Fatalf("recovery = %+v, harusnya satu alert pulih", got)
	}
	if got[0].downFor != 5*time.Minute {
		t.Errorf("downFor = %v, want 5m — durasi hilang dari pesan pulih", got[0].downFor)
	}
	if got := evaluateTransitions([]CheckState{up("a")}); len(got) != 0 {
		t.Errorf("tetap up alerted %d kali", len(got))
	}

	// A disabled check never speaks, however broken it looks.
	off := down("b", 9)
	off.Enabled = false
	if got := evaluateTransitions([]CheckState{off}); len(got) != 0 {
		t.Errorf("check nonaktif alerted %d kali", len(got))
	}

	// A check that has never run has nothing to report.
	never := CheckState{Check: Check{ID: "c", Enabled: true}}
	if got := evaluateTransitions([]CheckState{never}); len(got) != 0 {
		t.Errorf("check tanpa hasil alerted %d kali", len(got))
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45 detik"},
		{5 * time.Minute, "5 menit"},
		{3 * time.Hour, "3 jam"},
		{72 * time.Hour, "3 hari"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// HTTP headers are latin-1: a UTF-8 emoji in one arrives mangled, which is why
// ntfy titles are stripped and the icon is carried by Tags instead.
func TestASCIIOnly(t *testing.T) {
	cases := map[string]string{
		"🔴 NAS down":             "NAS down",
		"✅ Poka Server pulih":    "Poka Server pulih",
		"🔔 Tes notifikasi Orbit": "Tes notifikasi Orbit",
		"plain ascii":            "plain ascii",
	}
	for in, want := range cases {
		if got := asciiOnly(in); got != want {
			t.Errorf("asciiOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// A known-answer test, because the receiver is the only other thing that knows
// this format and it will not tell us why it rejected us. The digest below came
// from `openssl dgst -sha256 -hmac`, so it pins the composition — timestamp,
// then a literal dot, then the raw body — independently of this code.
func TestWebhookSignature(t *testing.T) {
	const (
		secret  = "s3cr3t"
		ts      = "1700000000"
		payload = `{"title":"probe"}`
		want    = "7d795dcb5fe7fb6857bc34f42f294bbbd2023f0133d99e59f1be83c004b6462a"
	)

	if got := webhookSignature(secret, ts, []byte(payload)); got != want {
		t.Errorf("webhookSignature = %q, want %q", got, want)
	}

	// Replay protection is only real if the timestamp actually reaches the MAC:
	// signing it alongside the body rather than into it would leave the digest
	// unchanged here, and a captured request would stay valid forever.
	moved := webhookSignature(secret, "1700000001", []byte(payload))
	if moved == want {
		t.Error("signature ignores the timestamp — replay protection is not wired up")
	}
}

// machineStatus is what turns the grey dot in the tree green, so it is worth a
// test against real sockets rather than a stub: an open port, a closed one, and
// an entry there is no way to probe at all.
func TestMachineStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	openPort := ln.Addr().(*net.TCPAddr).Port

	// A port that was bound and released: nothing answers, and on Linux the
	// connect is refused immediately rather than hanging to the timeout.
	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := spare.Addr().(*net.TCPAddr).Port
	spare.Close()

	creds := &sshAuth{Type: "password", Value: "x"}
	primaries := map[string]*Device{
		"up":   {ID: "up", Host: "127.0.0.1", Port: openPort, Username: "u", Protocol: "ssh", Auth: creds},
		"down": {ID: "down", Host: "127.0.0.1", Port: closedPort, Username: "u", Protocol: "ssh", Auth: creds},
		// No credentials and no host to ping: unknowable, and saying "offline"
		// would be a guess dressed up as a reading.
		"dark": {ID: "dark", Protocol: "wol", MACAddress: "aabbccddeeff"},
	}

	invalidate("\x00machines-reach")
	got := machineStatus(primaries)

	want := map[string]string{"up": "online", "down": "offline", "dark": "unknown"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: got %q, want %q", id, got[id], w)
		}
	}

	// The verdict is cached: a machine that went down between polls must not
	// cost every request in the next 15 seconds a fresh connect.
	ln.Close()
	if again := machineStatus(primaries); again["up"] != "online" {
		t.Errorf("cached verdict lost: got %q, want %q", again["up"], "online")
	}
	invalidate("\x00machines-reach")
}
