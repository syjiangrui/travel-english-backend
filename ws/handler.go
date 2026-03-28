package ws

import (
	"log"
	"net/http"

	"travel-english-backend/config"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// NewHandler creates a WebSocket handler with the given config.
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

// Handler is a backward-compatible handler with nil config (uses mock mode).
func Handler(w http.ResponseWriter, r *http.Request) {
	NewHandler(nil)(w, r)
}
