package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The resource tree.
//
//	Datacenter
//	└── PVE                     ← the Proxmox host (hypervisor)
//	    ├── [VM] NAS            ← discovered from `qm list`
//	    ├── [VM] project        ← discovered, and linked to a device with creds
//	    └── PC Utama            ← a plain machine, added by hand
//
// Guests are discovered from the hypervisor, so a VM created in Proxmox turns
// up here on its own. Everything else is a device from devices.json. A guest
// and a device describe the same machine from two sides — the hypervisor knows
// its VMID and how much RAM it was given, the device knows how to log into it —
// so the two are linked (see linkVM) and presented as one node.

// NodeKind distinguishes what a row in the tree actually is.
type NodeKind string

const (
	KindHypervisor NodeKind = "hypervisor"
	KindVM         NodeKind = "vm"
	KindMachine    NodeKind = "machine"
	// A 9router usage node: not a machine at all, just a view over the API
	// router's request log. Lives at the bottom of the tree.
	KindRouter9 NodeKind = "router9"
	// A heading, not a machine: the branch the hand-added boxes hang off.
	// Carries no host, no credentials and no pages of its own.
	KindGroup NodeKind = "group"
)

// outpostsID is the synthetic node the hand-added machines hang off.
//
// They need a branch of their own: they are not the hypervisor's guests, but
// left parentless they sat at the same level as the hypervisor itself, and a
// growing list of them buried it. "Outposts" because that is what they are —
// boxes running on their own out beyond the main server, not inside it.
const outpostsID = "outposts"

type Node struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Kind   NodeKind `json:"kind"`
	Parent string   `json:"parent,omitempty"`
	OS     string   `json:"os"`
	Icon   string   `json:"icon,omitempty"`
	Host   string   `json:"host,omitempty"`

	// DeviceID is the devices.json entry that holds this node's SSH
	// credentials. Empty means the node was discovered from the hypervisor and
	// nobody has told us how to log in yet — it can still show the CPU/RAM the
	// hypervisor reports, but not services or processes.
	DeviceID string `json:"device_id,omitempty"`

	VMID int `json:"vmid,omitempty"`

	// Status is the hypervisor's view for guests ("running"/"stopped"), or a
	// reachability probe for everything else.
	Status string `json:"status"`

	// Capabilities the frontend uses to decide which views to offer.
	CanShell    bool `json:"can_shell"`
	CanServices bool `json:"can_services"`
	CanTasks    bool `json:"can_tasks"`
	CanWake     bool `json:"can_wake"`
	CanPower    bool `json:"can_power"`

	// DeviceIDs are every registry entry folded into this node — a machine
	// often has one entry for Wake-on-LAN and another for SSH.
	DeviceIDs []string `json:"device_ids,omitempty"`
	// WakeDeviceID is the entry carrying the MAC address, if any.
	WakeDeviceID string `json:"wake_device_id,omitempty"`

	// Hypervisor-reported figures, present for guests even with no credentials.
	GuestMemMax  uint64 `json:"guest_mem_max,omitempty"`
	GuestMemUsed uint64 `json:"guest_mem_used,omitempty"`
	GuestCPUs    int    `json:"guest_cpus,omitempty"`
	GuestUptime  int64  `json:"guest_uptime,omitempty"`
	GuestDiskMax uint64 `json:"guest_disk_max,omitempty"`
	Unreachable  string `json:"unreachable,omitempty"`
	IsLocal      bool   `json:"is_local"`

	// Health checks attached to this node, so the tree can flag a machine
	// whose app is down even while the machine itself looks fine.
	CheckCount    int `json:"check_count"`
	ChecksFailing int `json:"checks_failing"`
}

// ── PVE inventory ───────────────────────────────────────────────────────────

// pveDeviceID names the devices.json entry used to reach the Proxmox host.
// Configurable because not everyone's hypervisor entry is called the same
// thing; empty disables the whole hypervisor branch and leaves a flat list.
var pveDeviceID = ""

// detectPVEDevice picks the hypervisor when PVE_DEVICE was not set, so a fresh
// install finds an existing "pve"/"proxmox" entry without being configured.
func detectPVEDevice() {
	if pveDeviceID != "" {
		if devices.get(pveDeviceID) == nil {
			fmt.Printf("⚠️  PVE_DEVICE=%q tidak ada di devices.json\n", pveDeviceID)
			pveDeviceID = ""
		}
		return
	}
	for _, d := range devices.all() {
		if d.Auth == nil || d.Protocol == "android" {
			continue
		}
		name := normalizeName(d.ID + " " + d.Label)
		if strings.Contains(name, "pve") || strings.Contains(name, "proxmox") {
			pveDeviceID = d.ID
			fmt.Printf("  hypervisor: %s (%s)\n", d.Label, d.ID)
			return
		}
	}
}

type pveGuest struct {
	VMID    int
	Name    string
	Status  string
	MaxMem  uint64
	Mem     uint64
	CPUs    int
	Uptime  int64
	MaxDisk uint64
}

var qmListRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(\S+)`)

// parseQMList reads `qm list`:
//
//	VMID NAME                 STATUS     MEM(MB)    BOOTDISK(GB) PID
//	 100 NAS.100              running    2048             100.00 1352
func parseQMList(out string) []pveGuest {
	var guests []pveGuest
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "VMID") {
			continue // header
		}
		m := qmListRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		guests = append(guests, pveGuest{VMID: id, Name: m[2], Status: m[3]})
	}
	return guests
}

// parseQMStatus reads the `key: value` block from `qm status <id> --verbose`.
// Only the handful of keys the dashboard shows are picked out; the output also
// carries several hundred lines of block-device statistics.
func parseQMStatus(out string, g *pveGuest) {
	for _, line := range strings.Split(out, "\n") {
		// Nested blocks are indented; only top-level keys are wanted.
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "maxmem":
			g.MaxMem = parseUint(value)
		case "mem":
			g.Mem = parseUint(value)
		case "cpus":
			g.CPUs = int(parseUint(value))
		case "uptime":
			g.Uptime = int64(parseUint(value))
		case "maxdisk":
			g.MaxDisk = parseUint(value)
		case "status":
			g.Status = value
		}
	}
}

func parseUint(s string) uint64 {
	// Values arrive as plain integers, occasionally as floats ("100.00").
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		s = s[:dot]
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

// collectGuests asks the hypervisor what it is running. One `qm list` plus one
// `qm status` per guest, in a single SSH command to keep it to one round trip.
func collectGuests(d *Device) ([]pveGuest, error) {
	_, listOut, err := runRemote(d, "qm list", remoteCmdTimeout)
	if err != nil {
		return nil, err
	}
	guests := parseQMList(listOut)
	if len(guests) == 0 {
		return guests, nil
	}

	var script strings.Builder
	for _, g := range guests {
		script.WriteString("echo '#vm " + strconv.Itoa(g.VMID) + "'; qm status " +
			strconv.Itoa(g.VMID) + " --verbose 2>/dev/null; ")
	}
	_, statusOut, err := runRemote(d, script.String(), remoteCmdTimeout)
	if err != nil {
		return guests, nil // the list alone is still useful
	}

	byID := map[int]*pveGuest{}
	for i := range guests {
		byID[guests[i].VMID] = &guests[i]
	}
	for _, block := range strings.Split(statusOut, "#vm ") {
		if block == "" {
			continue
		}
		head, rest, _ := strings.Cut(block, "\n")
		id, err := strconv.Atoi(strings.TrimSpace(head))
		if err != nil {
			continue
		}
		if g := byID[id]; g != nil {
			parseQMStatus(rest, g)
		}
	}
	return guests, nil
}

// ── Tree assembly ───────────────────────────────────────────────────────────

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func normalizeName(s string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(s), "")
}

// linkVM finds the device that holds credentials for a guest.
//
// There is no QEMU guest agent on any of these VMs, so the hypervisor cannot
// report a guest's IP and the two sides have to be matched by name. An explicit
// VMID on the device always wins; otherwise names are compared loosely, because
// what Proxmox calls "NAS.100" is the device labelled "NAS", and the guest
// named "project" is the host that calls itself "project-daffa".
func linkVM(g pveGuest, devs []*Device) *Device {
	for _, d := range devs {
		if d.VMID != 0 && d.VMID == g.VMID {
			return d
		}
	}
	want := normalizeName(g.Name)
	// Strip a trailing VMID some naming schemes append ("NAS.100" → "nas").
	want = strings.TrimSuffix(want, strconv.Itoa(g.VMID))
	if want == "" {
		return nil
	}
	for _, d := range devs {
		if d.Protocol == "wol" || d.Protocol == "android" || d.Auth == nil {
			continue // no way in, so nothing to link
		}
		candidates := []string{d.ID, d.Label, d.Host}
		// The VM this API runs inside is reached as 127.0.0.1, so neither its
		// host nor its label ever resembles the guest name Proxmox shows —
		// only the machine's own hostname does ("project" ⊂ "project-daffa").
		if isLocalHost(d.Host) {
			if hostname, err := os.Hostname(); err == nil {
				candidates = append(candidates, hostname)
			}
		}
		for _, candidate := range candidates {
			got := normalizeName(candidate)
			if got == "" {
				continue
			}
			if got == want || strings.HasPrefix(got, want) || strings.HasPrefix(want, got) {
				return d
			}
		}
	}
	return nil
}

func isLocalHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func nodeOS(d *Device) string {
	if d != nil && d.OS != "" {
		return d.OS
	}
	return "linux"
}

// buildTree assembles the whole tree: the hypervisor, its guests, and every
// other device that is reachable in its own right.
func buildTree() []Node {
	devs := devices.all()
	nodes := []Node{}
	used := map[string]bool{}

	var pveDev *Device
	if pveDeviceID != "" {
		pveDev = devices.get(pveDeviceID)
	}

	rootID := ""
	if pveDev != nil {
		rootID = pveDev.ID
		used[pveDev.ID] = true
		nodes = append(nodes, Node{
			ID: pveDev.ID, Label: pveDev.Label, Kind: KindHypervisor,
			OS: "linux", Icon: pveDev.iconOr("🖧"), Host: pveDev.Host,
			DeviceID: pveDev.ID, Status: "running",
			CanShell: true, CanServices: true,
			DeviceIDs: []string{pveDev.ID},
		})
		applyOverride(&nodes[0])

		guests, snapErr := cached(cacheKey(pveDev.ID, "guests"), 30*time.Second,
			func() ([]pveGuest, error) { return collectGuests(pveDev) })
		for _, g := range guests {
			linked := linkVM(g, devs)
			node := Node{
				ID: "vm-" + strconv.Itoa(g.VMID), Label: g.Name, Kind: KindVM,
				Parent: rootID, VMID: g.VMID, Status: g.Status, Icon: "🖥️",
				GuestMemMax: g.MaxMem, GuestMemUsed: g.Mem,
				GuestCPUs: g.CPUs, GuestUptime: g.Uptime, GuestDiskMax: g.MaxDisk,
				OS: nodeOS(linked),
			}
			if linked != nil {
				used[linked.ID] = true
				node.DeviceID = linked.ID
				node.Host = linked.Host
				node.Label = linked.Label
				node.Icon = linked.iconOr("🖥️")
				node.IsLocal = isLocalHost(linked.Host)
				node.CanShell = g.Status == "running"
				node.CanServices = g.Status == "running"
				node.CanTasks = g.Status == "running" && linked.OS == "windows"
			}
			applyOverride(&node)
			nodes = append(nodes, node)
		}
		if snapErr != "" && len(guests) == 0 {
			nodes[0].Unreachable = snapErr
		}
	}

	// Everything else: machines added by hand.
	//
	// Entries are grouped by host first. One physical machine commonly has two
	// registry entries — a Wake-on-LAN one holding its MAC and an SSH one
	// holding credentials — and listing them as two nodes would put the same
	// laptop in the tree twice, each half-usable.
	byHost := map[string][]*Device{}
	var order []string
	for _, d := range devs {
		if used[d.ID] {
			continue
		}
		key := d.Host
		if key == "" {
			key = "id:" + d.ID // nothing to group on; keep it standalone
		}
		if _, seen := byHost[key]; !seen {
			order = append(order, key)
		}
		byHost[key] = append(byHost[key], d)
	}

	// The entry each machine node is probed through, filled in below and
	// resolved in one batch afterwards.
	primaries := map[string]*Device{}

	// Collected first and appended together, because the Outposts heading only
	// exists if there is something to put under it.
	var machines []Node

	for _, key := range order {
		group := byHost[key]
		node := Node{
			Kind: KindMachine, Parent: outpostsID, Status: "unknown",
			Host: group[0].Host, IsLocal: isLocalHost(group[0].Host),
		}
		// The primary entry is whichever can actually open a shell; it also
		// supplies the node's identity, so the tree shows "Laptop Rumah"
		// rather than "Laptop Rumah (SSH)".
		var primary *Device
		for _, d := range group {
			node.DeviceIDs = append(node.DeviceIDs, d.ID)
			isSSH := d.Protocol == "ssh" && d.Auth != nil
			if primary == nil || (isSSH && primary.Protocol != "ssh") {
				primary = d
			}
			if d.Protocol == "wol" && d.MACAddress != "" {
				node.CanWake = true
				node.WakeDeviceID = d.ID
			}
			if d.Auth != nil && d.Protocol != "android" {
				node.CanPower = true
			}
		}

		hasSSH := primary.Auth != nil && primary.Protocol == "ssh"
		node.ID = primary.ID
		node.DeviceID = primary.ID
		node.OS = nodeOS(primary)
		node.Icon = primary.iconOr("🖥️")
		node.Label = strings.TrimSpace(strings.TrimSuffix(primary.Label, "(SSH)"))
		node.CanShell = hasSSH
		node.CanServices = hasSSH
		node.CanTasks = hasSSH && strings.EqualFold(primary.OS, "windows")
		applyOverride(&node)
		primaries[node.ID] = primary
		machines = append(machines, node)
	}

	if len(machines) > 0 {
		nodes = append(nodes, Node{
			ID: outpostsID, Label: "Outposts", Kind: KindGroup, Icon: "📡",
		})
		nodes = append(nodes, machines...)
	}

	// Machines have no hypervisor to ask, so their status comes from a probe.
	// Guests keep the hypervisor's answer, which is authoritative: a VM can be
	// "running" while its SSH port is still coming up.
	if reach := machineStatus(primaries); len(reach) > 0 {
		for i := range nodes {
			if nodes[i].Kind != KindMachine {
				continue
			}
			if s := reach[nodes[i].ID]; s != "" {
				nodes[i].Status = s
			}
		}
	}

	// The 9router usage node hangs off Datacenter beside everything else, last,
	// so the tree ends where the API-router view starts.
	if router9Pass != "" {
		nodes = append(nodes, Node{
			ID: "router9", Label: "9router", Kind: KindRouter9,
			OS: "router", Icon: "🔀", Host: router9URL, Status: "running",
		})
	}

	// Last, so every node in the tree gets its tally — including any appended
	// after the main loops.
	annotateChecks(nodes)
	return nodes
}

// How long a reachability verdict is reused. Short enough that switching a
// machine on shows up within a poll or two, long enough that the probe is not
// what the tree spends its time on.
const reachTTL = 15 * time.Second

// machineStatus probes hand-added machines and returns "online"/"offline" per
// node id, or "unknown" for a machine there is no way to check — a
// Wake-on-LAN-only entry on a host with no ping binary.
//
// The whole fleet shares one cache entry and one round of goroutines. That
// matters because buildTree sits on the path of *every* per-node request: with
// a probe per call, a laptop that is switched off would add its connect
// timeout to each of them. Here a machine that is off costs one timeout, once
// per TTL, in parallel with all the others.
func machineStatus(primaries map[string]*Device) map[string]string {
	if len(primaries) == 0 {
		return nil
	}
	ids := make([]string, 0, len(primaries))
	for id := range primaries {
		ids = append(ids, id)
	}
	sort.Strings(ids) // stable order so the goroutine results line up

	// One fixed key, not one derived from the ids: a device added mid-TTL then
	// reads as "unknown" for a few seconds rather than stranding a cache entry
	// per device-list shape.
	out, _ := cached("\x00machines-reach", reachTTL, func() (map[string]string, error) {
		verdict := make([]string, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Add(1)
			go func(i int, d *Device) {
				defer wg.Done()
				switch up := checkDeviceUp(d); {
				case up == nil:
					verdict[i] = "unknown"
				case *up:
					verdict[i] = "online"
				default:
					verdict[i] = "offline"
				}
			}(i, primaries[id])
		}
		wg.Wait()

		m := make(map[string]string, len(ids))
		for i, id := range ids {
			m[id] = verdict[i]
		}
		return m, nil
	})
	return out
}

// annotateChecks folds the health-check tally onto each node, so the tree can
// flag a machine whose app is down even while the machine itself looks fine.
func annotateChecks(nodes []Node) {
	total := map[string]int{}
	failing := map[string]int{}
	for _, c := range checks.all() {
		if c.NodeID == "" {
			continue
		}
		total[c.NodeID]++
		if !c.Enabled {
			continue
		}
		if st := checks.state(c); st.Last != nil && !st.Last.OK {
			failing[c.NodeID]++
		}
	}
	for i := range nodes {
		nodes[i].CheckCount = total[nodes[i].ID]
		nodes[i].ChecksFailing = failing[nodes[i].ID]
	}
}

// nodeByID resolves a tree node and the device to reach it with.
func nodeByID(id string) (*Node, *Device) {
	for _, n := range buildTree() {
		if n.ID == id {
			node := n
			if node.DeviceID == "" {
				return &node, nil
			}
			return &node, devices.get(node.DeviceID)
		}
	}
	return nil, nil
}
