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

type Session struct {
	ID         string
	conn       *websocket.Conn
	cfg        *config.Config
	mu         sync.Mutex
	sessionCfg *SessionConfig
	ctx        *llm.ContextManager

	// Streaming STT
	sttConn     *stt.RealtimeSTT
	sttReady    bool
	audioBuffer [][]byte // fallback buffer for mock mode
}

func NewSession(conn *websocket.Conn, cfg *config.Config) *Session {
	return &Session{
		conn:        conn,
		cfg:         cfg,
		audioBuffer: make([][]byte, 0),
	}
}

func (s *Session) isLive() bool {
	return s.cfg != nil && s.cfg.ElevenLabsKey != "" && s.cfg.OpenRouterKey != ""
}

// isBatchSTT returns true when the client requested batch (non-streaming) STT mode.
func (s *Session) isBatchSTT() bool {
	return s.sessionCfg != nil && s.sessionCfg.STTMode == "batch"
}

// sendJSON writes a JSON text frame, protected by mutex.
func (s *Session) sendJSON(v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteJSON(v)
}

// sendBinary writes a binary frame, protected by mutex.
func (s *Session) sendBinary(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (s *Session) logf(format string, args ...interface{}) {
	prefix := fmt.Sprintf("[WS] session %s: ", s.ID)
	log.Printf(prefix+format, args...)
}

// HandleMessage routes a JSON text message.
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

// HandleBinary processes incoming audio data.
// In batch STT mode: always buffer locally (no realtime forwarding).
// In realtime mode with live STT: forward to ElevenLabs STT WebSocket + buffer as fallback.
// In mock mode: buffer locally.
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

	s.logf("session started (live=%v, stt=%v, sttMode=%s)", s.isLive(), s.sttReady, func() string {
		if s.isBatchSTT() {
			return "batch"
		}
		return "realtime"
	}())
	_ = s.sendJSON(ServerMessage{Type: "session.started", SessionID: s.ID})
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

		// Set up channel to receive committed transcript
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

	sttClient := &stt.ElevenLabsSTT{APIKey: s.cfg.ElevenLabsKey}
	text, err := sttClient.Transcribe(ctx, pcmData)
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

func (s *Session) handleTextQuery(text string) {
	s.logf("text.query: %s", text)

	if !s.isLive() {
		s.mockTextQuery(text)
		return
	}

	s.ctx.AddUserMessage(text)
	s.streamLLMWithTTS(text)
}

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

// streamLLMWithTTS streams LLM response with parallel TTS sentence synthesis.
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

// --- mock fallbacks (same as Phase 1, for tests) ---

func (s *Session) mockAudioEnd() {
	_ = s.sendJSON(ServerMessage{Type: "asr.result", Text: "Mock transcription of your audio", IsFinal: BoolPtr(true)})
	s.streamChatDeltas([]string{"That's", " a", " great", " question!", " You", " can", " check", " in", " at", " counter", " 3."})
	_ = s.sendJSON(ServerMessage{Type: "chat.done"})
	s.sendMockTTS()
}

func (s *Session) mockTextQuery(text string) {
	response := fmt.Sprintf("I understand your question about: %s. Let me help you with that.", text)
	tokens := tokenize(response)
	s.streamChatDeltas(tokens)
	_ = s.sendJSON(ServerMessage{Type: "chat.done"})
	s.sendMockTTS()
}

func (s *Session) streamChatDeltas(tokens []string) {
	for _, tok := range tokens {
		_ = s.sendJSON(ServerMessage{Type: "chat.delta", Text: tok})
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Session) sendMockTTS() {
	_ = s.sendJSON(ServerMessage{Type: "tts.start"})
	dummyMP3 := make([]byte, 1024)
	_ = s.sendBinary(dummyMP3)
	_ = s.sendJSON(ServerMessage{Type: "tts.end"})
}

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
