package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Outbound notifications for health-check state changes.
//
// A dashboard only tells you something is broken while you happen to be
// looking at it, which is the one moment you probably already know. This is
// the other half: the check that goes down while the tab is closed.
//
// One URL configures the whole thing (NOTIFY_WEBHOOK). Its shape decides the
// payload — Discord, ntfy, or a plain JSON POST — so switching services later
// is a config change rather than a code change.

const (
	// Alert only after this many consecutive failures. A single missed poll is
	// usually a blip: a restart, a slow moment, a dropped packet. Two in a row
	// at 30s spacing is a minute of being genuinely unreachable.
	notifyFailThreshold = 2

	// Above this many transitions in one cycle, send one summary instead of a
	// flood. Losing the network makes every check fail at once, and twenty
	// separate messages is worse than one that says twenty.
	notifyBurstLimit = 3

	notifyTimeout = 10 * time.Second
)

var notifyURL string // NOTIFY_WEBHOOK

// Shared secret for signing the POST. Receivers that accept webhooks from
// anywhere on the network — Hermes, for one — reject an unsigned request, and
// a secret is the only thing distinguishing us from anything else that can
// reach the port. Empty sends unsigned, which is fine for Discord and ntfy:
// there the URL is itself the credential.
var notifySecret string // NOTIFY_SECRET

var notifyClient = &http.Client{Timeout: notifyTimeout}

// alerted maps a check with an unacknowledged "down" notification to the
// moment it went down, so a node that stays down produces one message rather
// than one every 30 seconds — and so the recovery message can say how long it
// was gone.
//
// The timestamp has to be captured here rather than read back on recovery:
// record() updates the check's changed-at to the RECOVERY time before
// transitions are evaluated, so by then the original down time is gone.
//
// In memory only: after a restart, anything still down alerts once more. That
// is a deliberate trade — persisting it would mean a store this app does not
// have, and "still down after a deploy" is arguably worth saying anyway.
var alerted = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

type transition struct {
	check   Check
	down    bool
	result  CheckResult
	downFor time.Duration // how long it was down, on recovery
}

// evaluateTransitions decides what is worth telling anyone about, given the
// results of one poll cycle. Separated from sending so the decision is
// testable without a webhook.
func evaluateTransitions(states []CheckState) []transition {
	var out []transition

	alerted.Lock()
	defer alerted.Unlock()

	for _, st := range states {
		if !st.Enabled || st.Last == nil {
			continue
		}
		res := *st.Last
		isDown := !res.OK

		_, isAlerted := alerted.m[st.ID]

		switch {
		case isDown && st.FailStreak >= notifyFailThreshold && !isAlerted:
			// ChangedAt still points at the first failure at this moment, which
			// is a truer "down since" than now (two polls have already passed).
			downSince := res.At
			if st.ChangedAt != nil {
				downSince = *st.ChangedAt
			}
			alerted.m[st.ID] = downSince
			out = append(out, transition{check: st.Check, down: true, result: res})

		case !isDown && isAlerted:
			t := transition{check: st.Check, down: false, result: res}
			t.downFor = res.At.Sub(alerted.m[st.ID])
			delete(alerted.m, st.ID)
			out = append(out, t)
		}
	}
	return out
}

// notifyTransitions posts whatever the cycle turned up. Never blocks the
// poller for longer than the HTTP timeout, and a failed send is logged rather
// than retried — the next state change will say the same thing anyway.
func notifyTransitions(list []transition) {
	if notifyURL == "" || len(list) == 0 {
		return
	}
	if len(list) > notifyBurstLimit {
		down := 0
		for _, t := range list {
			if t.down {
				down++
			}
		}
		title := fmt.Sprintf("%d check berubah status", len(list))
		body := fmt.Sprintf("%d down, %d pulih. Buka dashboard buat detailnya.",
			down, len(list)-down)
		logSendErr(send(title, body, down > 0))
		return
	}
	for _, t := range list {
		title, body := describe(t)
		logSendErr(send(title, body, t.down))
	}
}

func logSendErr(err error) {
	if err != nil {
		fmt.Printf("⚠️  Gagal kirim notifikasi: %v\n", err)
	}
}

func describe(t transition) (title, body string) {
	if t.down {
		reason := t.result.Error
		if reason == "" {
			reason = fmt.Sprintf("status %d", t.result.Status)
		}
		return "🔴 " + t.check.Label + " down", fmt.Sprintf("%s\n%s", t.check.URL, reason)
	}
	recovered := "✅ " + t.check.Label + " pulih"
	detail := t.check.URL
	if t.downFor > 0 {
		detail += fmt.Sprintf("\nSempat down %s", humanDuration(t.downFor))
	}
	return recovered, detail
}

// asciiOnly drops anything a latin-1 header cannot carry, and tidies up the
// space the removed emoji leaves behind.
func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func humanDuration(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d detik", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d menit", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d jam", int(d.Hours()))
	}
	return fmt.Sprintf("%d hari", int(d.Hours()/24))
}

// ── Transports ──────────────────────────────────────────────────────────────

// send picks a payload shape from the URL. Nothing about the URL is ever
// logged or returned: a webhook URL is a credential.
//
// The error is returned rather than only printed so the "test notification"
// button can say what went wrong. Silently reporting success while the POST
// failed is worse than useless when you are trying to get a relay talking.
func send(title, body string, bad bool) error {
	var (
		payload     []byte
		contentType = "application/json"
		headers     = map[string]string{}
	)

	switch {
	case strings.Contains(notifyURL, "discord.com/api/webhooks"),
		strings.Contains(notifyURL, "discordapp.com/api/webhooks"):
		colour := 0x4eb85f // ok green
		if bad {
			colour = 0xe05a55 // destructive red
		}
		payload, _ = json.Marshal(map[string]any{
			"username": "Orbit",
			"embeds": []map[string]any{{
				"title":       title,
				"description": body,
				"color":       colour,
				"timestamp":   time.Now().Format(time.RFC3339),
			}},
		})

	case strings.Contains(notifyURL, "ntfy.sh"), strings.Contains(notifyURL, "/ntfy"):
		// ntfy takes the message as the raw body, everything else as headers.
		payload = []byte(body)
		contentType = "text/plain"
		// HTTP headers are latin-1, so the emoji these titles start with comes
		// out mangled ("ð Tes…"). ntfy's own idiom is Tags for the icon and a
		// plain Title, which is what this does.
		headers["Title"] = asciiOnly(title)
		headers["Tags"] = "white_check_mark"
		if bad {
			headers["Tags"] = "rotating_light"
			headers["Priority"] = "high"
		}

	default:
		payload, _ = json.Marshal(map[string]any{
			"title": title,
			"body":  body,
			"level": map[bool]string{true: "error", false: "ok"}[bad],
			"at":    time.Now().Format(time.RFC3339),
		})
	}

	req, err := http.NewRequest(http.MethodPost, notifyURL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("NOTIFY_WEBHOOK bukan URL yang valid")
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Driven by the secret alone, deliberately independent of which payload
	// shape was picked above: signing is about who is allowed to post here, not
	// about what the message looks like.
	if notifySecret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-Webhook-Timestamp", ts)
		req.Header.Set("X-Webhook-Signature-V2", webhookSignature(notifySecret, ts, payload))
	}

	resp, err := notifyClient.Do(req)
	if err != nil {
		// http.Client wraps every failure in *url.Error, whose message embeds
		// the full URL — and this text now reaches the dashboard as well as the
		// log. Only the inner cause is kept, so the credential stays out of both.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("webhook nggak bisa dihubungi: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// 401/403 here almost always means the signature was rejected: either
		// NOTIFY_SECRET disagrees with the receiver's, or nothing is signed at all.
		return fmt.Errorf("webhook balas %d", resp.StatusCode)
	}
	return nil
}

// webhookSignature implements the "generic V2" scheme: a hex HMAC-SHA256 over
// "<unix seconds>.<raw body>". Binding the timestamp into the signed material
// is what makes it replay-resistant — the receiver refuses a timestamp more
// than a few minutes off its own clock, and the timestamp cannot be edited
// without invalidating the digest.
//
// Pure, so the wire format can be pinned by a test rather than by standing up
// a receiver and hoping.
func webhookSignature(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// ── HTTP ────────────────────────────────────────────────────────────────────

func handleNotifyStatus(w http.ResponseWriter, r *http.Request) error {
	// The URL itself is never returned — only whether one is set, and enough
	// of its shape to confirm the right service is configured.
	kind := "generic"
	switch {
	case notifyURL == "":
		kind = ""
	case strings.Contains(notifyURL, "discord"):
		kind = "discord"
	case strings.Contains(notifyURL, "ntfy"):
		kind = "ntfy"
	}
	writeJSON(w, 200, map[string]any{
		"configured": notifyURL != "",
		"kind":       kind,
		"threshold":  notifyFailThreshold,
	})
	return nil
}

func handleNotifyTest(w http.ResponseWriter, r *http.Request) error {
	if notifyURL == "" {
		return errf(409, "NOTIFY_WEBHOOK belum diisi di .env")
	}
	if err := send("🔔 Tes notifikasi Orbit",
		"Kalau ini nyampe, notifikasi check bakal nyampe juga.", false); err != nil {
		return errf(502, err.Error())
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
