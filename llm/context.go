package llm

// ContextManager maintains conversation history for LLM calls.
type ContextManager struct {
	SystemRole  string
	MaxMessages int // max total messages (default 40 = 20 QA pairs)
	History     []Message
}

// NewContextManager creates a new context manager.
func NewContextManager(systemRole string) *ContextManager {
	return &ContextManager{
		SystemRole:  systemRole,
		MaxMessages: 40,
		History:     make([]Message, 0),
	}
}

// AddUserMessage appends a user message.
func (c *ContextManager) AddUserMessage(text string) {
	c.History = append(c.History, Message{Role: "user", Content: text})
	c.trim()
}

// AddAssistantMessage appends an assistant message.
func (c *ContextManager) AddAssistantMessage(text string) {
	c.History = append(c.History, Message{Role: "assistant", Content: text})
	c.trim()
}

// SetHistory replaces the history with injected context.
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

// BuildMessages returns the full message list for LLM, including system prompt.
func (c *ContextManager) BuildMessages(userText string) []Message {
	msgs := make([]Message, 0, 2+len(c.History))
	if c.SystemRole != "" {
		msgs = append(msgs, Message{Role: "system", Content: c.SystemRole})
	}
	msgs = append(msgs, c.History...)
	msgs = append(msgs, Message{Role: "user", Content: userText})
	return msgs
}

func (c *ContextManager) trim() {
	max := c.MaxMessages
	if max <= 0 {
		max = 40
	}
	if len(c.History) > max {
		c.History = c.History[len(c.History)-max:]
	}
}
