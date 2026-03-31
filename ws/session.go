package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"travel-english-backend/config"
	"travel-english-backend/llm"
	"travel-english-backend/stt"
	"travel-english-backend/tts"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Session represents a single WebSocket conversation session. It owns the
// connection, manages STT/LLM/TTS lifecycle, and maintains conversation context.
// All writes to the WebSocket are serialized through mu to prevent interleaving.
type Session struct {
	ID         string
	conn       *websocket.Conn
	cfg        *config.Config
	mu         sync.Mutex     // guards all conn writes (sendJSON/sendBinary)
	sessionCfg *SessionConfig // client-provided session configuration
	ctx        *llm.ContextManager

	// Streaming STT state
	sttConn     *stt.RealtimeSTT // nil when not connected or in batch mode
	sttReady    bool             // false after first send error to suppress repeated logging
	audioBuffer [][]byte         // accumulates PCM chunks for batch fallback or mock mode
}

// NewSession creates a session bound to the given WebSocket connection.
// The session starts in an uninitialized state — call handleSessionStart to assign an ID.
func NewSession(conn *websocket.Conn, cfg *config.Config) *Session {
	return &Session{
		conn:        conn,
		cfg:         cfg,
		audioBuffer: make([][]byte, 0),
	}
}

// isLive returns true when API keys are configured for at least one STT provider
// and the LLM service, meaning the session can use real STT/LLM/TTS services.
func (s *Session) isLive() bool {
	if s.cfg == nil || s.cfg.OpenRouterKey == "" {
		return false
	}
	// At least one STT provider must have a key
	return s.cfg.ElevenLabsKey != "" || s.cfg.DeepInfraKey != ""
}

// isBatchSTT returns true when the client requested batch (non-streaming) STT mode.
// In batch mode, all audio is buffered locally and sent to the REST API at audio.end.
func (s *Session) isBatchSTT() bool {
	return s.sessionCfg != nil && s.sessionCfg.STTMode == "batch"
}

// sendJSON marshals v as JSON and writes it as a text frame.
// Thread-safe: serialized by mu to prevent frame interleaving from concurrent goroutines.
func (s *Session) sendJSON(v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteJSON(v)
}

// sendBinary writes raw bytes as a binary frame. Thread-safe via mu.
func (s *Session) sendBinary(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, data)
}

// logf logs a formatted message with the session ID prefix for correlation.
func (s *Session) logf(format string, args ...interface{}) {
	prefix := fmt.Sprintf("[WS] session %s: ", s.ID)
	log.Printf(prefix+format, args...)
}

// HandleMessage parses a JSON text frame and dispatches to the appropriate handler.
// Long-running handlers (audio.end, text.query, tts.synthesize) run in separate
// goroutines so the read loop is not blocked.
func (s *Session) HandleMessage(raw []byte) {
	var msg ClientMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.logf("invalid JSON: %v", err)
		_ = s.sendJSON(ServerMessage{Type: "error", Code: "invalid_json", Message: err.Error()})
		return
	}

	switch msg.Type {
	case "session.start":
		s.handleSessionStart(msg)
	case "session.update":
		s.handleSessionUpdate(msg)
	case "audio.end":
		go s.handleAudioEnd()
	case "text.query":
		go s.handleTextQuery(msg.Text)
	case "tts.synthesize":
		go s.handleTTSSynthesize(msg.Text)
	case "conversation.history":
		s.handleConversationHistory(msg.Items)
	case "session.end":
		s.handleSessionEnd()
	default:
		s.logf("unknown message type: %s", msg.Type)
	}
}

// HandleBinary processes an incoming PCM audio chunk (16 kHz, 16-bit, mono).
//
// Audio is always buffered locally for batch STT fallback. In realtime mode,
// chunks are also forwarded to the ElevenLabs streaming STT WebSocket.
// After the first forward error, further errors are suppressed to avoid log spam.
func (s *Session) HandleBinary(data []byte) {
	// Always buffer audio for batch mode or as fallback
	s.audioBuffer = append(s.audioBuffer, data)

	// In realtime mode, also forward to streaming STT
	if !s.isBatchSTT() && s.isLive() && s.sttConn != nil && s.sttConn.IsConnected() {
		if err := s.sttConn.SendAudio(data); err != nil {
			// Only log once, not per-chunk
			if s.sttReady {
				s.logf("STT forward error (stopping): %v", err)
				s.sttReady = false
			}
		}
	}
}

// --- handlers ---

// handleSessionStart initializes the session: assigns an ID, stores config,
// creates the LLM context manager, and optionally opens a streaming STT connection.
func (s *Session) handleSessionStart(msg ClientMessage) {
	if msg.SessionID != "" {
		s.ID = msg.SessionID
	} else {
		s.ID = uuid.New().String()
	}
	s.sessionCfg = msg.Config

	// Initialize context manager
	systemRole := ""
	if s.sessionCfg != nil {
		systemRole = s.sessionCfg.SystemRole
	}
	s.ctx = llm.NewContextManager(systemRole)
	s.logf("session.start config: systemRole=%q (len=%d)", systemRole, len(systemRole))

	// Connect streaming STT if live and not batch mode
	if s.isLive() && !s.isBatchSTT() {
		s.connectSTT()
	}

	s.logf("session started (live=%v, stt=%v, sttMode=%s, sttProvider=%s)", s.isLive(), s.sttReady, func() string {
		if s.isBatchSTT() {
			return "batch"
		}
		return "realtime"
	}(), func() string {
		if s.sessionCfg != nil && s.sessionCfg.STTProvider != "" {
			return s.sessionCfg.STTProvider
		}
		if s.cfg != nil {
			return s.cfg.STTProvider
		}
		return "elevenlabs"
	}())
	_ = s.sendJSON(ServerMessage{Type: "session.started", SessionID: s.ID})
}

// handleSessionUpdate updates session configuration mid-session without
// requiring a full reconnect. Currently supports updating the system_role
// (e.g., after memory extraction adds new memories to the system prompt).
func (s *Session) handleSessionUpdate(msg ClientMessage) {
	if s.ctx == nil {
		s.logf("session.update: no active session, ignoring")
		return
	}
	if msg.Config == nil {
		s.logf("session.update: no config provided, ignoring")
		return
	}
	if msg.Config.SystemRole != "" {
		old := s.ctx.SystemRole
		s.ctx.SystemRole = msg.Config.SystemRole
		s.logf("session.update: systemRole updated (old=%d chars, new=%d chars)", len(old), len(msg.Config.SystemRole))
	}
}

// connectSTT opens the ElevenLabs realtime STT WebSocket.
func (s *Session) connectSTT() {
	r := &stt.RealtimeSTT{
		APIKey: s.cfg.ElevenLabsKey,
	}

	// Partial transcripts → forward to client as interim ASR
	r.OnPartial = func(text string) {
		_ = s.sendJSON(ServerMessage{Type: "asr.result", Text: text, IsFinal: BoolPtr(false)})
	}

	// Committed transcript → will be picked up by handleAudioEnd via channel
	// We use a channel to bridge the async callback to the sync handleAudioEnd flow
	r.OnCommitted = func(text string) {
		// This will be set per-turn, see handleAudioEnd
	}

	r.OnError = func(err error) {
		s.logf("STT error: %v", err)
	}

	lang := "" // 默认不指定，由 Scribe v2 自动检测语言（支持中英混说）
	if s.sessionCfg != nil && s.sessionCfg.STTLanguage != "" {
		lang = s.sessionCfg.STTLanguage
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := r.Connect(ctx, lang); err != nil {
		s.logf("STT connect failed: %v", err)
		return
	}

	s.sttConn = r
	s.sttReady = true
}

// handleAudioEnd processes the end of a user's audio recording. It follows a
// tiered strategy:
//  1. Realtime STT: commit the streaming transcript (preferred, lowest latency)
//  2. Batch STT fallback: send buffered PCM to the REST API (used when realtime
//     fails, times out, or when batch mode is explicitly requested)
//  3. Mock mode: return canned responses for testing without API keys
//
// After obtaining the transcript, it feeds it to the LLM → TTS pipeline.
func (s *Session) handleAudioEnd() {
	if !s.isLive() {
		// Mock mode: use buffered audio
		s.audioBuffer = s.audioBuffer[:0]
		s.mockAudioEnd()
		return
	}

	// Batch STT mode: skip realtime commit, go directly to batch REST API
	if s.isBatchSTT() {
		s.logf("audio.end: batch STT mode, using REST API")
		goto batchFallback
	}

	// If streaming STT is connected, commit and wait for result
	if s.sttConn != nil && s.sttConn.IsConnected() {
		s.logf("audio.end: committing streaming STT")

		// Set up a one-shot channel to bridge the async OnCommitted callback
		// to this synchronous flow. The channel is buffered(1) so the callback
		// never blocks even if we've already timed out.
		committedCh := make(chan string, 1)
		s.sttConn.OnCommitted = func(text string) {
			select {
			case committedCh <- text:
			default:
			}
		}

		if err := s.sttConn.Commit(); err != nil {
			s.logf("STT commit error: %v, falling back to batch", err)
			goto batchFallback
		}

		// Wait for committed transcript
		select {
		case finalText := <-committedCh:
			// Clear buffer — realtime succeeded, no need for batch fallback data
			s.audioBuffer = s.audioBuffer[:0]

			if strings.TrimSpace(finalText) == "" {
				_ = s.sendJSON(ServerMessage{Type: "error", Code: "stt_empty", Message: "No speech detected"})
				return
			}
			s.logf("STT streaming final: %s", finalText)
			_ = s.sendJSON(ServerMessage{Type: "asr.result", Text: finalText, IsFinal: BoolPtr(true)})

			s.ctx.AddUserMessage(finalText)
			s.streamLLMWithTTS(finalText)
			return

		case <-time.After(10 * time.Second):
			s.logf("STT commit timeout, falling back to batch")
			goto batchFallback
		}
	}

batchFallback:
	// Fallback: collect any buffered audio and use batch REST STT
	var pcmData []byte
	for _, chunk := range s.audioBuffer {
		pcmData = append(pcmData, chunk...)
	}
	s.audioBuffer = s.audioBuffer[:0]
	s.logf("audio.end batch fallback: %d bytes", len(pcmData))

	if len(pcmData) == 0 {
		_ = s.sendJSON(ServerMessage{Type: "error", Code: "no_audio", Message: "No audio data received"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Select STT provider: client override > server default > "elevenlabs"
	provider := "elevenlabs"
	if s.cfg != nil && s.cfg.STTProvider != "" {
		provider = s.cfg.STTProvider
	}
	if s.sessionCfg != nil && s.sessionCfg.STTProvider != "" {
		provider = s.sessionCfg.STTProvider
	}

	var text string
	var err error
	switch provider {
	case "deepinfra":
		s.logf("batch STT provider: deepinfra")
		client := &stt.DeepInfraSTT{APIKey: s.cfg.DeepInfraKey}
		text, err = client.Transcribe(ctx, pcmData)
	default: // "elevenlabs"
		s.logf("batch STT provider: elevenlabs")
		client := &stt.ElevenLabsSTT{APIKey: s.cfg.ElevenLabsKey}
		text, err = client.Transcribe(ctx, pcmData)
	}
	if err != nil {
		s.logf("batch STT error: %v", err)
		_ = s.sendJSON(ServerMessage{Type: "error", Code: "stt_failed", Message: err.Error()})
		return
	}
	if strings.TrimSpace(text) == "" {
		_ = s.sendJSON(ServerMessage{Type: "error", Code: "stt_empty", Message: "No speech detected"})
		return
	}

	s.logf("STT batch result: %s", text)
	_ = s.sendJSON(ServerMessage{Type: "asr.result", Text: text, IsFinal: BoolPtr(true)})

	s.ctx.AddUserMessage(text)
	s.streamLLMWithTTS(text)
}

// handleTextQuery processes a direct text input (no STT needed).
// In live mode it feeds the text to LLM → TTS; in mock mode returns canned responses.
func (s *Session) handleTextQuery(text string) {
	s.logf("text.query: %s", text)

	if !s.isLive() {
		s.mockTextQuery(text)
		return
	}

	s.ctx.AddUserMessage(text)
	s.streamLLMWithTTS(text)
}

// handleTTSSynthesize performs standalone text-to-speech without LLM involvement.
// Used by the Flutter client for message replay and welcome greetings.
func (s *Session) handleTTSSynthesize(text string) {
	s.logf("tts.synthesize: %s", text)

	if !s.isLive() {
		s.sendMockTTS()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ttsClient := &tts.ElevenLabsTTS{
		APIKey:  s.cfg.ElevenLabsKey,
		VoiceID: s.cfg.DefaultVoiceID,
	}
	mp3Data, err := ttsClient.Synthesize(ctx, text)
	if err != nil {
		s.logf("TTS error: %v", err)
		_ = s.sendJSON(ServerMessage{Type: "error", Code: "tts_failed", Message: err.Error()})
		return
	}

	_ = s.sendJSON(ServerMessage{Type: "tts.start"})
	_ = s.sendBinary(mp3Data)
	_ = s.sendJSON(ServerMessage{Type: "tts.end"})
}

// handleConversationHistory injects prior conversation turns into the LLM context,
// allowing session continuity across reconnects. The Flutter client sends saved
// messages; "teacher" role is normalized to "assistant" for OpenAI-compatible APIs.
func (s *Session) handleConversationHistory(items []HistoryItem) {
	if s.ctx == nil {
		s.ctx = llm.NewContextManager("")
	}
	for _, item := range items {
		role := item.Role
		if role == "teacher" {
			role = "assistant"
		}
		if role == "user" {
			s.ctx.AddUserMessage(item.Text)
		} else {
			s.ctx.AddAssistantMessage(item.Text)
		}
	}
	s.logf("conversation.history: %d items injected", len(items))
}

// handleSessionEnd tears down the session: closes the STT WebSocket and
// notifies the client. The main WebSocket connection remains open for potential reuse.
func (s *Session) handleSessionEnd() {
	// Close STT WebSocket if open
	if s.sttConn != nil {
		s.sttConn.Close()
		s.sttConn = nil
		s.sttReady = false
	}
	s.logf("session ended")
	_ = s.sendJSON(ServerMessage{Type: "session.ended"})
}

// streamLLMWithTTS orchestrates the LLM → TTS pipeline with parallelism:
//
//  1. Streams LLM tokens to the client as chat.delta messages
//  2. A SentenceSplitter accumulates tokens into complete sentences
//  3. Each sentence is sent to ElevenLabs TTS in a parallel goroutine
//  4. TTS MP3 binary frames are sent to the client as they become available
//
// This architecture minimizes time-to-first-audio: TTS synthesis begins as
// soon as the first sentence is complete, without waiting for the full LLM response.
func (s *Session) streamLLMWithTTS(userText string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Build messages from context history (already includes latest user msg)
	msgs := make([]llm.Message, 0)
	if s.ctx.SystemRole != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: s.ctx.SystemRole})
	}
	msgs = append(msgs, s.ctx.History...)
	s.logf("LLM request: %d messages, systemRole=%q", len(msgs), s.ctx.SystemRole)

	llmClient := &llm.OpenRouterLLM{
		APIKey: s.cfg.OpenRouterKey,
		Model:  s.cfg.DefaultModel,
	}

	// TTS sentence channel + goroutine
	sentenceCh := make(chan string, 10)
	var ttsWg sync.WaitGroup

	ttsWg.Add(1)
	go func() {
		defer ttsWg.Done()
		ttsClient := &tts.ElevenLabsTTS{
			APIKey:  s.cfg.ElevenLabsKey,
			VoiceID: s.cfg.DefaultVoiceID,
		}
		_ = s.sendJSON(ServerMessage{Type: "tts.start"})
		for sentence := range sentenceCh {
			s.logf("TTS synthesizing: %s", sentence)
			mp3Data, err := ttsClient.Synthesize(ctx, sentence)
			if err != nil {
				s.logf("TTS error for sentence: %v", err)
				continue
			}
			_ = s.sendBinary(mp3Data)
		}
		_ = s.sendJSON(ServerMessage{Type: "tts.end"})
	}()

	// Sentence splitter feeds TTS channel
	splitter := tts.NewSentenceSplitter(func(sentence string) {
		sentenceCh <- sentence
	})

	// Stream LLM
	fullResponse, err := llmClient.StreamChat(ctx, msgs, func(delta string) {
		_ = s.sendJSON(ServerMessage{Type: "chat.delta", Text: delta})
		splitter.Feed(delta)
	})

	// Flush remaining text to TTS
	splitter.Flush()
	close(sentenceCh)

	// Signal chat complete
	_ = s.sendJSON(ServerMessage{Type: "chat.done"})

	// Wait for TTS to finish
	ttsWg.Wait()

	// Update context with assistant response
	if err == nil && fullResponse != "" {
		s.ctx.AddAssistantMessage(fullResponse)
		// Feed last assistant reply to STT as previous_text for better accuracy
		if s.sttConn != nil {
			s.sttConn.PreviousText = fullResponse
		}
	} else if err != nil {
		s.logf("LLM error: %v", err)
		_ = s.sendJSON(ServerMessage{Type: "error", Code: "llm_failed", Message: err.Error()})
	}
}

// --- mock fallbacks (used when API keys are not configured, e.g., in tests) ---

// mockAudioEnd simulates the STT → LLM → TTS pipeline with canned data.
func (s *Session) mockAudioEnd() {
	_ = s.sendJSON(ServerMessage{Type: "asr.result", Text: "Mock transcription of your audio", IsFinal: BoolPtr(true)})
	s.streamChatDeltas([]string{"That's", " a", " great", " question!", " You", " can", " check", " in", " at", " counter", " 3."})
	_ = s.sendJSON(ServerMessage{Type: "chat.done"})
	s.sendMockTTS()
}

// mockTextQuery simulates an LLM response for a text query in mock mode.
func (s *Session) mockTextQuery(text string) {
	response := fmt.Sprintf("I understand your question about: %s. Let me help you with that.", text)
	tokens := tokenize(response)
	s.streamChatDeltas(tokens)
	_ = s.sendJSON(ServerMessage{Type: "chat.done"})
	s.sendMockTTS()
}

// streamChatDeltas sends tokens one at a time with a 50ms delay to simulate
// the streaming behavior of a real LLM response.
func (s *Session) streamChatDeltas(tokens []string) {
	for _, tok := range tokens {
		_ = s.sendJSON(ServerMessage{Type: "chat.delta", Text: tok})
		time.Sleep(50 * time.Millisecond)
	}
}

// sendMockTTS sends a dummy TTS sequence (start → 1KB binary → end) for testing.
func (s *Session) sendMockTTS() {
	_ = s.sendJSON(ServerMessage{Type: "tts.start"})
	dummyMP3 := make([]byte, 1024)
	_ = s.sendBinary(dummyMP3)
	_ = s.sendJSON(ServerMessage{Type: "tts.end"})
}

// tokenize splits a string into space-preserving tokens for mock streaming.
// Each token retains its leading space (e.g., " hello") except the first word.
func tokenize(s string) []string {
	tokens := make([]string, 0)
	current := ""
	for i, ch := range s {
		if ch == ' ' && i > 0 {
			if current != "" {
				tokens = append(tokens, current)
			}
			current = " "
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		tokens = append(tokens, current)
	}
	return tokens
}
