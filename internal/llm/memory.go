package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

// ConversationBuffer stores conversational history in memory.
// This replaces github.com/tmc/langchaingo/memory.ConversationBuffer.
type ConversationBuffer struct {
	mu       sync.Mutex
	messages []anyllm.Message
}

// NewConversationBuffer creates a new ConversationBuffer.
func NewConversationBuffer() *ConversationBuffer {
	return &ConversationBuffer{
		messages: make([]anyllm.Message, 0),
	}
}

// SaveContext saves context to history.
func (cb *ConversationBuffer) SaveContext(ctx context.Context, input map[string]any, output map[string]any) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	inStr, _ := input["input"].(string)
	outStr, _ := output["output"].(string)

	if inStr != "" {
		cb.messages = append(cb.messages, anyllm.Message{
			Role:    anyllm.RoleUser,
			Content: inStr,
		})
	}
	if outStr != "" {
		cb.messages = append(cb.messages, anyllm.Message{
			Role:    anyllm.RoleAssistant,
			Content: outStr,
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
		if msg.Role == anyllm.RoleAssistant {
			roleLabel = "AI"
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", roleLabel, msg.ContentString()))
	}

	return map[string]any{
		"history": sb.String(),
	}, nil
}
