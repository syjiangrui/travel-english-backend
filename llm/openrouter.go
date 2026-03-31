// Package llm provides LLM chat completion via OpenRouter's OpenAI-compatible API.
// It supports streaming (SSE) responses for real-time token delivery to the client.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message represents a single chat message in the OpenAI messages format.
type Message struct {
	Role    string `json:"role"`    // "system", "user", or "assistant"
	Content string `json:"content"` // message text
}

// OpenRouterLLM is a client for streaming chat completions via OpenRouter.
type OpenRouterLLM struct {
	APIKey      string
	Model       string  // e.g., "deepseek/deepseek-chat-v3.1"
	BaseURL     string  // default "https://openrouter.ai/api/v1"
	MaxTokens   int     // 0 = use default (100)
	Temperature float64 // 0 = omit from request (use API default)
}

// StreamChat sends messages to OpenRouter and streams delta tokens via the onDelta callback.
// Returns the full accumulated response text and any error.
// max_tokens is set to 100 to keep replies short and conversational for the travel English use case.
func (o *OpenRouterLLM) StreamChat(ctx context.Context, messages []Message, onDelta func(string)) (string, error) {
	baseURL := o.BaseURL
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}

	maxTokens := o.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 100 // default: keep replies short and conversational
	}

	reqBody := map[string]interface{}{
		"model":      o.Model,
		"messages":   messages,
		"stream":     true,
		"max_tokens": maxTokens,
	}
	if o.Temperature > 0 {
		reqBody["temperature"] = o.Temperature
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse SSE stream: each line starting with "data: " contains a JSON chunk.
	// The stream terminates with "data: [DONE]".
	var fullResponse strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed chunks
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			delta := chunk.Choices[0].Delta.Content
			fullResponse.WriteString(delta)
			onDelta(delta)
		}
	}

	return fullResponse.String(), scanner.Err()
}
