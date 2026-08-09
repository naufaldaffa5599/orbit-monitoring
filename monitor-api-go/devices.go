package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// sshAuth mirrors the "auth" object stored in devices.json. Fields are
// omitempty so a password device round-trips without growing an empty "path",
// keeping hand-edited files readable.
type sshAuth struct {
	Type       string `json:"type"`
	Value      string `json:"value,omitempty"`
	Path       string `json:"path,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

type Device struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Icon       string   `json:"icon,omitempty"`
	Host       string   `json:"host,omitempty"`
	Port       int      `json:"port,omitempty"`
	Username   string   `json:"username,omitempty"`
	OS         string   `json:"os,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	MACAddress string   `json:"mac_address,omitempty"`
	Auth       *sshAuth `json:"auth,omitempty"`

	// VMID pins this device to a Proxmox guest. Without a QEMU guest agent the
	// hypervisor cannot report a guest's IP, so guests are otherwise matched to
	// devices by name (see linkVM) — this is the escape hatch for when that
	// guess is wrong or the names simply differ.
	VMID int `json:"vmid,omitempty"`
}

// hasSSH reports whether this device carries credentials usable for a shell or
// a power action. "wol" devices may or may not, which is why it is a check and
// not a protocol comparison.
func (d *Device) hasSSH() bool { return d.Auth != nil && d.Username != "" }

func (d *Device) sshPort() int {
	if d.Port > 0 {
		return d.Port
	}
	return 22
}

func (d *Device) adbPort() int {
	if d.Port > 0 {
		return d.Port
	}
	return 5555
}

func (d *Device) iconOr(fallback string) string {
	if d.Icon != "" {
		return d.Icon
	}
	return fallback
}

// registry keeps insertion order, because that is the order the device list
// renders in and a map alone would reshuffle it on every poll.
type registry struct {
	mu   sync.RWMutex
	list []*Device
}

var devices = &registry{}

func (r *registry) get(id string) *Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, d := range r.list {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (r *registry) all() []*Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Device, len(r.list))
	copy(out, r.list)
	return out
}

func (r *registry) load() {
	data, err := os.ReadFile(devicesFile)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️  Failed to load devices.json: %v\n", err)
		}
		return
	}
	var list []*Device
	if err := json.Unmarshal(data, &list); err != nil {
		fmt.Printf("⚠️  Failed to load devices.json: %v\n", err)
		return
	}
	r.mu.Lock()
	r.list = list
	r.mu.Unlock()
}

// save writes atomically: a truncated devices.json would cost every stored
// credential, and this file is rewritten on every add/delete.
func (r *registry) save() error {
	r.mu.RLock()
	data, err := json.MarshalIndent(r.list, "", "  ")
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := devicesFile + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, devicesFile)
}

func (r *registry) add(d *Device) {
	r.mu.Lock()
	r.list = append(r.list, d)
	r.mu.Unlock()
}

func (r *registry) remove(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, d := range r.list {
		if d.ID == id {
			r.list = append(r.list[:i], r.list[i+1:]...)
			return true
		}
	}
	return false
}

// uniqueID appends -2, -3, … until the slug is free.
func (r *registry) uniqueID(base string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	taken := func(id string) bool {
		for _, d := range r.list {
			if d.ID == id {
				return true
			}
		}
		return false
	}
	id := base
	for n := 2; taken(id); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(label string) string {
	slug := strings.Trim(slugStrip.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if slug == "" {
		return "device"
	}
	return slug
}

var nonHex = regexp.MustCompile(`[^0-9a-fA-F]`)

// normalizeMAC accepts "AA:BB:CC:DD:EE:FF", "AA-BB-CC-DD-EE-FF" or bare hex and
// returns 12 lowercase hex chars, or "" if it is not a valid MAC address.
func normalizeMAC(mac string) string {
	hexOnly := nonHex.ReplaceAllString(mac, "")
	if len(hexOnly) != 12 {
		return ""
	}
	return strings.ToLower(hexOnly)
}

// ── Handlers ────────────────────────────────────────────────────────────────

// handleListDevices lists configured devices. Credentials are never included —
// only whether SSH creds exist, which the UI needs to decide if a "wol" device
// can also be restarted/shut down.
func handleListDevices(w http.ResponseWriter, r *http.Request) error {
	out := []map[string]any{}
	for _, d := range devices.all() {
		os_ := d.OS
		if os_ == "" {
			os_ = "linux"
		}
		protocol := d.Protocol
		if protocol == "" {
			protocol = "ssh"
		}
		out = append(out, map[string]any{
			"id":        d.ID,
			"label":     d.Label,
			"icon":      d.iconOr("🖥️"),
			"os":        os_,
			"host":      d.Host,
			"protocol":  protocol,
			"ssh_power": d.hasSSH(),
		})
	}
	writeJSON(w, 200, out)
	return nil
}

func tcpProbe(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func pingProbe(host string) bool {
	if pingBin == "" {
		return false
	}
	rc, _, _ := runCmd(2*time.Second, pingBin, "-c", "1", "-W", "1", host)
	return rc == 0
}

// checkDeviceUp returns a tri-state: true/false when reachability could be
// determined, nil when we have no way to check (WOL-only device with no SSH
// creds and no ping binary).
func checkDeviceUp(d *Device) *bool {
	host := strings.TrimSpace(d.Host)
	if host == "" {
		return nil
	}
	yes, no := true, false

	if d.Protocol == "android" {
		if tcpProbe(host, d.adbPort(), 1500*time.Millisecond) {
			return &yes
		}
		return &no
	}
	if d.hasSSH() {
		if tcpProbe(host, d.sshPort(), 1500*time.Millisecond) {
			return &yes
		}
		return &no
	}
	if pingBin != "" {
		if pingProbe(host) {
			return &yes
		}
		return &no
	}
	return nil
}

// handleDevicesStatus probes every device concurrently. Each probe is a
// goroutine rather than an async task, so a device whose TCP connect hangs
// until timeout cannot delay the others or the rest of the server.
func handleDevicesStatus(w http.ResponseWriter, r *http.Request) error {
	list := devices.all()
	results := make([]*bool, len(list))

	var wg sync.WaitGroup
	for i, d := range list {
		wg.Add(1)
		go func(i int, d *Device) {
			defer wg.Done()
			results[i] = checkDeviceUp(d)
		}(i, d)
	}
	wg.Wait()

	out := map[string]*bool{}
	for i, d := range list {
		out[d.ID] = results[i]
	}
	writeJSON(w, 200, out)
	return nil
}

type deviceCreate struct {
	Label          string `json:"label"`
	Icon           string `json:"icon"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	OS             string `json:"os"`
	Protocol       string `json:"protocol"`
	AuthType       string `json:"auth_type"`
	Password       string `json:"password"`
	KeyPath        string `json:"key_path"`
	Passphrase     string `json:"passphrase"`
	MACAddress     string `json:"mac_address"`
	EnableSSHPower bool   `json:"enable_ssh_power"`
}

// applyDefaults fills in what pydantic's field defaults used to supply.
func (p *deviceCreate) applyDefaults() {
	if p.Port == 0 {
		p.Port = 22
	}
	if p.OS == "" {
		p.OS = "linux"
	}
	if p.Protocol == "" {
		p.Protocol = "ssh"
	}
	if p.AuthType == "" {
		p.AuthType = "password"
	}
}

func buildSSHAuth(authType, password, keyPath, passphrase string) (*sshAuth, error) {
	switch authType {
	case "key":
		if keyPath == "" {
			return nil, errf(400, "key_path is required for key auth")
		}
		return &sshAuth{Type: "key", Path: keyPath, Passphrase: passphrase}, nil
	case "password":
		return &sshAuth{Type: "password", Value: password}, nil
	default:
		return nil, errf(400, "auth_type must be 'password' or 'key'")
	}
}

func handleAddDevice(w http.ResponseWriter, r *http.Request) error {
	var payload deviceCreate
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}
	payload.applyDefaults()

	if payload.Label == "" {
		return errf(400, "label wajib diisi")
	}
	switch payload.Protocol {
	case "ssh", "wol", "android":
	default:
		return errf(400, "protocol must be 'ssh', 'wol', or 'android'")
	}

	deviceID := devices.uniqueID(slugify(payload.Label))
	var d *Device

	switch payload.Protocol {
	case "wol":
		mac := normalizeMAC(payload.MACAddress)
		if mac == "" {
			return errf(400, "mac_address wajib diisi dan harus format MAC yang valid")
		}
		d = &Device{
			ID: deviceID, Label: payload.Label,
			Icon: cmpOr(payload.Icon, "🖥️"), Host: payload.Host,
			OS: payload.OS, Protocol: "wol", MACAddress: mac,
		}
		if payload.EnableSSHPower {
			auth, err := buildSSHAuth(payload.AuthType, payload.Password, payload.KeyPath, payload.Passphrase)
			if err != nil {
				return err
			}
			d.Port, d.Username, d.Auth = payload.Port, payload.Username, auth
		}

	case "android":
		d = &Device{
			ID: deviceID, Label: payload.Label,
			Icon: cmpOr(payload.Icon, "📱"), Host: payload.Host,
			Port: payload.Port, // ADB port, typically 5555
			OS:   "android", Protocol: "android",
		}

	default:
		auth, err := buildSSHAuth(payload.AuthType, payload.Password, payload.KeyPath, payload.Passphrase)
		if err != nil {
			return err
		}
		d = &Device{
			ID: deviceID, Label: payload.Label,
			Icon: cmpOr(payload.Icon, "🖥️"), Host: payload.Host,
			Port: payload.Port, Username: payload.Username,
			OS: payload.OS, Protocol: "ssh", Auth: auth,
		}
	}

	devices.add(d)
	if err := devices.save(); err != nil {
		devices.remove(deviceID)
		return errf(500, "gagal simpan devices.json: "+err.Error())
	}

	writeJSON(w, 200, map[string]any{
		"id": d.ID, "label": d.Label, "icon": d.Icon,
		"os": d.OS, "host": d.Host, "protocol": d.Protocol,
	})
	return nil
}

func handleDeleteDevice(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("device_id")
	if !devices.remove(id) {
		return errf(404, "device not found")
	}
	if err := devices.save(); err != nil {
		return errf(500, "gagal simpan devices.json: "+err.Error())
	}
	closeDeviceSessions(id)
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func cmpOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
