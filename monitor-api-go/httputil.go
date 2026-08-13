package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"time"
)

// apiError carries an HTTP status alongside the message. It exists so handlers
// can `return` a failure the way the Python code raised HTTPException, instead
// of threading (status, msg) through every return value.
type apiError struct {
	Status int
	Detail string
}

func (e *apiError) Error() string { return e.Detail }

func errf(status int, detail string) *apiError {
	return &apiError{Status: status, Detail: detail}
}

// writeJSON mirrors FastAPI's default response encoding.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr emits {"detail": "..."} — the shape the frontend already reads
// (`err?.detail`), inherited from FastAPI's HTTPException.
func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, map[string]string{"detail": ae.Detail})
		return
	}
	writeJSON(w, 500, map[string]string{"detail": err.Error()})
}

// handler is an http.HandlerFunc that may fail. Wrapping it keeps the error
// path in one place rather than repeating the encode-and-return dance.
type handler func(http.ResponseWriter, *http.Request) error

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		writeErr(w, err)
	}
}

func decodeJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return errf(400, "body bukan JSON yang valid: "+err.Error())
	}
	return nil
}

// runCmd runs a command with a deadline, returning (exitCode, stdout, stderr).
// Exit code is -1 on timeout, matching the Python _run_cmd contract that
// several callers branch on.
func runCmd(timeout time.Duration, name string, args ...string) (int, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr []byte
	stdoutPipe, stderrPipe := &byteBuf{}, &byteBuf{}
	cmd.Stdout, cmd.Stderr = stdoutPipe, stderrPipe

	err := cmd.Run()
	stdout, stderr = stdoutPipe.Bytes(), stderrPipe.Bytes()

	if ctx.Err() == context.DeadlineExceeded {
		return -1, "", "timeout"
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			return -1, string(stdout), err.Error()
		}
	}
	return code, string(stdout), string(stderr)
}

// byteBuf is a minimal io.Writer sink; bytes.Buffer would do, but this keeps
// the intent obvious at the call site above.
type byteBuf struct{ b []byte }

func (b *byteBuf) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *byteBuf) Bytes() []byte { return b.b }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round1(v float64) float64 { return float64(int64(v*10+copysign(0.5, v))) / 10 }
func round2(v float64) float64 { return float64(int64(v*100+copysign(0.5, v))) / 100 }

func copysign(mag, sign float64) float64 {
	if sign < 0 {
		return -mag
	}
	return mag
}
