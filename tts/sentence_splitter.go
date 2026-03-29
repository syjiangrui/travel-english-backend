package tts

import "strings"

// SentenceSplitter buffers streaming LLM delta tokens and emits complete sentences
// for TTS synthesis. It recognizes sentence boundaries at punctuation followed by
// whitespace (English: ". ", "! ", "? ") or CJK sentence-ending punctuation
// (Chinese: "。", "！", "？"). This allows TTS to begin synthesizing the first
// sentence while the LLM is still generating subsequent ones.
type SentenceSplitter struct {
	buffer     strings.Builder
	OnSentence func(string) // called with each complete sentence
}

// NewSentenceSplitter creates a splitter that calls onSentence for each detected sentence.
func NewSentenceSplitter(onSentence func(string)) *SentenceSplitter {
	return &SentenceSplitter{OnSentence: onSentence}
}

// Feed adds a delta token to the buffer and checks for sentence boundaries.
// When a boundary is found, all complete sentences up to that point are emitted
// and the remainder stays in the buffer for the next Feed call.
func (s *SentenceSplitter) Feed(delta string) {
	s.buffer.WriteString(delta)
	text := s.buffer.String()

	// Scan for the last sentence boundary in the accumulated buffer.
	// Using LastIndex (not first) ensures we emit as much complete text as possible
	// in a single callback, reducing the number of TTS API calls.
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
