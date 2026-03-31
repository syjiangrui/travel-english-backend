// Package translate provides a REST API endpoint for text translation.
// The Flutter client calls POST /translate with a text string, and receives
// the translated text. Language direction is auto-detected: Chinese → English,
// otherwise → Chinese (Simplified).
package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	"unicode"

	"travel-english-backend/config"
	"travel-english-backend/llm"
)

// translateRequest is the JSON body sent by the Flutter client.
type translateRequest struct {
	Text string `json:"text"`
}

// translateResponse is the JSON body returned to the client.
type translateResponse struct {
	Result string `json:"result"`
}

// HandleTranslate returns an http.HandlerFunc that translates text using the LLM.
func HandleTranslate(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CORS headers for Flutter client
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req translateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[translate] JSON decode error: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
			return
		}

		if req.Text == "" {
			http.Error(w, `{"error":"text is required"}`, http.StatusBadRequest)
			return
		}

		// Mock mode
		if cfg.OpenRouterKey == "" {
			writeJSON(w, translateResponse{Result: "[mock translation]"})
			return
		}

		isChinese := containsChinese(req.Text)
		targetLang := "Chinese (Simplified)"
		if isChinese {
			targetLang = "English"
		}

		log.Printf("[translate] text=%q targetLang=%s", req.Text, targetLang)

		msgs := []llm.Message{
			{
				Role: "system",
				Content: "You are a translator. Translate the following text to " + targetLang + ". " +
					"Output ONLY the translation, no explanation, no quotes, no extra text.",
			},
			{
				Role:    "user",
				Content: req.Text,
			},
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		llmClient := &llm.OpenRouterLLM{
			APIKey: cfg.OpenRouterKey,
			Model:  cfg.DefaultModel,
		}

		result, err := llmClient.StreamChat(ctx, msgs, func(delta string) {})
		if err != nil {
			log.Printf("[translate] LLM error: %v", err)
			http.Error(w, `{"error":"translation failed"}`, http.StatusInternalServerError)
			return
		}

		if result == "" {
			http.Error(w, `{"error":"empty translation"}`, http.StatusInternalServerError)
			return
		}

		log.Printf("[translate] result=%q", result)
		writeJSON(w, translateResponse{Result: result})
	}
}

// containsChinese reports whether s contains any CJK unified ideograph.
func containsChinese(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// writeJSON sends a JSON response with 200 OK status.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
