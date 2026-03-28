package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"travel-english-backend/ws"

	"github.com/gorilla/websocket"
)

// helper: start test server and return ws URL
func setupServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ws.Handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	server := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	return server, wsURL
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	return conn
}

type serverMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text,omitempty"`
	IsFinal   *bool  `json:"is_final,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func readJSON(t *testing.T, conn *websocket.Conn) serverMsg {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("expected text frame, got type %d", msgType)
	}
	var msg serverMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	return msg
}

func readBinary(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary frame, got type %d", msgType)
	}
	return data
}

func sendJSON(t *testing.T, conn *websocket.Conn, v interface{}) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write error: %v", err)
	}
}

func startSession(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	sendJSON(t, conn, map[string]interface{}{"type": "session.start"})
	msg := readJSON(t, conn)
	if msg.Type != "session.started" {
		t.Fatalf("expected session.started, got %s", msg.Type)
	}
	if msg.SessionID == "" {
		t.Fatal("session_id is empty")
	}
	return msg.SessionID
}

// ---------- Tests ----------

func TestHealthEndpoint(t *testing.T) {
	server, _ := setupServer(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSessionLifecycle(t *testing.T) {
	server, wsURL := setupServer(t)
	defer server.Close()
	conn := dial(t, wsURL)
	defer conn.Close()

	sessionID := startSession(t, conn)
	t.Logf("session_id: %s", sessionID)

	sendJSON(t, conn, map[string]string{"type": "session.end"})
	msg := readJSON(t, conn)
	if msg.Type != "session.ended" {
		t.Fatalf("expected session.ended, got %s", msg.Type)
	}
}

func TestAudioEndMockPipeline(t *testing.T) {
	server, wsURL := setupServer(t)
	defer server.Close()
	conn := dial(t, wsURL)
	defer conn.Close()

	startSession(t, conn)

	// Send a few binary audio chunks
	for i := 0; i < 3; i++ {
		err := conn.WriteMessage(websocket.BinaryMessage, make([]byte, 640))
		if err != nil {
			t.Fatalf("send binary failed: %v", err)
		}
	}

	// Send audio.end
	sendJSON(t, conn, map[string]string{"type": "audio.end"})

	// 1. asr.result
	msg := readJSON(t, conn)
	if msg.Type != "asr.result" {
		t.Fatalf("expected asr.result, got %s", msg.Type)
	}
	if msg.IsFinal == nil || !*msg.IsFinal {
		t.Fatal("asr.result should have is_final=true")
	}
	if msg.Text == "" {
		t.Fatal("asr.result text is empty")
	}

	// 2. Multiple chat.delta
	deltaCount := 0
	for {
		msg = readJSON(t, conn)
		if msg.Type == "chat.done" {
			break
		}
		if msg.Type != "chat.delta" {
			t.Fatalf("expected chat.delta or chat.done, got %s", msg.Type)
		}
		if msg.Text == "" {
			t.Fatal("chat.delta text is empty")
		}
		deltaCount++
	}
	if deltaCount == 0 {
		t.Fatal("expected at least one chat.delta")
	}
	t.Logf("received %d chat.delta messages", deltaCount)

	// 3. tts.start
	msg = readJSON(t, conn)
	if msg.Type != "tts.start" {
		t.Fatalf("expected tts.start, got %s", msg.Type)
	}

	// 4. binary frame
	binData := readBinary(t, conn)
	if len(binData) == 0 {
		t.Fatal("expected non-empty binary TTS data")
	}

	// 5. tts.end
	msg = readJSON(t, conn)
	if msg.Type != "tts.end" {
		t.Fatalf("expected tts.end, got %s", msg.Type)
	}
}

func TestTextQuery(t *testing.T) {
	server, wsURL := setupServer(t)
	defer server.Close()
	conn := dial(t, wsURL)
	defer conn.Close()

	startSession(t, conn)

	sendJSON(t, conn, map[string]string{"type": "text.query", "text": "How do I check in?"})

	// Collect chat.delta tokens
	var tokens []string
	for {
		msg := readJSON(t, conn)
		if msg.Type == "chat.done" {
			break
		}
		if msg.Type != "chat.delta" {
			t.Fatalf("expected chat.delta or chat.done, got %s", msg.Type)
		}
		tokens = append(tokens, msg.Text)
	}
	if len(tokens) == 0 {
		t.Fatal("expected at least one chat.delta")
	}
	combined := strings.Join(tokens, "")
	if !strings.Contains(combined, "How do I check in?") {
		t.Fatalf("response should echo the query text, got: %s", combined)
	}

	// tts.start → binary → tts.end
	msg := readJSON(t, conn)
	if msg.Type != "tts.start" {
		t.Fatalf("expected tts.start, got %s", msg.Type)
	}
	readBinary(t, conn)
	msg = readJSON(t, conn)
	if msg.Type != "tts.end" {
		t.Fatalf("expected tts.end, got %s", msg.Type)
	}
}

func TestTtsSynthesize(t *testing.T) {
	server, wsURL := setupServer(t)
	defer server.Close()
	conn := dial(t, wsURL)
	defer conn.Close()

	startSession(t, conn)

	sendJSON(t, conn, map[string]string{"type": "tts.synthesize", "text": "Welcome!"})

	msg := readJSON(t, conn)
	if msg.Type != "tts.start" {
		t.Fatalf("expected tts.start, got %s", msg.Type)
	}
	binData := readBinary(t, conn)
	if len(binData) != 1024 {
		t.Fatalf("expected 1024 bytes dummy MP3, got %d", len(binData))
	}
	msg = readJSON(t, conn)
	if msg.Type != "tts.end" {
		t.Fatalf("expected tts.end, got %s", msg.Type)
	}
}

func TestConversationHistory(t *testing.T) {
	server, wsURL := setupServer(t)
	defer server.Close()
	conn := dial(t, wsURL)
	defer conn.Close()

	startSession(t, conn)

	sendJSON(t, conn, map[string]interface{}{
		"type": "conversation.history",
		"items": []map[string]string{
			{"role": "user", "text": "Hello"},
			{"role": "assistant", "text": "Hi there!"},
		},
	})

	// conversation.history doesn't send a response, so just verify we can
	// still communicate by ending the session.
	sendJSON(t, conn, map[string]string{"type": "session.end"})
	msg := readJSON(t, conn)
	if msg.Type != "session.ended" {
		t.Fatalf("expected session.ended, got %s", msg.Type)
	}
}
