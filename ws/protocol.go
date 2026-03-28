package ws

// ---------- Client → Server ----------

type ClientMessage struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Config    *SessionConfig  `json:"config,omitempty"`
	Text      string          `json:"text,omitempty"`
	Items     []HistoryItem   `json:"items,omitempty"`
}

type SessionConfig struct {
	SystemRole    string `json:"system_role,omitempty"`
	SpeakingStyle string `json:"speaking_style,omitempty"`
	TTSVoiceID    string `json:"tts_voice_id,omitempty"`
	STTLanguage   string `json:"stt_language,omitempty"`
}

type HistoryItem struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// ---------- Server → Client ----------

type ServerMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text,omitempty"`
	IsFinal   *bool  `json:"is_final,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func BoolPtr(b bool) *bool { return &b }
