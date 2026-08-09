package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	apiPort     = 8585
	wib         = time.FixedZone("WIB", 7*3600)
	baseDir     string
	devicesFile string
	frontendDir string
	homeDir     string

	// Device id whose `sensors -j` feeds the temperature card. Empty means use
	// this machine's own sensors, which on a VM means no reading at all.
	tempDevice string

	// 9router dashboard (see router9.go). Both must be set for the usage node
	// to appear in the tree.
	router9URL  string
	router9Pass string
)

// Binaries are resolved once at startup. exec.LookPath failing is not fatal:
// ping in particular is absent on plenty of minimal installs, and probing is
// documented as best-effort, so callers check for "" instead.
var (
	systemctlBin  = lookPathOr("systemctl", "/usr/bin/systemctl")
	journalctlBin = lookPathOr("journalctl", "/usr/bin/journalctl")
	sudoBin       = lookPathOr("sudo", "/usr/bin/sudo")
	adbBin        string
	pingBin       string
)

func lookPathOr(name, fallback string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return fallback
}

// managedService is the whitelist entry for a systemd unit. The unit name is
// never taken from the request, so this list is the sole gate on which units
// are visible or controllable through the API.
type managedService struct {
	ID    string
	Unit  string
	Label string
	Icon  string
}

// A slice, not a map: the frontend renders these in order, and Go map
// iteration would reshuffle the Services tab on every poll.
var managedServices = []managedService{
	{"monitor-api", "monitor-api.service", "Orbit API", "🛡️"},
	{"poka-server", "poka-server.service", "Poka Server", "📦"},
	{"poka-watch", "poka-watch.service", "Poka Watch (build)", "👀"},
	{"reminder-bot-backend", "reminder-bot-backend.service", "Reminder Bot Backend", "⏰"},
	{"reminder-bot-frontend", "reminder-bot-frontend.service", "Reminder Bot Frontend", "🗓️"},
	{"prd-project", "prd-project.service", "PRD Project", "⚡"},
}

func serviceByID(id string) *managedService {
	for i := range managedServices {
		if managedServices[i].ID == id {
			return &managedServices[i]
		}
	}
	return nil
}

func initConfig() {
	exe, err := os.Executable()
	if err == nil {
		baseDir = filepath.Dir(exe)
	}
	// Running via `go run` puts the binary in a temp dir, so an explicit
	// override is the only reliable way to point at the real data directory
	// during development.
	if d := os.Getenv("MONITOR_DIR"); d != "" {
		baseDir = d
	} else if cwd, err := os.Getwd(); err == nil {
		// Prefer the working directory when it actually holds our data file;
		// systemd sets WorkingDirectory= for exactly this reason.
		if _, err := os.Stat(filepath.Join(cwd, "devices.json")); err == nil {
			baseDir = cwd
		}
	}

	loadDotenv(filepath.Join(baseDir, ".env"))

	if p := os.Getenv("API_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			apiPort = n
		}
	}

	devicesFile = filepath.Join(baseDir, "devices.json")
	frontendDir = filepath.Join(filepath.Dir(baseDir), "monitor-app")
	if d := os.Getenv("FRONTEND_DIR"); d != "" {
		frontendDir = d
	}
	homeDir, _ = os.UserHomeDir()

	tempDevice = os.Getenv("TEMP_DEVICE")

	// Where check state changes are announced. Empty disables notifications.
	notifyURL = os.Getenv("NOTIFY_WEBHOOK")

	router9URL = os.Getenv("ROUTER9_URL")
	router9Pass = os.Getenv("ROUTER9_PASSWORD")

	// Which device is the Proxmox host. Its guests are auto-discovered and hung
	// under it in the resource tree; everything else lands beside them. Unset
	// and undetectable means no hypervisor branch, just a flat list of machines.
	pveDeviceID = os.Getenv("PVE_DEVICE")

	adbBin = os.Getenv("ADB_BIN")
	if adbBin == "" {
		adbBin = lookPathOr("adb", "adb")
	}
	if p, err := exec.LookPath("ping"); err == nil {
		pingBin = p
	}
}

// loadDotenv reads KEY=VALUE lines into the environment without overwriting
// anything already set, matching python-dotenv's default and letting systemd's
// EnvironmentFile= win when both are present.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

// nowWIB formats a timestamp the way the frontend's chart labels expect.
func nowWIB() string {
	return time.Now().In(wib).Format("2006-01-02T15:04:05.000000-07:00")
}
