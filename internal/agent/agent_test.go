package agent

import (
	"encoding/json"
	"strings"
	"testing"

	goaimcp "github.com/zendev-sh/goai/mcp"
	"github.com/zendev-sh/goai/provider"
)

func TestNormalizeToolResultText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantText string
		cached   bool
		stored   bool
	}{
		{
			name:     "cache hit prefix is removed",
			input:    "✓ cached result",
			wantText: "cached result",
			cached:   true,
		},
		{
			name:     "cache store prefix is removed",
			input:    "↓ fresh result",
			wantText: "fresh result",
			stored:   true,
		},
		{
			name:     "plain result is untouched",
			input:    "plain result",
			wantText: "plain result",
		},
		{
			name:     "cache hit marker requires trailing space",
			input:    "✓cached result",
			wantText: "✓cached result",
		},
		{
			name:     "cache store marker requires trailing space",
			input:    "↓fresh result",
			wantText: "↓fresh result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotText, gotCached, gotStored := normalizeToolResultText(tt.input)
			if gotText != tt.wantText {
				t.Fatalf("text = %q, want %q", gotText, tt.wantText)
			}
			if gotCached != tt.cached {
				t.Fatalf("cached = %v, want %v", gotCached, tt.cached)
			}
			if gotStored != tt.stored {
				t.Fatalf("stored = %v, want %v", gotStored, tt.stored)
			}
		})
	}
}

func TestSummarizeToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls []provider.ToolCall
		wantWords []string
	}{
		{
			name:      "Empty tool calls",
			toolCalls: []provider.ToolCall{},
			wantWords: []string{"Waiting"},
		},
		{
			name: "Single tool call",
			toolCalls: []provider.ToolCall{
				{Name: "search"},
			},
			wantWords: []string{"Running", "search"},
		},
		{
			name: "Two tool calls",
			toolCalls: []provider.ToolCall{
				{Name: "search"},
				{Name: "lookup_ip"},
			},
			wantWords: []string{"Running", "search", "lookup_ip"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeToolCalls(tt.toolCalls)
			for _, word := range tt.wantWords {
				if !strings.Contains(result, word) {
					t.Errorf("Expected %q to contain %q", result, word)
				}
			}
		})
	}
}

func TestExtractToolText(t *testing.T) {
	if got := extractToolText(nil); got != "" {
		t.Errorf("extractToolText(nil) = %q, want %q", got, "")
	}
}

func TestExtractToolTextJoinsTextBlocks(t *testing.T) {
	toolResp := &goaimcp.CallToolResult{
		Content: []goaimcp.ContentBlock{
			json.RawMessage(`{"type":"text","text":"first block"}`),
			json.RawMessage(`{"type":"image","data":"ignored"}`),
			json.RawMessage(`{"type":"text","text":"second block"}`),
			json.RawMessage(`not json`),
		},
	}

	if got, want := extractToolText(toolResp), "first block\nsecond block"; got != want {
		t.Errorf("extractToolText() = %q, want %q", got, want)
	}
}

func TestIsStalling(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"I will search for that now.", true},
		{"Let me check the logs.", true},
		{"Now I'll look into it.", true},
		{"Searching for related events.", true},
		{"LET ME CHECK THE LOGS.", true},
		{"Here are the results you requested.", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isStalling(tt.text); got != tt.want {
			t.Errorf("isStalling(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestRenderHistoryText(t *testing.T) {
	history := []provider.Message{
		goaiUserMessage("find suspicious logins"),
		goaiAssistantMessage("I found two suspicious logins."),
	}
	got := RenderHistoryText(history)
	if !strings.Contains(got, "Human: find suspicious logins") {
		t.Errorf("RenderHistoryText() = %q, want it to contain the human line", got)
	}
	if !strings.Contains(got, "AI: I found two suspicious logins.") {
		t.Errorf("RenderHistoryText() = %q, want it to contain the assistant line", got)
	}
}

func TestSummarizeHistoryForLog(t *testing.T) {
	history := []provider.Message{
		goaiUserMessage(strings.Repeat("a", 200)),
		{
			Role: provider.RoleAssistant,
			Content: []provider.Part{
				{
					Type:       provider.PartToolCall,
					ToolCallID: "call-1",
					ToolName:   "search_security_events",
					ToolInput:  json.RawMessage(`{"index":"logs-*","text":"malware"}`),
				},
			},
		},
		{
			Role: provider.RoleTool,
			Content: []provider.Part{
				{
					Type:       provider.PartToolResult,
					ToolCallID: "call-1",
					ToolOutput: "tool output",
				},
			},
		},
	}

	got := summarizeHistoryForLog(history)
	for _, want := range []string{
		`"role":"user"`,
		`"type":"text"`,
		`...(truncated)`,
		`"role":"assistant"`,
		`"type":"tool_call"`,
		`"name":"search_security_events"`,
		`"id":"call-1"`,
		`"role":"tool"`,
		`"type":"tool_result"`,
		`"tool_call_id":"call-1"`,
		`"preview":"tool output"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summarizeHistoryForLog() = %s, want it to contain %s", got, want)
		}
	}
}

// goaiUserMessage avoids importing the goai package just for one helper in
// this test file's setup.
func goaiUserMessage(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Part{{Type: provider.PartText, Text: text}},
	}
}

func goaiAssistantMessage(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Part{{Type: provider.PartText, Text: text}},
	}
}
