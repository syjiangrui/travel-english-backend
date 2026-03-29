// Package hint provides a REST API endpoint for generating contextual
// conversation hints. The Flutter client calls POST /hint when the user
// is idle for a few seconds, and receives a short English phrase suggestion
// based on the current scene and conversation history.
package hint

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"travel-english-backend/config"
	"travel-english-backend/llm"
)

// hintRequest is the JSON body sent by the Flutter client.
type hintRequest struct {
	SceneID  string        `json:"scene_id"`
	Messages []hintMessage `json:"messages"`
}

// hintMessage is a simplified chat message for hint context.
type hintMessage struct {
	Role string `json:"role"` // "user" or "assistant"/"teacher"
	Text string `json:"text"`
}

// hintResponse is the JSON body returned to the client.
type hintResponse struct {
	HintEN string `json:"hint_en"`
	HintCN string `json:"hint_cn"`
}

// sceneNames maps scene IDs to human-readable Chinese names for the LLM prompt.
var sceneNames = map[string]string{
	"airport":    "机场",
	"hotel":      "酒店",
	"restaurant": "餐厅",
	"shopping":   "购物",
	"transport":  "交通出行",
	"directions": "问路",
	"emergency":  "紧急求助",
}

// HandleHint returns an http.HandlerFunc that generates a contextual hint
// using the LLM. It performs a non-streaming LLM call with a specialized
// system prompt, parses the result, and returns a JSON hint.
func HandleHint(cfg *config.Config) http.HandlerFunc {
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

		var req hintRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[hint] JSON decode error: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
			return
		}

		// Mock mode: return a fixed hint when no API key is configured
		if cfg.OpenRouterKey == "" {
			writeJSON(w, hintResponse{
				HintEN: "Can you help me find gate 5?",
				HintCN: "你可以试着问: Can you help me find gate 5?",
			})
			return
		}

		// Build LLM messages
		sceneName := sceneNames[req.SceneID]
		if sceneName == "" {
			sceneName = req.SceneID
		}

		log.Printf("[hint] request: scene=%s, %d messages", req.SceneID, len(req.Messages))
		msgs := buildHintMessages(sceneName, req.Messages)

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		llmClient := &llm.OpenRouterLLM{
			APIKey: cfg.OpenRouterKey,
			Model:  cfg.DefaultModel,
		}

		// Use StreamChat with a no-op delta handler (response is ~20 tokens)
		fullResponse, err := llmClient.StreamChat(ctx, msgs, func(delta string) {})
		if err != nil {
			log.Printf("[hint] LLM error: %v", err)
			http.Error(w, `{"error":"hint generation failed"}`, http.StatusInternalServerError)
			return
		}

		hintEN, hintCN := parseHintResponse(fullResponse)
		if hintEN == "" {
			log.Printf("[hint] empty/unparseable response: %s", fullResponse)
			http.Error(w, `{"error":"no hint generated"}`, http.StatusInternalServerError)
			return
		}

		log.Printf("[hint] scene=%s EN=%s CN=%s", req.SceneID, hintEN, hintCN)
		writeJSON(w, hintResponse{HintEN: hintEN, HintCN: hintCN})
	}
}

// buildHintMessages constructs the LLM message list for hint generation.
func buildHintMessages(sceneName string, history []hintMessage) []llm.Message {
	systemPrompt := fmt.Sprintf(`You are a hint generator for a travel English learning app.
The student is currently practicing in the "%s" scene.
Based on the conversation history, suggest ONE short English phrase the student could say next.

Rules:
- Output EXACTLY one line in this format: HINT_EN: <english phrase> | HINT_CN: <chinese display>
- The English phrase should be 3-12 words, natural and useful for travel
- The HINT_CN MUST include both the English phrase AND its Chinese translation, like:
  "试试说: Can I have a window seat?（我可以坐靠窗的位置吗？）"
  "你可以问: Where is gate 5?（5号登机口在哪里？）"
- Do NOT repeat anything the student already said
- Make it contextually relevant to the conversation
- If no conversation history, suggest a good opening line for the scene
- Do NOT include any other text or explanation`, sceneName)

	msgs := make([]llm.Message, 0, 2+len(history))
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})

	// Add recent conversation history (last 10 messages max)
	start := 0
	if len(history) > 10 {
		start = len(history) - 10
	}
	for _, m := range history[start:] {
		role := m.Role
		if role == "teacher" {
			role = "assistant"
		}
		msgs = append(msgs, llm.Message{Role: role, Content: m.Text})
	}

	// Final user instruction
	msgs = append(msgs, llm.Message{
		Role:    "user",
		Content: "Generate a hint for me. Remember: output EXACTLY one line in format HINT_EN: ... | HINT_CN: ...",
	})

	return msgs
}

// parseHintResponse extracts the English hint and Chinese wrapper from the
// LLM response. Expected format: "HINT_EN: ... | HINT_CN: ..."
func parseHintResponse(raw string) (en, cn string) {
	raw = strings.TrimSpace(raw)

	// Try to parse "HINT_EN: ... | HINT_CN: ..."
	parts := strings.SplitN(raw, "|", 2)
	if len(parts) == 2 {
		enPart := strings.TrimSpace(parts[0])
		cnPart := strings.TrimSpace(parts[1])

		en = strings.TrimSpace(strings.TrimPrefix(enPart, "HINT_EN:"))
		cn = strings.TrimSpace(strings.TrimPrefix(cnPart, "HINT_CN:"))
	}

	// Fallback: use the entire response as hint
	if en == "" {
		en = raw
		// Remove potential prefix markers
		en = strings.TrimPrefix(en, "HINT_EN:")
		en = strings.TrimSpace(en)
	}
	if cn == "" && en != "" {
		cn = "你可以试着说: " + en
	}

	return
}

// writeJSON sends a JSON response with 200 OK status.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
