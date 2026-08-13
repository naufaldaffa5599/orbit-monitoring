package main

import (
	"bufio"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type serviceStatus struct {
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	Enabled     string `json:"enabled"`
	Description string `json:"description"`
}

func queryServiceStatus(unit string) serviceStatus {
	_, out, _ := runCmd(10*time.Second, systemctlBin, "show", unit,
		"--property=ActiveState,SubState,UnitFileState,Description")

	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.TrimRight(v, "\r")
		}
	}
	get := func(k, fallback string) string {
		if v, ok := props[k]; ok && v != "" {
			return v
		}
		return fallback
	}
	return serviceStatus{
		ActiveState: get("ActiveState", "unknown"),
		SubState:    get("SubState", "unknown"),
		Enabled:     get("UnitFileState", "unknown"),
		Description: get("Description", ""),
	}
}

// handleListServices reports the status of every whitelisted unit. The six
// systemctl calls run concurrently, so the tab's refresh costs one call's
// latency rather than six.
func handleListServices(w http.ResponseWriter, r *http.Request) error {
	statuses := make([]serviceStatus, len(managedServices))
	var wg sync.WaitGroup
	for i, svc := range managedServices {
		wg.Add(1)
		go func(i int, unit string) {
			defer wg.Done()
			statuses[i] = queryServiceStatus(unit)
		}(i, svc.Unit)
	}
	wg.Wait()

	out := make([]map[string]any, 0, len(managedServices))
	for i, svc := range managedServices {
		out = append(out, map[string]any{
			"id": svc.ID, "label": svc.Label, "icon": svc.Icon, "unit": svc.Unit,
			"active_state": statuses[i].ActiveState,
			"sub_state":    statuses[i].SubState,
			"enabled":      statuses[i].Enabled,
			"description":  statuses[i].Description,
		})
	}
	writeJSON(w, 200, out)
	return nil
}

func handleServiceLogs(w http.ResponseWriter, r *http.Request) error {
	svc := serviceByID(r.PathValue("service_id"))
	if svc == nil {
		return errf(404, "service not found")
	}
	lines := 200
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			lines = n
		}
	}
	lines = clampInt(lines, 1, 1000)

	rc, out, errOut := runCmd(10*time.Second, journalctlBin, "-u", svc.Unit,
		"-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso")
	if rc != 0 {
		detail := strings.TrimSpace(errOut)
		if detail == "" {
			detail = "gagal ambil log"
		}
		return errf(500, detail)
	}

	split := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(split) == 1 && split[0] == "" {
		split = []string{}
	}
	writeJSON(w, 200, map[string]any{"lines": split})
	return nil
}

func handleServiceAction(w http.ResponseWriter, r *http.Request) error {
	action := r.PathValue("action")
	switch action {
	case "start", "stop", "restart":
	default:
		return errf(400, "action must be 'start', 'stop', or 'restart'")
	}
	svc := serviceByID(r.PathValue("service_id"))
	if svc == nil {
		return errf(404, "service not found")
	}

	// Requires a passwordless-sudo rule scoped to exactly these systemctl
	// start/stop/restart <unit> commands (see monitor-api/sudoers-services.md)
	// — the API process itself runs unprivileged (User=daffa in the unit file).
	rc, out, errOut := runCmd(20*time.Second, sudoBin, "-n", systemctlBin, action, svc.Unit)
	if rc != 0 {
		detail := strings.TrimSpace(errOut)
		if detail == "" {
			detail = strings.TrimSpace(out)
		}
		if strings.Contains(detail, "password is required") {
			detail = "Sudo belum di-setup buat kontrol service ini. Lihat monitor-api/sudoers-services.md."
		}
		if detail == "" {
			detail = "gagal " + action + " service"
		}
		return errf(500, detail)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// handleLogsWS live-tails a service's journal (journalctl -f) over a websocket.
func handleLogsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	svc := serviceByID(r.PathValue("service_id"))
	if svc == nil {
		closeWS(conn, 4404, "service not found")
		return
	}

	cmd := exec.Command(journalctlBin, "-u", svc.Unit, "-n", "200", "-f", "-o", "short-iso")
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte("[error starting journalctl: "+err.Error()+"]"))
		closeWS(conn, 4500, "journalctl failed")
		return
	}

	// journalctl -f never exits on its own, so the read loop below is what
	// decides this handler's lifetime: it ends when the client disconnects,
	// and the deferred kill takes the process down with it.
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var writeMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			writeMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, sc.Bytes())
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}
