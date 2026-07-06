package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/zendev-sh/goai/provider"

	"github.com/mfranz/elastic-security-mcp/internal/agent"
	goai "github.com/zendev-sh/goai"
)

// normalizeToolResultText moved to internal/agent; see
// TestNormalizeToolResultText there.

func TestModelProvider(t *testing.T) {
	tests := []struct {
		modelName string
		want      string
	}{
		{"gpt-5", "openai"},
		{"gpt-5-mini", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"claude-sonnet-4-6", "anthropic"},
		{"gemini-2.0-flash", "gemini"},
		{"llama-3", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := modelProvider(tt.modelName); got != tt.want {
			t.Errorf("modelProvider(%q) = %q, want %q", tt.modelName, got, tt.want)
		}
	}
}

func newTestModel() model {
	ti := textinput.New()
	return model{
		histIndex: -1,
		textInput: ti,
	}
}

func TestPushInputHistoryDedupesConsecutiveEntries(t *testing.T) {
	t.Setenv("CLIENT_HISTORY_FILE", filepath.Join(t.TempDir(), "history"))
	m := newTestModel()

	m.pushInputHistory("first query")
	m.pushInputHistory("first query")
	m.pushInputHistory("second query")

	want := []string{"first query", "second query"}
	if len(m.inputHist) != len(want) {
		t.Fatalf("inputHist = %#v, want %#v", m.inputHist, want)
	}
	for i, w := range want {
		if m.inputHist[i] != w {
			t.Errorf("inputHist[%d] = %q, want %q", i, m.inputHist[i], w)
		}
	}
}

func TestPushInputHistoryIgnoresBlankInput(t *testing.T) {
	t.Setenv("CLIENT_HISTORY_FILE", filepath.Join(t.TempDir(), "history"))
	m := newTestModel()

	m.pushInputHistory("   ")

	if len(m.inputHist) != 0 {
		t.Fatalf("inputHist = %#v, want empty", m.inputHist)
	}
}

func TestBrowseHistoryPreservesDraftAndBoundaries(t *testing.T) {
	m := newTestModel()
	m.inputHist = []string{"first", "second", "third"}
	m.textInput.SetValue("in-progress draft")

	// Moving back from a fresh (non-browsing) state should stash the
	// current input as the draft and jump to the most recent history entry.
	m.browseHistory(-1)
	if got := m.textInput.Value(); got != "third" {
		t.Fatalf("after first browseHistory(-1), input = %q, want %q", got, "third")
	}
	if m.histDraft != "in-progress draft" {
		t.Fatalf("histDraft = %q, want %q", m.histDraft, "in-progress draft")
	}

	// Continue back to the oldest entry, then verify the top boundary clamps
	// rather than wrapping or going out of range.
	m.browseHistory(-1)
	m.browseHistory(-1)
	if got := m.textInput.Value(); got != "first" {
		t.Fatalf("at oldest entry, input = %q, want %q", got, "first")
	}
	m.browseHistory(-1)
	if got := m.textInput.Value(); got != "first" {
		t.Fatalf("browsing past oldest entry, input = %q, want clamped %q", got, "first")
	}

	// Moving forward past the newest entry should restore the stashed draft.
	m.browseHistory(1)
	m.browseHistory(1)
	m.browseHistory(1)
	if got := m.textInput.Value(); got != "in-progress draft" {
		t.Fatalf("after browsing past newest entry, input = %q, want restored draft %q", got, "in-progress draft")
	}
	if m.histIndex != -1 {
		t.Fatalf("histIndex = %d, want -1 after draft restore", m.histIndex)
	}
}

func TestBrowseHistoryNoOpWhenEmpty(t *testing.T) {
	m := newTestModel()
	m.textInput.SetValue("untouched")

	m.browseHistory(-1)

	if got := m.textInput.Value(); got != "untouched" {
		t.Fatalf("input = %q, want unchanged %q", got, "untouched")
	}
}

func TestPruneHistoryRetainsNewestMessages(t *testing.T) {
	m := newTestModel()
	for i := 0; i < maxHistoryMessages+5; i++ {
		m.history = append(m.history, goai.UserMessage(string(rune('a'+i%26))))
	}

	m.pruneHistory()

	if len(m.history) != maxHistoryMessages {
		t.Fatalf("len(history) = %d, want %d", len(m.history), maxHistoryMessages)
	}
	// The retained slice should be the tail of the original, i.e. the newest
	// maxHistoryMessages entries survive.
	wantFirstText := textOf(t, m.history[0])
	if wantFirstText != string(rune('a'+5%26)) {
		t.Fatalf("history[0] text = %q, want %q (oldest surviving entry)", wantFirstText, string(rune('a'+5%26)))
	}
}

func TestPruneHistoryNoOpUnderLimit(t *testing.T) {
	m := newTestModel()
	m.history = []provider.Message{goai.UserMessage("only one")}

	m.pruneHistory()

	if len(m.history) != 1 {
		t.Fatalf("len(history) = %d, want 1", len(m.history))
	}
}

func textOf(t *testing.T, msg provider.Message) string {
	t.Helper()
	for _, part := range msg.Content {
		if part.Type == provider.PartText {
			return part.Text
		}
	}
	t.Fatalf("message has no text part: %#v", msg)
	return ""
}

func TestFormatToolCallArgumentsEmptyInput(t *testing.T) {
	if got := formatToolCallArguments(provider.ToolCall{}); got != "{}" {
		t.Fatalf("formatToolCallArguments(empty) = %q, want %q", got, "{}")
	}
}

func TestFormatToolCallArgumentsMalformedJSONFallsBackToRaw(t *testing.T) {
	tc := provider.ToolCall{Name: "search_elastic", Input: json.RawMessage(`not json`)}
	if got := formatToolCallArguments(tc); got != "not json" {
		t.Fatalf("formatToolCallArguments(malformed) = %q, want raw input passthrough", got)
	}
}

func TestFormatToolCallArgumentsExpandsNestedJSONString(t *testing.T) {
	tc := provider.ToolCall{
		Name:  "search_elastic",
		Input: json.RawMessage(`{"index":"logs-*","query":"{\"match_all\":{}}"}`),
	}
	got := formatToolCallArguments(tc)

	if !strings.Contains(got, `"match_all"`) {
		t.Fatalf("formatToolCallArguments = %q, want the nested query JSON string expanded into structured JSON", got)
	}
	if strings.Contains(got, `\"match_all\"`) {
		t.Fatalf("formatToolCallArguments = %q, still contains escaped nested JSON, want it expanded", got)
	}
}

func TestFormatToolCallArgumentsToleratesMissingToolName(t *testing.T) {
	// formatToolCallArguments never reads tc.Name, so an empty name must not
	// affect formatting of the arguments themselves.
	tc := provider.ToolCall{Input: json.RawMessage(`{"index":"logs-*"}`)}
	if got := formatToolCallArguments(tc); !strings.Contains(got, `"index": "logs-*"`) {
		t.Fatalf("formatToolCallArguments(no name) = %q, want it to still format the index field", got)
	}
}

func TestToolPanelUpsertPairsStartAndEnd(t *testing.T) {
	m := newTestModel()
	call := provider.ToolCall{
		ID:    "call-1",
		Name:  "search_security_events",
		Input: json.RawMessage(`{"index":"logs-*","ip":"1.2.3.4"}`),
	}

	m.handleAgentEvent(agent.Event{
		Kind: agent.EventToolStart,
		Tool: &agent.ToolCallEvent{
			Call:  call,
			Seq:   1,
			Args:  map[string]any{"index": "logs-*", "ip": "1.2.3.4"},
			State: "running",
		},
	})
	m.handleAgentEvent(agent.Event{
		Kind: agent.EventToolEnd,
		Tool: &agent.ToolCallEvent{
			Call:     call,
			Seq:      1,
			Args:     map[string]any{"index": "logs-*", "ip": "1.2.3.4"},
			State:    "completed",
			Result:   "found 3 matching events",
			IsCached: true,
		},
	})

	if len(m.toolPanel) != 1 {
		t.Fatalf("len(toolPanel) = %d, want 1 paired item", len(m.toolPanel))
	}
	item := m.toolPanel[0]
	if item.state != "completed" {
		t.Fatalf("tool state = %q, want completed", item.state)
	}
	if !item.isCached {
		t.Fatal("tool item should record cache hit")
	}
	if item.resultPreview != "found 3 matching events" {
		t.Fatalf("resultPreview = %q, want result preview", item.resultPreview)
	}
	if m.toolCalls != 1 || m.cacheHits != 1 || m.cacheMisses != 0 {
		t.Fatalf("counters = calls:%d hits:%d misses:%d, want calls:1 hits:1 misses:0", m.toolCalls, m.cacheHits, m.cacheMisses)
	}
}

func TestToolPanelUsesNewestFirstAndCapsItems(t *testing.T) {
	m := newTestModel()

	for i := 0; i < maxToolPanelItems+3; i++ {
		call := provider.ToolCall{
			ID:    "call-" + string(rune('a'+i)),
			Name:  "lookup_ip",
			Input: json.RawMessage(`{"ip":"1.2.3.4"}`),
		}
		m.upsertToolPanelItem(m.startToolPanelID(&agent.ToolCallEvent{Call: call, Seq: i + 1}), &agent.ToolCallEvent{
			Call:  call,
			Seq:   i + 1,
			Args:  map[string]any{"ip": "1.2.3.4"},
			State: "running",
		})
	}

	if len(m.toolPanel) != maxToolPanelItems {
		t.Fatalf("len(toolPanel) = %d, want cap %d", len(m.toolPanel), maxToolPanelItems)
	}
	if m.toolPanel[0].seq != maxToolPanelItems+3 {
		t.Fatalf("newest seq = %d, want %d", m.toolPanel[0].seq, maxToolPanelItems+3)
	}
}

func TestToolPanelWidthOnlyForWideTerminals(t *testing.T) {
	if got := toolPanelWidthForTerminal(100); got != 0 {
		t.Fatalf("toolPanelWidthForTerminal(100) = %d, want disabled panel", got)
	}
	if got := toolPanelWidthForTerminal(120); got < minToolPanelWidth || got > maxToolPanelWidth {
		t.Fatalf("toolPanelWidthForTerminal(120) = %d, want within configured bounds", got)
	}
}

func TestBuildMarkdownExport(t *testing.T) {
	exportedAt := time.Date(2026, 5, 5, 9, 30, 0, 0, time.UTC)
	conversation := []exportMessage{
		{role: "user", content: "Find suspicious logins"},
		{role: "assistant", content: "No suspicious logins found."},
		{role: "system", content: "Conversation Memory:\n(empty)"},
	}

	got := buildMarkdownExport(conversation, exportedAt)

	wantParts := []string{
		"# Elastic Security Investigation Export",
		"*Exported on: Tue, 05 May 2026 09:30:00 UTC*",
		"**You:**\nFind suspicious logins",
		"**Assistant:**\nNo suspicious logins found.",
		"**System:**\nConversation Memory:\n(empty)",
	}

	for _, part := range wantParts {
		if !strings.Contains(got, part) {
			t.Fatalf("export missing %q\nfull export:\n%s", part, got)
		}
	}
}

func TestExportFilename(t *testing.T) {
	got := exportFilename(time.Date(2026, 5, 5, 9, 30, 45, 0, time.UTC))
	want := "investigation-export-2026-05-05T09-30-45.md"
	if got != want {
		t.Fatalf("filename = %q, want %q", got, want)
	}
}

func TestNormalizeMarkdownForTerminal(t *testing.T) {
	input := "### Key Observations\nNormal line\n  ## Recommendation\n- bullet"
	got := normalizeMarkdownForTerminal(input)
	want := "Key Observations\nNormal line\n  Recommendation\n- bullet"
	if got != want {
		t.Fatalf("normalized markdown = %q, want %q", got, want)
	}
}
