package llm

// ContextManager maintains a sliding window of conversation history for LLM calls.
// It keeps at most MaxMessages entries (default 40 ≈ 20 QA pairs) to stay within
// typical LLM context limits while preserving recent conversation flow.
type ContextManager struct {
	SystemRole  string    // system prompt injected at the start of every LLM request
	MaxMessages int       // max total messages retained (default 40 = 20 QA pairs)
	History     []Message // ordered conversation turns (user/assistant alternating)
}

// NewContextManager creates a new context manager with the given system prompt.
func NewContextManager(systemRole string) *ContextManager {
	return &ContextManager{
		SystemRole:  systemRole,
		MaxMessages: 40,
		History:     make([]Message, 0),
	}
}

// AddUserMessage appends a user message and trims history if it exceeds MaxMessages.
func (c *ContextManager) AddUserMessage(text string) {
	c.History = append(c.History, Message{Role: "user", Content: text})
	c.trim()
}

// AddAssistantMessage appends an assistant message and trims history if needed.
func (c *ContextManager) AddAssistantMessage(text string) {
	c.History = append(c.History, Message{Role: "assistant", Content: text})
	c.trim()
}

// SetHistory replaces the current history with injected conversation context,
// typically from the Flutter client's persisted chat history on reconnect.
// "teacher" role is normalized to "assistant" for OpenAI API compatibility.
func (c *ContextManager) SetHistory(items []struct{ Role, Text string }) {
	c.History = make([]Message, 0, len(items))
	for _, item := range items {
		role := item.Role
		if role == "teacher" || role == "assistant" {
			role = "assistant"
		}
		c.History = append(c.History, Message{Role: role, Content: item.Text})
	}
	c.trim()
}

// BuildMessages returns the complete message list for an LLM request, including
// the system prompt, conversation history, and the new user message.
// Note: this appends userText as a new message without adding it to History —
// use AddUserMessage first if the message should be persisted in context.
func (c *ContextManager) BuildMessages(userText string) []Message {
	msgs := make([]Message, 0, 2+len(c.History))
	if c.SystemRole != "" {
		msgs = append(msgs, Message{Role: "system", Content: c.SystemRole})
	}
	msgs = append(msgs, c.History...)
	msgs = append(msgs, Message{Role: "user", Content: userText})
	return msgs
}

// trim drops the oldest messages when History exceeds MaxMessages,
// keeping only the most recent entries for context window management.
func (c *ContextManager) trim() {
	max := c.MaxMessages
	if max <= 0 {
		max = 40
	}
	if len(c.History) > max {
		c.History = c.History[len(c.History)-max:]
	}
}
