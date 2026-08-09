package main

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Origin is checked against the host being addressed, which is stricter than
// it sounds: the frontend is always served by this same server, so a browser
// tab that legitimately opens a terminal always reports an Origin matching the
// Host it connected to — whether that is an IP, a hostname or a tunnel.
//
// This used to return true unconditionally, on the reasoning that there was no
// boundary to protect. Sessions changed that. Without this check any page on
// the internet could open a socket to /ws/ssh/... in a visitor's browser and
// ride their cookie into a root shell.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 32768,
	CheckOrigin:     sameOriginWS,
}

func sameOriginWS(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	// Non-browser clients (a script, a probe) send no Origin at all. They also
	// carry no ambient cookie, so there is nothing to hijack.
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// wsClient serializes writes to one connection. gorilla/websocket allows only
// one concurrent writer, and terminal output is broadcast from a different
// goroutine than the one handling control messages.
type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *wsClient) writeText(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, []byte(s))
}

func (c *wsClient) writeBinary(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, b)
}

func (c *wsClient) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *wsClient) close(code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = c.conn.Close()
}

// closeWS sends a close frame with an application code, mirroring FastAPI's
// websocket.close(code=...) — the frontend branches on these codes.
func closeWS(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = conn.Close()
}
