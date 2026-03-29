// Package evaluate provides a REST API endpoint for evaluating user English
// expression quality. The Flutter client calls POST /evaluate after each user
// message, and receives a score, correction, and feedback.
package evaluate

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

// evaluateRequest is the JSON body sent by the Flutter client.
type evaluateRequest struct {
	UserText string            `json:"user_text"`
	SceneID  string            `json:"scene_id"`
	Context  []contextMessage  `json:"context"`
}

// contextMessage is a simplified chat message for evaluation context.
type contextMessage struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

// evaluateResponse is the JSON body returned to the client.
type evaluateResponse struct {
	Score      int    `json:"score"`      // 1-5
	Correction string `json:"correction"` // more natural expression
	Feedback   string `json:"feedback"`   // Chinese feedback
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

// HandleEvaluate returns an http.HandlerFunc that evaluates the user's English
// expression using the LLM.
func HandleEvaluate(cfg *config.Config) http.HandlerFunc {
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

		var req evaluateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[evaluate] JSON decode error: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(req.UserText) == "" {
			http.Error(w, `{"error":"user_text is required"}`, http.StatusBadRequest)
			return
		}

		// Mock mode: return a fixed evaluation when no API key is configured
		if cfg.OpenRouterKey == "" {
			writeJSON(w, evaluateResponse{
				Score:      4,
				Correction: req.UserText,
				Feedback:   "表达清楚！(mock mode)",
			})
			return
		}

		sceneName := sceneNames[req.SceneID]
		if sceneName == "" {
			sceneName = req.SceneID
		}

		log.Printf("[evaluate] request: scene=%s, text=%q, %d context msgs",
			req.SceneID, req.UserText, len(req.Context))

		msgs := buildEvalMessages(sceneName, req.UserText, req.Context)

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		llmClient := &llm.OpenRouterLLM{
			APIKey: cfg.OpenRouterKey,
			Model:  cfg.DefaultModel,
		}

		fullResponse, err := llmClient.StreamChat(ctx, msgs, func(delta string) {})
		if err != nil {
			log.Printf("[evaluate] LLM error: %v", err)
			http.Error(w, `{"error":"evaluation failed"}`, http.StatusInternalServerError)
			return
		}

		result := parseEvalResponse(fullResponse)
		log.Printf("[evaluate] score=%d, correction=%q", result.Score, result.Correction)
		writeJSON(w, result)
	}
}

// buildEvalMessages constructs the LLM message list for evaluation.
func buildEvalMessages(sceneName, userText string, history []contextMessage) []llm.Message {
	systemPrompt := fmt.Sprintf(`You are an English tutor evaluating a Chinese student's spoken English in a travel scenario.
Scene: %s

Evaluate the student's English expression and respond in EXACTLY this JSON format (no other text):
{"score":3,"correction":"corrected sentence","feedback":"中文反馈"}

Rules:
- score: 1-5 integer (1=major errors, 2=several errors, 3=minor issues, 4=good with small improvements, 5=perfect/native-like)
- correction: rewrite the sentence in more natural/native English. If already perfect, repeat the original.
- feedback: 1-2 sentences in Chinese explaining what was good/wrong. Be encouraging. Mention specific grammar/vocabulary issues if any.
- Keep feedback concise (under 50 characters if possible)
- Consider the conversation context when evaluating appropriateness
- Single words like "yes", "no", "thanks" should score 3-4 (acceptable but could be more complete)`, sceneName)

	msgs := make([]llm.Message, 0, 3+len(history))
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})

	// Add recent conversation context (last 6 messages)
	start := 0
	if len(history) > 6 {
		start = len(history) - 6
	}
	for _, m := range history[start:] {
		role := m.Role
		if role == "teacher" {
			role = "assistant"
		}
		msgs = append(msgs, llm.Message{Role: role, Content: m.Text})
	}

	// Final instruction with the user text to evaluate
	msgs = append(msgs, llm.Message{
		Role:    "user",
		Content: fmt.Sprintf("Evaluate this English expression: %q\nRespond with ONLY the JSON object.", userText),
	})

	return msgs
}

// parseEvalResponse extracts score, correction, and feedback from the LLM response.
func parseEvalResponse(raw string) evaluateResponse {
	raw = strings.TrimSpace(raw)

	// Try to extract JSON from the response (LLM might wrap it in markdown)
	jsonStr := raw
	if idx := strings.Index(raw, "{"); idx >= 0 {
		if endIdx := strings.LastIndex(raw, "}"); endIdx > idx {
			jsonStr = raw[idx : endIdx+1]
		}
	}

	var result evaluateResponse
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		log.Printf("[evaluate] JSON parse error: %v, raw: %s", err, raw)
		// Fallback: return a neutral evaluation
		return evaluateResponse{
			Score:      3,
			Correction: "",
			Feedback:   "评价生成失败，请重试",
		}
	}

	// Clamp score to 1-5
	if result.Score < 1 {
		result.Score = 1
	}
	if result.Score > 5 {
		result.Score = 5
	}

	return result
}

// writeJSON sends a JSON response with 200 OK status.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
