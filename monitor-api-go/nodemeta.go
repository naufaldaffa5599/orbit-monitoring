package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Display overrides for tree nodes.
//
// Kept in their own file rather than written back into devices.json for two
// reasons. A guest discovered from the hypervisor may have no device entry at
// all — there would be nowhere to put its name — and devices.json holds
// credentials, so the fewer writes it takes for cosmetic reasons the better.
//
// An override only changes what is displayed. The underlying device keeps its
// own label, which is still what the SSH terminal page shows in its title bar.

type nodeOverride struct {
	Label string `json:"label,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

var overrides = struct {
	sync.RWMutex
	m map[string]nodeOverride
}{m: map[string]nodeOverride{}}

func overridesFile() string { return filepath.Join(baseDir, "node-overrides.json") }

func loadOverrides() {
	data, err := os.ReadFile(overridesFile())
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️  Gagal baca node-overrides.json: %v\n", err)
		}
		return
	}
	var m map[string]nodeOverride
	if err := json.Unmarshal(data, &m); err != nil {
		fmt.Printf("⚠️  node-overrides.json rusak: %v\n", err)
		return
	}
	overrides.Lock()
	overrides.m = m
	overrides.Unlock()
}

// saveOverrides writes atomically, matching how devices.json is persisted: a
// truncated file would silently lose every custom name.
func saveOverrides() error {
	overrides.RLock()
	data, err := json.MarshalIndent(overrides.m, "", "  ")
	overrides.RUnlock()
	if err != nil {
		return err
	}
	tmp := overridesFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, overridesFile())
}

func overrideFor(nodeID string) (nodeOverride, bool) {
	overrides.RLock()
	defer overrides.RUnlock()
	ov, ok := overrides.m[nodeID]
	return ov, ok
}

// applyOverride rewrites a node's display fields in place.
func applyOverride(n *Node) {
	ov, ok := overrideFor(n.ID)
	if !ok {
		return
	}
	if ov.Label != "" {
		n.Label = ov.Label
	}
	if ov.Icon != "" {
		n.Icon = ov.Icon
	}
}

func setOverride(nodeID string, ov nodeOverride) error {
	overrides.Lock()
	if ov.Label == "" && ov.Icon == "" {
		delete(overrides.m, nodeID) // back to the discovered name
	} else {
		overrides.m[nodeID] = ov
	}
	overrides.Unlock()
	return saveOverrides()
}

func clearOverride(nodeID string) {
	overrides.Lock()
	_, existed := overrides.m[nodeID]
	delete(overrides.m, nodeID)
	overrides.Unlock()
	if existed {
		_ = saveOverrides()
	}
}

// validLabel keeps names to something that renders in a tree row. Labels are
// only ever displayed, never interpolated into a command, so the check is
// about sanity rather than safety.
func validLabel(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errf(400, "nama tidak boleh kosong")
	}
	if len([]rune(s)) > 48 {
		return errf(400, "nama maksimal 48 karakter")
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return errf(400, "nama tidak boleh mengandung baris baru")
	}
	return nil
}
