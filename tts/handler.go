package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"travel-english-backend/config"
)

const mockMP3Size = 1024

type synthesizeRequest struct {
	Text    string `json:"text"`
	VoiceID string `json:"voice_id,omitempty"`
}

// HandleSynthesize exposes a small REST endpoint for standalone TTS synthesis.
//
// It is intentionally separate from the WebSocket chat pipeline so clients can
// request a clean pronunciation demo without affecting the active conversation
// turn state or playback callbacks.
func HandleSynthesize(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req synthesizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}

		text := strings.TrimSpace(req.Text)
		if text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "no-store")

		// Keep mock mode behavior aligned with the WebSocket test path so local
		// development and automated tests work without external API keys.
		if cfg == nil || cfg.ElevenLabsKey == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, mockMP3Size))
			return
		}

		voiceID := cfg.DefaultVoiceID
		if trimmed := strings.TrimSpace(req.VoiceID); trimmed != "" {
			voiceID = trimmed
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		ttsClient := &ElevenLabsTTS{
			APIKey:  cfg.ElevenLabsKey,
			VoiceID: voiceID,
		}
		mp3Data, err := ttsClient.Synthesize(ctx, text)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mp3Data)
	}
}
