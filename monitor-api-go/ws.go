package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The API is already served with permissive CORS and sits behind the same
// origin as the frontend in every deployment, so origin checking here would
// only break the "open it from your phone by IP" workflow without adding a
// boundary the HTTP side doesn't already have.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 32768,
	CheckOrigin:     func(r *http.Request) bool { return true },
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
