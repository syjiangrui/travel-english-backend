// Package memory provides a REST API endpoint for extracting long-term user
// memories from recent conversation history. The Flutter client calls POST /memory
// after every 2 completed AI turns, and receives a JSON array of extracted memory strings.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"travel-english-backend/config"
	"travel-english-backend/llm"
)

// memoryRequest is the JSON body sent by the Flutter client.
type memoryRequest struct {
	Messages []memoryMessage `json:"messages"`
}

// memoryMessage is a simplified chat message for memory extraction context.
type memoryMessage struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

// memoryResponse is the JSON body returned to the client.
type memoryResponse struct {
	Memories []string `json:"memories"`
}

// System prompt for memory extraction, matching the voice-chat-demo behavior.
const memorySystemPrompt = `你是一个记忆总结助手。请总结以下对话，提取出用户新的长期记忆点（如偏好、核心事件、个人信息等）。
只输出一个JSON数组，其中每个元素是一个字符串（即一个记忆节点）。如果本次对话没有新的值得记忆的内容，请输出空数组 []。
不要输出任何其他文本或解释，必须是纯粹的合法的JSON格式。`

// jsonArrayRe extracts a JSON array from potentially noisy LLM output.
var jsonArrayRe = regexp.MustCompile(`(?s)\[.*\]`)

// HandleMemory returns an http.HandlerFunc that extracts long-term memories
// from recent conversation using the LLM.
func HandleMemory(cfg *config.Config) http.HandlerFunc {
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

		var req memoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[memory] JSON decode error: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
			return
		}

		if len(req.Messages) == 0 {
			writeJSON(w, memoryResponse{Memories: []string{}})
			return
		}

		// Mock mode
		if cfg.OpenRouterKey == "" {
			writeJSON(w, memoryResponse{Memories: []string{}})
			return
		}

		log.Printf("[memory] request: %d messages", len(req.Messages))

		// Build LLM messages: system + user (conversation text)
		conversationText := formatConversation(req.Messages)
		msgs := []llm.Message{
			{Role: "system", Content: memorySystemPrompt},
			{Role: "user", Content: "这是最近的对话历史：\n" + conversationText},
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		llmClient := &llm.OpenRouterLLM{
			APIKey:      cfg.OpenRouterKey,
			Model:       cfg.DefaultModel,
			MaxTokens:   300,
			Temperature: 0.1,
		}

		fullResponse, err := llmClient.StreamChat(ctx, msgs, func(delta string) {})
		if err != nil {
			log.Printf("[memory] LLM error: %v", err)
			http.Error(w, `{"error":"memory extraction failed"}`, http.StatusInternalServerError)
			return
		}

		memories := parseMemories(fullResponse)
		log.Printf("[memory] extracted %d memories from response: %s", len(memories), fullResponse)
		writeJSON(w, memoryResponse{Memories: memories})
	}
}

// formatConversation builds the conversation text for the user message.
func formatConversation(messages []memoryMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// parseMemories extracts a JSON string array from the LLM response.
// Uses regex fallback to handle noisy output (e.g., markdown wrapping).
func parseMemories(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}

	// Strategy 1: regex extract JSON array
	match := jsonArrayRe.FindString(raw)
	if match != "" {
		var arr []string
		if err := json.Unmarshal([]byte(match), &arr); err == nil {
			return filterEmpty(arr)
		}
	}

	// Strategy 2: direct parse
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		return filterEmpty(arr)
	}

	// Strategy 3: give up, return empty
	log.Printf("[memory] unparseable response: %s", raw)
	return []string{}
}

// filterEmpty removes empty or whitespace-only strings from the array.
func filterEmpty(arr []string) []string {
	result := make([]string, 0, len(arr))
	for _, s := range arr {
		if strings.TrimSpace(s) != "" {
			result = append(result, strings.TrimSpace(s))
		}
	}
	return result
}

// writeJSON sends a JSON response with 200 OK status.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
