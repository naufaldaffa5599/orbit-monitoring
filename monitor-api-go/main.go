// Monitor API — system metrics, managed systemd services, and a web SSH
// terminal, for this host and a small registry of LAN devices.
package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

// withCORS mirrors the permissive CORS the Python version enabled: the
// frontend is opened by IP from phones and laptops on the LAN, so pinning an
// origin would break the main way this thing is used.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func routes() http.Handler {
	mux := http.NewServeMux()

	// System metrics (public — no auth required)
	mux.Handle("GET /api/system", handler(handleSystem))
	mux.Handle("GET /api/system/history", handler(handleSystemHistory))
	mux.Handle("GET /api/processes", handler(handleProcesses))
	mux.Handle("GET /api/processes/{pid}", handler(handleProcessDetail))

	// Devices
	mux.Handle("GET /api/devices", handler(handleListDevices))
	mux.Handle("GET /api/devices/status", handler(handleDevicesStatus))
	mux.Handle("POST /api/devices", handler(handleAddDevice))
	mux.Handle("DELETE /api/devices/{device_id}", handler(handleDeleteDevice))
	mux.Handle("GET /api/devices/{device_id}/sessions", handler(handleListSessions))
	mux.Handle("DELETE /api/devices/{device_id}/sessions/{session_id}", handler(handleKillSession))
	mux.Handle("POST /api/devices/{device_id}/wol", handler(handleWake))
	mux.Handle("POST /api/devices/{device_id}/power/{action}", handler(handlePowerAction))

	// Resource tree: the hypervisor, its guests, and every other machine
	mux.Handle("GET /api/nodes", handler(handleListNodes))
	mux.Handle("PATCH /api/nodes/{node_id}", handler(handleRenameNode))
	mux.Handle("DELETE /api/nodes/{node_id}", handler(handleDeleteNode))
	mux.Handle("GET /api/datacenter", handler(handleDatacenter))
	mux.Handle("GET /api/nodes/{node_id}/summary", handler(handleNodeSummary))
	mux.Handle("GET /api/nodes/{node_id}/services", handler(handleNodeServices))
	mux.Handle("POST /api/nodes/{node_id}/services/{unit}/{action}", handler(handleNodeServiceAction))
	mux.Handle("GET /api/nodes/{node_id}/processes", handler(handleNodeProcesses))
	mux.Handle("GET /api/nodes/{node_id}/checks", handler(handleNodeChecks))
	mux.Handle("GET /api/nodes/{node_id}/ports", handler(handleNodePorts))
	mux.Handle("POST /api/nodes/{node_id}/processes/{pid}/kill", handler(handleNodeKillProcess))

	// App health checks
	mux.Handle("GET /api/checks", handler(handleListChecks))
	mux.Handle("POST /api/checks", handler(handleCreateCheck))
	mux.Handle("PATCH /api/checks/{check_id}", handler(handleUpdateCheck))
	mux.Handle("DELETE /api/checks/{check_id}", handler(handleDeleteCheck))
	mux.Handle("POST /api/checks/{check_id}/run", handler(handleRunCheck))
	mux.Handle("GET /api/notify", handler(handleNotifyStatus))
	mux.Handle("POST /api/notify/test", handler(handleNotifyTest))

	// Managed systemd services
	mux.Handle("GET /api/services", handler(handleListServices))
	mux.Handle("GET /api/services/{service_id}/logs", handler(handleServiceLogs))
	mux.Handle("POST /api/services/{service_id}/action/{action}", handler(handleServiceAction))

	// 9router usage (request log from the API router's dashboard)
	mux.Handle("GET /api/router9/usage", handler(handleRouter9Usage))

	// Websockets
	mux.HandleFunc("/ws/ssh/{device_id}/{session_id}", handleSSHWS)
	mux.HandleFunc("/ws/android/{device_id}", handleAndroidWS)
	mux.HandleFunc("/ws/logs/{service_id}", handleLogsWS)

	// Frontend
	mux.HandleFunc("GET /android.html", func(w http.ResponseWriter, r *http.Request) {
		serveNoStore(w, r, "android.html", "")
	})
	mux.HandleFunc("GET /terminal.html", func(w http.ResponseWriter, r *http.Request) {
		serveNoStore(w, r, "terminal.html", "")
	})
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		serveNoStore(w, r, "sw.js", "application/javascript")
	})
	mux.HandleFunc("/", handleIndex)

	return withCORS(mux)
}

func main() {
	initConfig()
	initStatic()
	devices.load()
	loadOverrides()
	checks.load()
	detectPVEDevice()

	go collectMetrics()
	go pollChecks()
	go adbStartServer()
	if tempDevice != "" {
		go pollRemoteTemps()
	}

	addr := ":" + strconv.Itoa(apiPort)
	srv := &http.Server{
		Addr:    addr,
		Handler: routes(),
		// No write timeout: the terminal, the log tail and the Android stream
		// are all long-lived connections that a deadline would cut off
		// mid-session.
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("Monitor API listening on http://0.0.0.0%s\n", addr)
	fmt.Printf("  data dir: %s\n  frontend: %s\n", baseDir, frontendDir)
	log.Fatal(srv.ListenAndServe())
}
