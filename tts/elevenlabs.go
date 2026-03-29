// Package tts provides text-to-speech synthesis via ElevenLabs REST API
// and a sentence splitter for streaming LLM-to-TTS pipelining.
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ElevenLabsTTS synthesizes speech from text using the ElevenLabs v1 API.
// It returns MP3 audio bytes suitable for direct playback or WebSocket forwarding.
type ElevenLabsTTS struct {
	APIKey  string
	VoiceID string // ElevenLabs voice ID; defaults to "21m00Tcm4TlvDq8ikWAM" (Rachel)
	BaseURL string // default "https://api.elevenlabs.io/v1"

	// PreviousText provides context from the preceding sentence for natural prosody
	// continuity across sentence boundaries. Updated automatically after each Synthesize call.
	PreviousText string
}

// Synthesize converts text to MP3 audio bytes using ElevenLabs multilingual_v2 model.
// After each successful call, PreviousText is updated to enable prosody continuity
// when synthesizing the next sentence in a sequence.
func (t *ElevenLabsTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	baseURL := t.BaseURL
	if baseURL == "" {
		baseURL = "https://api.elevenlabs.io/v1"
	}

	voiceID := t.VoiceID
	if voiceID == "" {
		voiceID = "21m00Tcm4TlvDq8ikWAM" // default Rachel
	}

	reqBody := map[string]interface{}{
		"text":     text,
		"model_id": "eleven_multilingual_v2",
		"voice_settings": map[string]interface{}{
			"stability":        0.5,
			"similarity_boost": 0.75,
		},
	}
	if t.PreviousText != "" {
		reqBody["previous_text"] = t.PreviousText
	}
	jsonBody, _ := json.Marshal(reqBody)

	url := fmt.Sprintf("%s/text-to-speech/%s", baseURL, voiceID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("xi-api-key", t.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TTS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TTS API error %d: %s", resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Update previous_text so the next sentence has prosody continuity
	t.PreviousText = text
	return data, nil
}
