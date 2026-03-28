package tts

import "strings"

// SentenceSplitter buffers LLM delta tokens and emits complete sentences.
type SentenceSplitter struct {
	buffer     strings.Builder
	OnSentence func(string) // called when a complete sentence is detected
}

// NewSentenceSplitter creates a new splitter with the given callback.
func NewSentenceSplitter(onSentence func(string)) *SentenceSplitter {
	return &SentenceSplitter{OnSentence: onSentence}
}

// Feed adds a delta token and checks for sentence boundaries.
func (s *SentenceSplitter) Feed(delta string) {
	s.buffer.WriteString(delta)
	text := s.buffer.String()

	// Find the last sentence boundary
	lastIdx := -1
	for _, ender := range []string{". ", "! ", "? ", ".\n", "!\n", "?\n", "。", "！", "？"} {
		if idx := strings.LastIndex(text, ender); idx > lastIdx {
			lastIdx = idx + len(ender) - 1
		}
	}

	if lastIdx >= 0 {
		sentence := strings.TrimSpace(text[:lastIdx+1])
		remainder := text[lastIdx+1:]
		s.buffer.Reset()
		s.buffer.WriteString(remainder)
		if sentence != "" && s.OnSentence != nil {
			s.OnSentence(sentence)
		}
	}
}

// Flush emits any remaining buffered text as a final sentence.
func (s *SentenceSplitter) Flush() {
	remaining := strings.TrimSpace(s.buffer.String())
	if remaining != "" && s.OnSentence != nil {
		s.OnSentence(remaining)
	}
	s.buffer.Reset()
}
