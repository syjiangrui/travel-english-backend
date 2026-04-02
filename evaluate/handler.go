// Package evaluate provides a REST API endpoint for evaluating user language
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
	UserText string           `json:"user_text"`
	SceneID  string           `json:"scene_id"`
	Context  []contextMessage `json:"context"`
}

// contextMessage is a simplified chat message for evaluation context.
type contextMessage struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

// evaluateResponse is the JSON body returned to the client.
type evaluateResponse struct {
	Score      int    `json:"score"`      // 0-5 (0 = non-target-language)
	Correction string `json:"correction"` // more natural expression
	Feedback   string `json:"feedback"`   // Chinese feedback
}

// Model used for evaluation — Gemini Flash Lite for fast, cheap, good instruction following.
const evalModel = "google/gemini-3.1-flash-lite-preview"

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

// HandleEvaluate returns an http.HandlerFunc that evaluates the user's
// language expression using the LLM.
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

		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()

		llmClient := &llm.OpenRouterLLM{
			APIKey: cfg.OpenRouterKey,
			Model:  evalModel,
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
	systemPrompt := fmt.Sprintf(`You are a language tutor evaluating a student's spoken language in a travel scenario.
Scene: %s

## Your task
1. Determine the TARGET LANGUAGE by looking at what language the AI assistant speaks in the conversation context. If no context, default to English.
2. Evaluate the student's input against that target language.

## Output format
Respond with ONLY a JSON object, no other text:
{"score":N,"correction":"...","feedback":"..."}

## Scoring rules

If student speaks IN the target language:
- score: 1-5 (1=major errors, 2=several errors, 3=minor issues, 4=good with small improvements, 5=perfect/native-like)
- correction: more natural expression in the target language. If perfect, repeat original.
- feedback: brief Chinese feedback (under 60 chars), encouraging tone.

If student speaks in a DIFFERENT language than the target:
- score: 0 (MUST be exactly 0, not 1)
- correction: translate their message into the target language.
- feedback: 鼓励学生用目标语言表达，格式"试试说：<correction>"

If student MIXES target language with other languages:
- score: 3-5 (reward effort, higher if target language parts are correct)
- correction: full sentence rewritten in the target language only.
- feedback: Chinese feedback, praise the attempt, suggest full target language version.

## Important
- Single words (yes/no/thanks/はい/네) → score 3-4
- correction MUST be in the target language
- feedback MUST be in Chinese (中文)
- score 0 means "not in target language", it is NOT an error score`, sceneName)

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
		Content: fmt.Sprintf("Evaluate this expression: %q\nRespond with ONLY the JSON.", userText),
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
		return evaluateResponse{
			Score:      3,
			Correction: "",
			Feedback:   "评价生成失败，请重试",
		}
	}

	// Clamp score: allow 0 (non-target-language marker) and 1-5
	if result.Score < 0 {
		result.Score = 0
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
