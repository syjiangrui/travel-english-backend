package ws

import (
	"log"
	"net/http"

	"travel-english-backend/config"

	"github.com/gorilla/websocket"
)

// upgrader allows all origins so the Flutter client can connect from any host.
// In production, consider restricting CheckOrigin for security.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// NewHandler returns an http.HandlerFunc that upgrades HTTP connections to
// WebSocket and runs a per-connection read loop. Each connection gets its own
// Session that persists for the lifetime of the WebSocket.
//
// Text frames are dispatched to Session.HandleMessage (JSON control messages),
// and binary frames are dispatched to Session.HandleBinary (PCM audio chunks).
func NewHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[WS] upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		session := NewSession(conn, cfg)
		log.Printf("[WS] new connection from %s", r.RemoteAddr)

		// Blocking read loop — one goroutine per connection.
		// Returns when the client disconnects or a read error occurs.
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("[WS] read error: %v", err)
				}
				return
			}

			switch msgType {
			case websocket.TextMessage:
				session.HandleMessage(data)
			case websocket.BinaryMessage:
				session.HandleBinary(data)
			}
		}
	}
}

// Handler is a backward-compatible handler with nil config (mock mode).
// Useful for tests that don't require live API keys.
func Handler(w http.ResponseWriter, r *http.Request) {
	NewHandler(nil)(w, r)
}
