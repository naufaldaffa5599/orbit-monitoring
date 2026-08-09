package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTP surface for the resource tree. Everything here is per-node; the older
// /api/system, /api/services and /api/processes endpoints stay as they are and
// describe the host this API runs on.

func handleListNodes(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, buildTree())
	return nil
}

// resolveNode is the shared preamble: find the node, and refuse politely when
// it has no credentials rather than failing deep inside a collector.
func resolveNode(r *http.Request, needCreds bool) (*Node, *Device, error) {
	id := r.PathValue("node_id")
	node, dev := nodeByID(id)
	if node == nil {
		return nil, nil, errf(404, "node tidak ditemukan")
	}
	if needCreds && dev == nil {
		return nil, nil, errf(409, "node ini belum punya kredensial SSH — tambahkan lewat + Add device")
	}
	return node, dev, nil
}

func handleNodeSummary(w http.ResponseWriter, r *http.Request) error {
	node, dev, err := resolveNode(r, false)
	if err != nil {
		return err
	}
	if node.Kind == KindVM && node.Status != "running" {
		return errf(409, "VM ini sedang "+node.Status)
	}

	key := cacheKey(node.ID, "summary")
	summary, cacheErr := cached(key, remoteTTL, func() (NodeSummary, error) {
		return collectSummary(node, dev)
	})
	writeJSON(w, 200, map[string]any{"summary": summary, "error": cacheErr})
	return nil
}

func handleNodeServices(w http.ResponseWriter, r *http.Request) error {
	node, dev, err := resolveNode(r, true)
	if err != nil {
		return err
	}
	key := cacheKey(node.ID, "services")
	units, cacheErr := cached(key, remoteTTL, func() ([]ServiceUnit, error) {
		return collectServices(node, dev)
	})
	// Stable order: failures first (they are why anyone opens this list), then
	// running, then the rest alphabetically.
	sort.SliceStable(units, func(i, j int) bool {
		rank := func(u ServiceUnit) int {
			switch u.State {
			case "failed":
				return 0
			case "running":
				return 1
			default:
				return 2
			}
		}
		if rank(units[i]) != rank(units[j]) {
			return rank(units[i]) < rank(units[j])
		}
		return strings.ToLower(units[i].Name) < strings.ToLower(units[j].Name)
	})
	writeJSON(w, 200, map[string]any{"services": units, "error": cacheErr})
	return nil
}

func handleNodeServiceAction(w http.ResponseWriter, r *http.Request) error {
	node, dev, err := resolveNode(r, true)
	if err != nil {
		return err
	}
	unit := r.PathValue("unit")
	action := r.PathValue("action")
	switch action {
	case "start", "stop", "restart":
	default:
		return errf(400, "aksi harus start, stop, atau restart")
	}
	// The unit name comes from a request, so it is never interpolated into a
	// shell without checking. systemd unit names and Windows service names are
	// both drawn from a small, well-defined character set.
	if !validUnitName(unit) {
		return errf(400, "nama service tidak valid")
	}

	if strings.EqualFold(dev.OS, "windows") {
		err = windowsServiceAction(dev, unit, action)
	} else {
		err = linuxServiceAction(dev, unit, action)
	}
	if err != nil {
		return errf(502, err.Error())
	}
	// The next poll must see the new state, not the snapshot from before.
	invalidate(cacheKey(node.ID, "services"))
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func handleNodeProcesses(w http.ResponseWriter, r *http.Request) error {
	node, dev, err := resolveNode(r, true)
	if err != nil {
		return err
	}
	key := cacheKey(node.ID, "processes")
	rows, cacheErr := cached(key, remoteTTL, func() ([]ProcRow, error) {
		return collectProcesses(node, dev)
	})

	sortKey := r.URL.Query().Get("sort")
	sort.SliceStable(rows, func(i, j int) bool {
		if sortKey == "ram" {
			return rows[i].MemBytes > rows[j].MemBytes
		}
		return rows[i].CPU > rows[j].CPU
	})
	if len(rows) > 40 {
		rows = rows[:40]
	}
	writeJSON(w, 200, map[string]any{"processes": rows, "error": cacheErr})
	return nil
}

func handleNodeKillProcess(w http.ResponseWriter, r *http.Request) error {
	node, dev, err := resolveNode(r, true)
	if err != nil {
		return err
	}
	pid, convErr := strconv.Atoi(r.PathValue("pid"))
	if convErr != nil || pid <= 1 {
		// PID 1 is init/System — killing it takes the machine down, and no
		// dashboard button should be able to do that by accident.
		return errf(400, "PID tidak valid")
	}
	if strings.EqualFold(dev.OS, "windows") {
		err = windowsKillProcess(dev, pid)
	} else {
		err = linuxKillProcess(dev, pid)
	}
	if err != nil {
		return errf(502, err.Error())
	}
	invalidate(cacheKey(node.ID, "processes"))
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// handleDatacenter is the Proxmox-style overview: the hypervisor plus one row
// per node, each with the few figures that belong in a fleet view.
//
// Summaries are gathered concurrently and only for nodes that could answer —
// a stopped VM or a laptop that is switched off would otherwise add its
// connect timeout to the page load, one after another.
func handleDatacenter(w http.ResponseWriter, r *http.Request) error {
	nodes := buildTree()

	type row struct {
		Node    Node        `json:"node"`
		Summary NodeSummary `json:"summary"`
		Error   string      `json:"error,omitempty"`
	}
	rows := make([]row, len(nodes))

	var wg sync.WaitGroup
	for i, n := range nodes {
		rows[i] = row{Node: n}
		// Only ask nodes that can answer: a stopped VM, an Android entry, or a
		// machine with no credentials would each contribute a timeout and an
		// error string to a view that is meant to be a glance.
		collectible := (n.DeviceID != "" && n.CanServices) || n.Kind == KindVM
		if !collectible || (n.Kind == KindVM && n.Status != "running") {
			continue
		}
		wg.Add(1)
		go func(i int, n Node) {
			defer wg.Done()
			dev := devices.get(n.DeviceID)
			summary, cacheErr := cached(cacheKey(n.ID, "summary"), remoteTTL,
				func() (NodeSummary, error) { return collectSummary(&n, dev) })
			rows[i].Summary = summary
			rows[i].Error = cacheErr
		}(i, n)
	}
	wg.Wait()

	writeJSON(w, 200, map[string]any{"nodes": rows, "generated_at": nowWIB()})
	return nil
}

// validUnitName guards the one place a request-supplied string reaches a shell.
func validUnitName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_' || c == '@' || c == '\\' || c == ':':
		default:
			return false
		}
	}
	return true
}

func invalidate(key string) {
	e := entryFor(key)
	e.mu.Lock()
	e.at = time.Time{}
	e.mu.Unlock()
}

// handleRenameNode changes a node's displayed name and icon.
//
// The override is keyed by node id, so it works for a guest that has no device
// entry of its own — exactly the case where the hypervisor's name ("NAS.100")
// is the least useful one.
func handleRenameNode(w http.ResponseWriter, r *http.Request) error {
	node, _, err := resolveNode(r, false)
	if err != nil {
		return err
	}
	var payload struct {
		Label string `json:"label"`
		Icon  string `json:"icon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return errf(400, "body tidak valid")
	}
	payload.Label = strings.TrimSpace(payload.Label)
	payload.Icon = strings.TrimSpace(payload.Icon)

	if payload.Label != "" {
		if err := validLabel(payload.Label); err != nil {
			return err
		}
	}
	if len([]rune(payload.Icon)) > 4 {
		return errf(400, "icon maksimal 4 karakter")
	}
	if err := setOverride(node.ID, nodeOverride{Label: payload.Label, Icon: payload.Icon}); err != nil {
		return errf(500, "gagal simpan nama: "+err.Error())
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// handleDeleteNode removes every registry entry behind a node.
//
// A machine often has two — one for Wake-on-LAN, one for SSH — and deleting
// only the one the tree happened to pick would leave a half-node behind. A
// guest discovered from the hypervisor keeps existing after this; what goes is
// the credentials, so it falls back to hypervisor-only figures.
func handleDeleteNode(w http.ResponseWriter, r *http.Request) error {
	node, _, err := resolveNode(r, false)
	if err != nil {
		return err
	}

	ids := node.DeviceIDs
	if len(ids) == 0 && node.DeviceID != "" {
		ids = []string{node.DeviceID}
	}
	if len(ids) == 0 {
		return errf(409, "node ini nggak punya entry device buat dihapus")
	}

	removed := 0
	for _, id := range ids {
		if devices.remove(id) {
			removed++
			closeDeviceSessions(id)
			closeDeviceMonitoring(id)
		}
	}
	if removed == 0 {
		return errf(404, "device tidak ditemukan")
	}
	if err := devices.save(); err != nil {
		return errf(500, "gagal simpan devices.json: "+err.Error())
	}
	clearOverride(node.ID)

	// The hypervisor entry is what the whole guest branch hangs off; drop it
	// and the tree has to be rebuilt without it.
	if node.ID == pveDeviceID {
		pveDeviceID = ""
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed": removed})
	return nil
}
