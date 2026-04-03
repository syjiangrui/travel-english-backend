// Package ws implements the WebSocket server that bridges the Flutter client
// with ElevenLabs STT/TTS and OpenRouter LLM services. Communication uses
// JSON text frames for control messages and binary frames for audio data.
package ws

// ---------- Client → Server ----------

// ClientMessage is the envelope for all JSON messages sent by the Flutter client.
// The Type field determines which handler processes the message:
//   - "session.start"          – initialize a new conversation session
//   - "session.update"         – update session config mid-session (e.g., system_role after memory extraction)
//   - "audio.end"              – signal end of audio recording, trigger STT → LLM → TTS pipeline
//   - "text.query"             – send a text message directly (bypasses STT)
//   - "tts.synthesize"         – request standalone TTS synthesis (e.g., message replay)
//   - "tts.synthesize.full"    – alias for standalone TTS synthesis
//   - "conversation.history"   – inject prior conversation context for LLM continuity
//   - "turn.cancel"            – cancel the active LLM+TTS pipeline (barge-in interrupt)
//   - "session.end"            – tear down the session and release resources
type ClientMessage struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id,omitempty"`
	Config    *SessionConfig `json:"config,omitempty"`
	Text      string         `json:"text,omitempty"`
	Items     []HistoryItem  `json:"items,omitempty"`
}

// SessionConfig carries per-session settings supplied by the client at session.start.
type SessionConfig struct {
	SystemRole    string `json:"system_role,omitempty"`    // LLM system prompt (scene-specific persona)
	SpeakingStyle string `json:"speaking_style,omitempty"` // reserved for future voice style control
	TTSVoiceID    string `json:"tts_voice_id,omitempty"`   // ElevenLabs voice ID override
	STTLanguage   string `json:"stt_language,omitempty"`   // ISO 639-1 code; empty = auto-detect
	STTMode       string `json:"stt_mode,omitempty"`       // "realtime" (default) or "batch"
	STTProvider   string `json:"stt_provider,omitempty"`   // "elevenlabs" (default) or "deepinfra"; overrides server default
}

// HistoryItem represents a single turn in the conversation history,
// used by the "conversation.history" message to restore LLM context.
type HistoryItem struct {
	Role string `json:"role"` // "user", "teacher" (mapped to "assistant"), or "assistant"
	Text string `json:"text"`
}

// ---------- Server → Client ----------

// ServerMessage is the envelope for all JSON messages sent to the Flutter client.
// Key message types:
//   - "session.started"  – confirms session creation, includes assigned session_id
//   - "asr.result"       – ASR transcript (is_final=false for interim, true for committed)
//   - "chat.delta"       – streaming LLM token
//   - "chat.done"        – signals end of LLM response
//   - "tts.start/end"    – brackets binary MP3 audio frames
//   - "turn.cancelled"   – confirms active pipeline was cancelled (response to turn.cancel)
//   - "error"            – error with code and human-readable message
//   - "session.ended"    – confirms session teardown
type ServerMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text,omitempty"`
	IsFinal   *bool  `json:"is_final,omitempty"` // used by asr.result to distinguish interim vs final
	Code      string `json:"code,omitempty"`     // error code (e.g., "stt_failed", "llm_failed")
	Message   string `json:"message,omitempty"`  // human-readable error description
	TurnID    int64  `json:"turn_id,omitempty"`  // server-side turn generation; used by client to filter stale barge-in data
}

// BoolPtr returns a pointer to b, used for the optional IsFinal JSON field.
func BoolPtr(b bool) *bool { return &b }
