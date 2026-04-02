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
	ExistingMemories []string        `json:"existing_memories"`
	Messages         []memoryMessage `json:"messages"`
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
const memorySystemPrompt = `你是一个记忆总结助手。你的任务不是简单追加，而是维护一份稳定、去重、可持续更新的“用户长期记忆清单”。

你会收到两部分输入：
1. 现有长期记忆（可能包含重复、过时、表述重叠的内容）
2. 最近对话

请基于两部分信息，输出“合并后的完整长期记忆列表”，要求：
- 只保留对未来对话真正有价值的信息：身份背景、稳定偏好、长期计划、反复出现的兴趣、重要关系、持续性约束等
- 删除重复项，把语义相近的记忆合并成一条更清晰的表述
- 若新对话纠正了旧信息，以新信息为准
- 尽量保留稳定事实，忽略一次性、寒暄式、无长期价值的信息
- 每条记忆用简洁的一句中文表达
- 控制数量，宁缺毋滥

只输出一个JSON数组，不要输出任何其他文本或解释。若没有值得保留的记忆，输出 []。`

// jsonArrayRe extracts a JSON array from potentially noisy LLM output.
var jsonArrayRe = regexp.MustCompile(`(?s)\[.*?\]`)

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

		existingMemories := normalizeMemories(req.ExistingMemories)

		if len(req.Messages) == 0 {
			writeJSON(w, memoryResponse{Memories: existingMemories})
			return
		}

		// Mock mode
		if cfg.OpenRouterKey == "" {
			writeJSON(w, memoryResponse{Memories: existingMemories})
			return
		}

		log.Printf("[memory] request: %d existing memories, %d messages", len(existingMemories), len(req.Messages))

		// Build LLM messages: system + user (conversation text)
		conversationText := formatConversation(req.Messages)
		existingMemoriesText := formatExistingMemories(existingMemories)
		msgs := []llm.Message{
			{Role: "system", Content: memorySystemPrompt},
			{Role: "user", Content: "现有长期记忆：\n" + existingMemoriesText + "\n\n最近对话历史：\n" + conversationText},
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

func formatExistingMemories(memories []string) string {
	if len(memories) == 0 {
		return "[]"
	}

	var sb strings.Builder
	for _, memory := range memories {
		memory = strings.TrimSpace(memory)
		if memory == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(memory)
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "[]"
	}
	return sb.String()
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
			return normalizeMemories(arr)
		}
	}

	// Strategy 2: direct parse
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		return normalizeMemories(arr)
	}

	// Strategy 3: give up, return empty
	log.Printf("[memory] unparseable response: %s", raw)
	return []string{}
}


// normalizeMemories removes empty values and exact duplicates while preserving order.
func normalizeMemories(arr []string) []string {
	result := make([]string, 0, len(arr))
	seen := make(map[string]struct{}, len(arr))
	for _, s := range arr {
		normalized := strings.TrimSpace(s)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

// writeJSON sends a JSON response with 200 OK status.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
