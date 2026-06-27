// Package conv holds shared helpers for working with the conversation
// message history that both the CLI (cmd/cli) and the Web UI (internal/webui)
// maintain as a []provider.Message slice. Centralizing these avoids the
// duplicated rendering/pruning logic that previously lived in both consumers.
package conv

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zendev-sh/goai/provider"

	"github.com/mfranz/elastic-security-mcp/internal/util"
)

// MaxMessages is the sliding-window size used when conversation memory is
// disabled. Both consumers prune to the last MaxMessages messages.
const MaxMessages = 15

// Prune returns a sliding window of at most max trailing messages. It is a pure
// function: it never mutates the input slice and returns it unchanged when it is
// already within bounds (or when max <= 0).
func Prune(history []provider.Message, max int) []provider.Message {
	if max <= 0 || len(history) <= max {
		return history
	}
	return history[len(history)-max:]
}

// RenderText renders the textual turns of a conversation as a simple
// "Human:/AI:" transcript. Non-text parts (tool calls/results) are omitted; this
// is intended for the `/memory` command, not for replaying full state.
func RenderText(history []provider.Message) string {
	var sb strings.Builder
	for _, msg := range history {
		for _, p := range msg.Content {
			if p.Type == provider.PartText && p.Text != "" {
				role := "Human"
				if msg.Role == provider.RoleAssistant {
					role = "AI"
				}
				sb.WriteString(fmt.Sprintf("%s: %s\n", role, p.Text))
			}
		}
	}
	return sb.String()
}

// SummarizeForLog produces a compact JSON summary of the history suitable for
// structured logging: it records roles and per-part type/size with truncated
// previews instead of dumping full payloads.
func SummarizeForLog(history []provider.Message) string {
	type partSummary map[string]any
	type messageSummary map[string]any

	summary := make([]messageSummary, 0, len(history))
	for i, msg := range history {
		var parts []partSummary
		for _, p := range msg.Content {
			switch p.Type {
			case provider.PartText:
				parts = append(parts, partSummary{
					"type":    "text",
					"chars":   len(p.Text),
					"preview": util.TruncateForLog(p.Text, 160),
				})
			case provider.PartToolCall:
				parts = append(parts, partSummary{
					"type":      "tool_call",
					"name":      p.ToolName,
					"id":        p.ToolCallID,
					"arg_chars": len(p.ToolInput),
					"args":      util.TruncateForLog(string(p.ToolInput), 240),
				})
			case provider.PartToolResult:
				parts = append(parts, partSummary{
					"type":         "tool_result",
					"tool_call_id": p.ToolCallID,
					"chars":        len(p.ToolOutput),
					"preview":      util.TruncateForLog(p.ToolOutput, 240),
				})
			}
		}
		summary = append(summary, messageSummary{
			"index": i,
			"role":  string(msg.Role),
			"parts": parts,
		})
	}
	b, err := json.Marshal(summary)
	if err != nil {
		return fmt.Sprintf("failed to summarize history: %v", err)
	}
	return string(b)
}
