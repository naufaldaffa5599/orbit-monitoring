package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func adbSerial(host string, port int) string { return host + ":" + strconv.Itoa(port) }

func adbCmd(host string, port int, timeout time.Duration, args ...string) (int, string, string) {
	full := append([]string{"-s", adbSerial(host, port)}, args...)
	return runCmd(timeout, adbBin, full...)
}

// adbStartServer boots the adb daemon once, detached from our pipes.
//
// The daemon inherits stdout/stderr from whichever adb client spawns it, so if
// the first command we run is a plain `adb connect`, its pipes never close and
// runCmd times out even though the client already finished.
func adbStartServer() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, adbBin, "start-server")
	cmd.Stdout, cmd.Stderr = nil, nil
	_ = cmd.Run()
}

func ensureADBConnected(host string, port int) bool {
	adbStartServer()
	// `adb connect` exits 0 even when the target refuses, so the only reliable
	// signal is the message itself ("connected to" / "already connected to").
	rc, out, errOut := runCmd(15*time.Second, adbBin, "connect", adbSerial(host, port))
	if rc != 0 {
		return false
	}
	return strings.Contains(strings.ToLower(out+errOut), "connected to")
}

// adbWake wakes the display and waits until it actually reports ON.
//
// screenrecord aborts with INVALID_LAYER_STACK if the display is off, and the
// panel needs a moment after the wake keyevent before it is really on, so
// polling beats a fixed sleep here.
func adbWake(ctx context.Context, host string, port int, timeout time.Duration) bool {
	adbCmd(host, port, 5*time.Second, "shell", "input", "keyevent", "224")

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		rc, out, _ := adbCmd(host, port, 5*time.Second, "shell", "dumpsys", "display")
		if rc == 0 && strings.Contains(out, "mScreenState=ON") {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(400 * time.Millisecond):
		}
	}
	return false
}

// adbScreenrecord starts screenrecord piping a raw H.264 Annex-B stream to
// stdout.
func adbScreenrecord(ctx context.Context, host string, port int, size, bitrate string) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, adbBin, "-s", adbSerial(host, port), "exec-out",
		"screenrecord", "--output-format=h264", "--time-limit", "0",
		"--bit-rate", bitrate, "--size", size, "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, stdout, nil
}

func adbScreencapPNG(host string, port int) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, adbBin, "-s", adbSerial(host, port), "exec-out", "screencap", "-p")
	out, err := cmd.Output()
	if err != nil || len(out) < 100 {
		return nil
	}
	return out
}

var wmSizeRe = regexp.MustCompile(`(\d+)x(\d+)`)

func adbScreenSize(host string, port int) (int, int) {
	rc, out, _ := adbCmd(host, port, 10*time.Second, "shell", "wm", "size")
	if rc == 0 {
		if m := wmSizeRe.FindStringSubmatch(out); m != nil {
			w, _ := strconv.Atoi(m[1])
			h, _ := strconv.Atoi(m[2])
			return w, h
		}
	}
	return 1080, 1920
}

var (
	sizeRe    = regexp.MustCompile(`^\d{3,5}x\d{3,5}$`)
	bitrateRe = regexp.MustCompile(`^(\d{1,3}M|\d{4,9})$`)
)

type androidInput struct {
	Type     string `json:"type"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	X1       int    `json:"x1"`
	Y1       int    `json:"y1"`
	X2       int    `json:"x2"`
	Y2       int    `json:"y2"`
	Duration *int   `json:"duration"`
	Code     int    `json:"code"`
	Text     string `json:"text"`
}

func handleAndroidWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &wsClient{conn: conn}
	defer conn.Close()

	d := devices.get(r.PathValue("device_id"))
	if d == nil || d.Protocol != "android" {
		client.close(4404, "device not found")
		return
	}

	host, port := d.Host, d.adbPort()

	if !ensureADBConnected(host, port) {
		_ = client.writeJSON(map[string]any{
			"type": "error", "msg": "Gagal connect ADB ke " + adbSerial(host, port)})
		client.close(4500, "adb connect failed")
		return
	}

	q := r.URL.Query()
	mode := q.Get("mode")
	if mode == "" {
		mode = "h264"
	}
	size := q.Get("size")
	if !sizeRe.MatchString(size) {
		size = "720x1560"
	}
	bitrate := q.Get("bitrate")
	if !bitrateRe.MatchString(bitrate) {
		bitrate = "8M"
	}

	width, height := adbScreenSize(host, port)
	if err := client.writeJSON(map[string]any{
		"type": "info", "width": width, "height": height, "mode": mode,
	}); err != nil {
		return
	}

	// The stream goroutine outlives nothing: cancelling this context is what
	// stops screenrecord when the browser goes away.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if mode == "h264" {
		go streamH264(ctx, client, host, port, size, bitrate)
	} else {
		go streamPNG(ctx, client, host, port)
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg androidInput
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		handleAndroidInput(host, port, msg)
	}
}

func handleAndroidInput(host string, port int, msg androidInput) {
	itoa := strconv.Itoa
	switch msg.Type {
	case "tap":
		adbCmd(host, port, 5*time.Second, "shell", "input", "tap", itoa(msg.X), itoa(msg.Y))
	case "swipe":
		dur := 300
		if msg.Duration != nil {
			dur = *msg.Duration
		}
		adbCmd(host, port, 5*time.Second, "shell", "input", "swipe",
			itoa(msg.X1), itoa(msg.Y1), itoa(msg.X2), itoa(msg.Y2), itoa(dur))
	case "keyevent":
		adbCmd(host, port, 5*time.Second, "shell", "input", "keyevent", itoa(msg.Code))
	case "text":
		if msg.Text != "" {
			adbCmd(host, port, 5*time.Second, "shell", "input", "text",
				strings.ReplaceAll(msg.Text, " ", "%s"))
		}
	case "longpress":
		adbCmd(host, port, 5*time.Second, "shell", "input", "swipe",
			itoa(msg.X), itoa(msg.Y), itoa(msg.X), itoa(msg.Y), "600")
	}
}

// streamPNG is the fallback: poll screencap. ~8 fps, but needs no WebCodecs
// support in the browser.
func streamPNG(ctx context.Context, client *wsClient, host string, port int) {
	for ctx.Err() == nil {
		png := adbScreencapPNG(host, port)
		if png != nil {
			if err := client.writeBinary(png); err != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(120 * time.Millisecond):
		}
	}
}

// streamH264 streams the device's hardware H.264 encoder straight to the
// browser.
//
// screenrecord dies whenever the display turns off, so each pass wakes the
// screen first and the loop simply starts a new one — the client resets its
// decoder on the stream_restart notice because a fresh process emits new
// SPS/PPS and an IDR.
func streamH264(ctx context.Context, client *wsClient, host string, port int, size, bitrate string) {
	firstPass := true
	buf := make([]byte, 65536)

	for ctx.Err() == nil {
		if !adbWake(ctx, host, port, 6*time.Second) {
			if ctx.Err() == nil {
				_ = client.writeJSON(map[string]any{
					"type": "error", "msg": "Layar HP tidak bisa dinyalakan"})
			}
			return
		}
		if !firstPass {
			if err := client.writeJSON(map[string]any{"type": "stream_restart"}); err != nil {
				return
			}
		}
		firstPass = false

		cmd, stdout, err := adbScreenrecord(ctx, host, port, size, bitrate)
		if err != nil {
			return
		}

		// screenrecord reports failures as plain text on *stdout*, so the first
		// chunk has to be inspected rather than trusting exit codes. This is a
		// short read on purpose — waiting to fill the buffer would stall the
		// first frame until 64 KB of video had accumulated.
		n, _ := stdout.Read(buf)
		if n == 0 {
			_ = cmd.Wait()
			if ctx.Err() == nil {
				continue
			}
			return
		}
		head := buf[:n]
		if strings.HasPrefix(string(head), "ERROR") {
			msg := strings.TrimSpace(string(head))
			if len(msg) > 200 {
				msg = msg[:200]
			}
			_ = client.writeJSON(map[string]any{"type": "error", "msg": msg})
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return
		}
		if err := client.writeBinary(head); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return
		}

		for ctx.Err() == nil {
			n, err := stdout.Read(buf)
			if n > 0 {
				if werr := client.writeBinary(buf[:n]); werr != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					return
				}
			}
			if err != nil {
				break // screenrecord exited; the outer loop restarts it
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}
