package llm

import "sync"

// ContextManager maintains a sliding window of conversation history for LLM calls.
// It keeps at most MaxMessages entries (default 40 ≈ 20 QA pairs) to stay within
// typical LLM context limits while preserving recent conversation flow.
//
// All public methods are goroutine-safe via an internal RWMutex.
type ContextManager struct {
	SystemRole  string // system prompt injected at the start of every LLM request
	MaxMessages int    // max total messages retained (default 40 = 20 QA pairs)

	mu      sync.RWMutex
	history []Message // ordered conversation turns (user/assistant alternating)
}

// NewContextManager creates a new context manager with the given system prompt.
func NewContextManager(systemRole string) *ContextManager {
	return &ContextManager{
		SystemRole:  systemRole,
		MaxMessages: 40,
		history:     make([]Message, 0),
	}
}

// AddUserMessage appends a user message and trims history if it exceeds MaxMessages.
func (c *ContextManager) AddUserMessage(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append(c.history, Message{Role: "user", Content: text})
	c.trimLocked()
}

// AddAssistantMessage appends an assistant message and trims history if needed.
func (c *ContextManager) AddAssistantMessage(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append(c.history, Message{Role: "assistant", Content: text})
	c.trimLocked()
}

// HistorySnapshot returns a copy of the current history, safe for concurrent use.
// Callers get an immutable snapshot that won't be affected by subsequent mutations.
func (c *ContextManager) HistorySnapshot() []Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot := make([]Message, len(c.history))
	copy(snapshot, c.history)
	return snapshot
}

// SetHistory replaces the current history with injected conversation context,
// typically from the Flutter client's persisted chat history on reconnect.
// "teacher" role is normalized to "assistant" for OpenAI API compatibility.
func (c *ContextManager) SetHistory(items []struct{ Role, Text string }) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = make([]Message, 0, len(items))
	for _, item := range items {
		role := item.Role
		if role == "teacher" || role == "assistant" {
			role = "assistant"
		}
		c.history = append(c.history, Message{Role: role, Content: item.Text})
	}
	c.trimLocked()
}

// RemoveLastUserMessage removes the most recent user message from History,
// used to roll back context when a turn is cancelled (barge-in).
// Returns true if a message was removed.
func (c *ContextManager) RemoveLastUserMessage() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.history) - 1; i >= 0; i-- {
		if c.history[i].Role == "user" {
			c.history = append(c.history[:i], c.history[i+1:]...)
			return true
		}
	}
	return false
}

// trimLocked drops the oldest messages when history exceeds MaxMessages.
// Must be called with mu held.
func (c *ContextManager) trimLocked() {
	max := c.MaxMessages
	if max <= 0 {
		max = 40
	}
	if len(c.history) > max {
		c.history = c.history[len(c.history)-max:]
	}
}
