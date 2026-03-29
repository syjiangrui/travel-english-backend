package stt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ============================================================
//  Streaming WebSocket STT (primary)
// ============================================================

// RealtimeSTT holds a live WebSocket connection to ElevenLabs
// real-time speech-to-text endpoint. Audio chunks are forwarded
// as they arrive; partial and committed transcripts are delivered
// via callbacks.
type RealtimeSTT struct {
	APIKey string

	// PreviousText provides conversational context to improve transcription accuracy.
	// Set this to the last assistant response before each new user turn.
	PreviousText string

	// Callbacks – set before calling Connect()
	OnPartial   func(text string) // interim transcript (may change)
	OnCommitted func(text string) // final committed transcript
	OnError     func(err error)

	conn   *websocket.Conn
	mu     sync.Mutex // protects conn writes
	done   chan struct{}
	closed bool
}

// sttServerMsg is the union of possible server messages.
// ElevenLabs may return "error" as a string or object, so we use json.RawMessage.
type sttServerMsg struct {
	MessageType string          `json:"message_type"`
	Text        string          `json:"text,omitempty"`
	RawError    json.RawMessage `json:"error,omitempty"`
}

func (m *sttServerMsg) errorString() string {
	if len(m.RawError) == 0 {
		return ""
	}
	// Try string first
	var s string
	if json.Unmarshal(m.RawError, &s) == nil && s != "" {
		return s
	}
	// Try object
	var obj struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(m.RawError, &obj) == nil && obj.Message != "" {
		return fmt.Sprintf("[%s] %s", obj.Type, obj.Message)
	}
	return string(m.RawError)
}

// Connect opens the ElevenLabs realtime STT WebSocket.
// language can be "" for auto-detect or an ISO 639-1 code like "en".
func (r *RealtimeSTT) Connect(ctx context.Context, language string) error {
	url := "wss://api.elevenlabs.io/v1/speech-to-text/realtime?model_id=scribe_v2_realtime&audio_format=pcm_16000&commit_strategy=manual&enable_logging=false" +
		"&include_language_detection=true" +
		"&vad_threshold=0.5" +
		"&min_speech_duration_ms=200" +
		"&min_silence_duration_ms=200"
	if language != "" {
		url += "&language_code=" + language
	}

	header := http.Header{}
	header.Set("xi-api-key", r.APIKey)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return fmt.Errorf("STT WS dial: %w", err)
	}
	r.conn = conn
	r.done = make(chan struct{})
	r.closed = false

	// Read pump – delivers events to callbacks
	go r.readPump()

	// Wait for session_started before returning
	// Give it a reasonable timeout
	time.Sleep(500 * time.Millisecond)
	log.Printf("[STT] WebSocket connected")
	return nil
}

// SendAudio forwards a raw PCM chunk (16 kHz 16-bit mono) to ElevenLabs.
// It base64-encodes the chunk and sends as input_audio_chunk JSON.
func (r *RealtimeSTT) SendAudio(pcm []byte) error {
	if r.conn == nil || r.closed {
		return fmt.Errorf("STT not connected")
	}
	msg := map[string]interface{}{
		"message_type":  "input_audio_chunk",
		"audio_base_64": base64.StdEncoding.EncodeToString(pcm),
	}
	if r.PreviousText != "" {
		msg["previous_text"] = r.PreviousText
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn.WriteJSON(msg)
}

// Commit tells ElevenLabs to finalize the current utterance.
// The server will respond with committed_transcript.
func (r *RealtimeSTT) Commit() error {
	if r.conn == nil || r.closed {
		return fmt.Errorf("STT not connected")
	}
	// Send a commit by setting commit=true on a zero-length audio chunk
	msg := map[string]interface{}{
		"message_type": "input_audio_chunk",
		"audio_base_64": "",
		"commit":        true,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn.WriteJSON(msg)
}

// IsConnected returns true if the WebSocket is connected and not closed.
func (r *RealtimeSTT) IsConnected() bool {
	return r.conn != nil && !r.closed
}

// Close gracefully shuts down the WebSocket connection.
func (r *RealtimeSTT) Close() {
	if r.conn == nil || r.closed {
		return
	}
	r.closed = true
	r.mu.Lock()
	_ = r.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
	r.mu.Unlock()
	_ = r.conn.Close()
	<-r.done // wait for readPump to exit
	log.Printf("[STT] WebSocket closed")
}

func (r *RealtimeSTT) readPump() {
	defer close(r.done)
	for {
		_, data, err := r.conn.ReadMessage()
		if err != nil {
			if !r.closed {
				r.closed = true // prevent further sends
				if r.OnError != nil {
					r.OnError(fmt.Errorf("STT read: %w", err))
				}
			}
			return
		}

		log.Printf("[STT] raw msg: %s", string(data))

		var msg sttServerMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[STT] unmarshal error: %v | raw: %s", err, string(data))
			continue
		}

		switch msg.MessageType {
		case "session_started":
			log.Printf("[STT] session started")
		case "partial_transcript":
			if msg.Text != "" && r.OnPartial != nil {
				r.OnPartial(msg.Text)
			}
		case "committed_transcript", "committed_transcript_with_timestamps":
			if r.OnCommitted != nil {
				r.OnCommitted(msg.Text)
			}
		default:
			// Check for error
			if errStr := msg.errorString(); errStr != "" {
				log.Printf("[STT] server error: %s", errStr)
				if r.OnError != nil {
					r.OnError(fmt.Errorf("STT server: %s", errStr))
				}
			} else {
				log.Printf("[STT] unknown message_type: %s", msg.MessageType)
			}
		}
	}
}

// ============================================================
//  Batch REST STT (fallback / kept for simple use cases)
// ============================================================

// ElevenLabsSTT handles batch speech-to-text via ElevenLabs REST API.
type ElevenLabsSTT struct {
	APIKey  string
	BaseURL string // default "https://api.elevenlabs.io/v1"
}

// Transcribe converts raw PCM audio (16kHz, 16-bit, mono) to text.
func (s *ElevenLabsSTT) Transcribe(ctx context.Context, pcmAudio []byte) (string, error) {
	if len(pcmAudio) == 0 {
		return "", fmt.Errorf("empty audio data")
	}

	baseURL := s.BaseURL
	if baseURL == "" {
		baseURL = "https://api.elevenlabs.io/v1"
	}

	wavData := addWAVHeader(pcmAudio, 16000, 16, 1)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(wavData); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
	}
	_ = writer.WriteField("model_id", "scribe_v1")
	writer.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/speech-to-text", body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("xi-api-key", s.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("STT request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("STT API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.Text, nil
}

// addWAVHeader prepends a standard 44-byte RIFF/WAV header to raw PCM data.
func addWAVHeader(pcmData []byte, sampleRate, bitsPerSample, channels int) []byte {
	dataSize := len(pcmData)
	fileSize := 36 + dataSize
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, uint32(fileSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, uint32(16))
	binary.Write(buf, binary.LittleEndian, uint16(1))
	binary.Write(buf, binary.LittleEndian, uint16(channels))
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(buf, binary.LittleEndian, uint32(byteRate))
	binary.Write(buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, uint32(dataSize))
	buf.Write(pcmData)
	return buf.Bytes()
}
