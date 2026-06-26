package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	llm "github.com/amit-timalsina/pi-llm-go"
)

// ConversationBuffer stores conversational history in memory.
// This replaces github.com/tmc/langchaingo/memory.ConversationBuffer.
type ConversationBuffer struct {
	mu       sync.Mutex
	messages []llm.Message
}

// NewConversationBuffer creates a new ConversationBuffer.
func NewConversationBuffer() *ConversationBuffer {
	return &ConversationBuffer{
		messages: make([]llm.Message, 0),
	}
}

// SaveContext saves context to history.
func (cb *ConversationBuffer) SaveContext(ctx context.Context, input map[string]any, output map[string]any) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	inStr, _ := input["input"].(string)
	outStr, _ := output["output"].(string)

	if inStr != "" {
		cb.messages = append(cb.messages, llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.Block{llm.TextBlock{Text: inStr}},
		})
	}
	if outStr != "" {
		cb.messages = append(cb.messages, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.Block{llm.TextBlock{Text: outStr}},
		})
	}

	return nil
}

// LoadMemoryVariables loads variables from memory.
func (cb *ConversationBuffer) LoadMemoryVariables(ctx context.Context, _ map[string]any) (map[string]any, error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	var sb strings.Builder
	for _, msg := range cb.messages {
		roleLabel := "Human"
		if msg.Role == llm.RoleAssistant {
			roleLabel = "AI"
		}
		var text string
		for _, block := range msg.Content {
			if tb, ok := block.(llm.TextBlock); ok {
				text += tb.Text
			}
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", roleLabel, text))
	}

	return map[string]any{
		"history": sb.String(),
	}, nil
}
