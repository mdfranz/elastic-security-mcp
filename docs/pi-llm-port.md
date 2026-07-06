# Porting Plan: Swapping `any-llm-go` for `pi-llm-go` in `elastic-security-mcp`

This document details the plan to migrate the LLM orchestration layer in [elastic-security-mcp](file:///home/mdfranz/github/elastic-security-mcp) from the current dependency [any-llm-go](file:///home/mdfranz/github/any-llm-go) to the minimal, provider-agnostic LLM client [pi-llm-go](file:///home/mdfranz/github/pi-llm-go).

---

## Architectural Mapping: `any-llm-go` vs. `pi-llm-go`

| Concept | `any-llm-go` (Current) | `pi-llm-go` (Target) | Notes |
| :--- | :--- | :--- | :--- |
| **Go Module** | `github.com/mozilla-ai/any-llm-go` | `github.com/amit-timalsina/pi-llm-go` | Swap module dependencies in `go.mod`. |
| **Model Interface** | `anyllm.Provider` | `llm.LLM` | Provider-agnostic streaming interface. |
| **Completion call** | `provider.Completion(ctx, anyllm.CompletionParams{...})` returns `*anyllm.ChatCompletion` | `llm.Complete(ctx, provider, llm.Request{...})` returns `*llm.Message` | No `Choices` wrapper — `Complete` returns the assistant message directly. |
| **Message** | `anyllm.Message` with `Content string` | `llm.Message` with `Content []llm.Block` | Content is a typed block slice, not a flat string. |
| **Message Roles** | `RoleUser`, `RoleAssistant`, `RoleSystem`, `RoleTool` | `RoleUser`, `RoleAssistant`, `RoleTool` | No `RoleSystem` — system prompts go on `llm.Request.System`. |
| **Tools Definition** | `anyllm.Tool` with nested `Function` type | `llm.Tool` with flat `Name`, `Description`, `InputSchema json.RawMessage` | |
| **Tool Call Output** | `anyllm.ToolCall` inside `Message.ToolCalls` | `llm.ToolCallBlock` inside `Message.Content` | Tool calls are content blocks, not a separate field. |
| **Tool Result Input** | `anyllm.Message` with `RoleTool` & `ToolCallID` | `llm.Message` with `RoleTool` & `llm.ToolResultBlock` in `Content` | Tool results are `ToolResultBlock`s inside `Message.Content`. |
| **MaxTokens** | `*int` (pointer) | `int` (plain value) | Do not use `&maxTokens4096`. |

---

## Step-by-Step Porting Steps

### Step 1: Update Go Module Dependencies

`pi-llm-go` is already cloned at `/home/mdfranz/github/pi-llm-go`.

1. Remove:
   ```go
   github.com/mozilla-ai/any-llm-go v0.0.0
   replace github.com/mozilla-ai/any-llm-go => /home/mdfranz/github/any-llm-go
   ```
2. Add:
   ```go
   github.com/amit-timalsina/pi-llm-go v1.0.0
   replace github.com/amit-timalsina/pi-llm-go => /home/mdfranz/github/pi-llm-go
   ```
3. Run `go mod tidy`.

---

### Step 2: Adapt Conversational Memory (`internal/llm/memory.go`)

Update `ConversationBuffer` to use `llm.Message` and `llm.TextBlock`:

```go
package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	llm "github.com/amit-timalsina/pi-llm-go"
)

type ConversationBuffer struct {
	mu       sync.Mutex
	messages []llm.Message
}

func NewConversationBuffer() *ConversationBuffer {
	return &ConversationBuffer{messages: make([]llm.Message, 0)}
}

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
	return map[string]any{"history": sb.String()}, nil
}
```

---

### Step 3: Refactor the CLI Client (`cmd/cli/main.go`)

#### 3.1 Swap Packages & Message Types

Change all `anyllm.*` references to `llm.*` equivalents:
- `anyllm.Provider` → `llm.LLM`
- `anyllm.Tool` → `llm.Tool`
- `anyllm.Message` → `llm.Message`
- `anyllm.ToolCall` → `llm.ToolCallBlock`
- The `anyTools` field name on `model` struct → `tools`

#### 3.2 Update Provider Initialization

```go
import (
	llm "github.com/amit-timalsina/pi-llm-go"
	"github.com/amit-timalsina/pi-llm-go/providers/anthropic"
	"github.com/amit-timalsina/pi-llm-go/providers/gemini"
	"github.com/amit-timalsina/pi-llm-go/providers/openai"
)

switch modelProvider(modelName) {
case "openai":
	llmClient, err = openai.New(openai.Options{APIKey: openaiKey})
case "anthropic":
	llmClient, err = anthropic.New(anthropic.Options{APIKey: anthropicKey})
case "gemini":
	llmClient, err = gemini.New(gemini.Options{APIKey: geminiKey})
}
```

#### 3.3 Write Content-Block Helpers

Add these to replace `anyllm`'s flat `.ContentString()` and `.ToolCalls` field:

```go
func messageText(msg llm.Message) string {
	var sb strings.Builder
	for _, block := range msg.Content {
		if tb, ok := block.(llm.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

func messageToolCalls(msg llm.Message) []llm.ToolCallBlock {
	var calls []llm.ToolCallBlock
	for _, block := range msg.Content {
		if tc, ok := block.(llm.ToolCallBlock); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}
```

Replace every `choice.Message.ContentString()` with `messageText(resp)` and every `choice.Message.ToolCalls` with `messageToolCalls(resp)` throughout.

#### 3.4 Update the TUI Message Types

`llmResponseMsg` currently holds `*anyllm.ChatCompletion`. Change it to hold `*llm.Message` directly — `Complete` returns the assistant message, not a choices wrapper:

```go
type llmResponseMsg struct {
	resp *llm.Message  // was *anyllm.ChatCompletion
}

type executeToolsMsg struct {
	toolCalls []llm.ToolCallBlock  // was []anyllm.ToolCall
}

type toolsResultMsg struct {
	results  []llm.Message
	outcomes []toolOutcome
}
```

All `choice := msg.resp.Choices[0]` sites become `resp := msg.resp` and `choice.Message.*` becomes `resp.*`.

#### 3.5 Update the Tool Schema Conversion

Replace the `anyllm.Tool{Type: "function", Function: anyllm.Function{...}}` construction with the flat `llm.Tool`:

```go
tools := make([]llm.Tool, 0, len(toolsResult.Tools))
for _, t := range toolsResult.Tools {
	schemaBytes, _ := json.Marshal(t.InputSchema)
	tools = append(tools, llm.Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: json.RawMessage(schemaBytes),
	})
}
```

**Decision on `convertSchema`**: the current `convertSchema` function injects `"properties": {}` for object schemas that have none. Check whether `pi-llm-go` providers require this guard; if not, drop `convertSchema` entirely. If the guard is still needed, apply it after `json.Marshal` before constructing `InputSchema`.

#### 3.6 Update the LLM Request / Response

`MaxTokens` changed from `*int` to `int`. System prompt moves off the history slice and onto `Request.System`. History must contain only `RoleUser`, `RoleAssistant`, and `RoleTool` messages.

```go
var tempZero = 0.0

resp, err := util.WithRetry(ctx, func() (*llm.Message, error) {
	return llm.Complete(ctx, llmClient, llm.Request{
		Model:       modelName,
		System:      systemPrompt,
		Messages:    m.history,     // no system message in this slice
		Tools:       m.tools,
		Temperature: &tempZero,
		MaxTokens:   4096,          // plain int, not &maxTokens4096
	})
})
```

Update `util.WithRetry`'s generic type instantiation accordingly — it wraps `func() (*llm.Message, error)` now instead of `func() (*anyllm.ChatCompletion, error)`.

#### 3.7 Update History Initialization

Remove the system-message entry from history — the system prompt now lives on `Request.System`:

```go
// before
history: []anyllm.Message{{Role: anyllm.RoleSystem, Content: systemPrompt}}

// after
history: []llm.Message{}
```

#### 3.8 Fix `pruneHistory`

The current implementation preserves `m.history[0]` as the system message:

```go
// BROKEN after migration — history[0] is now the first user message
pruned := []anyllm.Message{m.history[0]}
pruned = append(pruned, m.history[len(m.history)-maxHistoryMessages+1:]...)
```

After migration there is no special first message, so drop the index-0 guard:

```go
func (m *model) pruneHistory() {
	if len(m.history) <= maxHistoryMessages {
		return
	}
	m.history = m.history[len(m.history)-maxHistoryMessages:]
}
```

#### 3.9 Update Tool Execution

Tool call fields change: `tc.Function.Name` → `tc.Name`, `tc.Function.Arguments` → `tc.Arguments` (a `json.RawMessage`), and `tc.ID` stays the same.

```go
func (m model) executeTools(toolCalls []llm.ToolCallBlock) tea.Cmd {
	return func() tea.Msg {
		var results []llm.Message
		var outcomes []toolOutcome
		for _, tc := range toolCalls {
			var args map[string]any
			if err := json.Unmarshal(tc.Arguments, &args); err != nil {
				results = append(results, llm.Message{
					Role: llm.RoleTool,
					Content: []llm.Block{llm.ToolResultBlock{
						ToolCallID: tc.ID,
						Content:    fmt.Sprintf("invalid tool arguments: %v", err),
						IsError:    true,
					}},
				})
				continue
			}
			toolResp, err := m.mcpSession.CallTool(m.ctx, &mcp.CallToolParams{
				Name:      tc.Name,
				Arguments: args,
			})
			resultText, isCached, isStored := normalizeToolResultText(extractToolContent(toolResp))
			isError := err != nil || (toolResp != nil && toolResp.IsError)
			results = append(results, llm.Message{
				Role: llm.RoleTool,
				Content: []llm.Block{llm.ToolResultBlock{
					ToolCallID: tc.ID,
					Content:    resultText,
					IsError:    isError,
				}},
			})
			outcomes = append(outcomes, toolOutcome{isCached: isCached, isStored: isStored, isError: isError})
		}
		return toolsResultMsg{results: results, outcomes: outcomes}
	}
}
```

#### 3.10 Rewrite `summarizeHistoryForLog`

This function (`main.go:853-901`) directly accesses `msg.ToolCalls`, `tc.Function.Name`, `tc.Function.Arguments`, `msg.Name`, `msg.ToolCallID` — all `anyllm`-specific fields. It needs a full rewrite using type-switches over `msg.Content []Block`:

```go
func summarizeHistoryForLog(history []llm.Message) string {
	type partSummary map[string]any
	type messageSummary map[string]any

	summary := make([]messageSummary, 0, len(history))
	for i, msg := range history {
		var parts []partSummary
		for _, block := range msg.Content {
			switch b := block.(type) {
			case llm.TextBlock:
				parts = append(parts, partSummary{
					"type":    "text",
					"chars":   len(b.Text),
					"preview": util.TruncateForLog(b.Text, 160),
				})
			case llm.ToolCallBlock:
				parts = append(parts, partSummary{
					"type":      "tool_call",
					"name":      b.Name,
					"id":        b.ID,
					"arg_chars": len(b.Arguments),
					"args":      util.TruncateForLog(string(b.Arguments), 240),
				})
			case llm.ToolResultBlock:
				parts = append(parts, partSummary{
					"type":         "tool_response",
					"tool_call_id": b.ToolCallID,
					"chars":        len(b.Content),
					"preview":      util.TruncateForLog(b.Content, 240),
				})
			}
		}
		summary = append(summary, messageSummary{
			"index": i,
			"role":  msg.Role,
			"parts": parts,
		})
	}
	b, err := json.Marshal(summary)
	if err != nil {
		return fmt.Sprintf("failed to summarize history: %v", err)
	}
	return string(b)
}
```

Also update `summarizeToolCalls` to take `[]llm.ToolCallBlock` and access `tc.Name` instead of `tc.Function.Name`.

---

### Step 4: Refactor Web UI Server (`internal/webui/server.go`)

#### 4.1 Update Struct and Function Signatures

- `Server.llmClient anyllm.Provider` → `llm.LLM`
- `Server.anyTools []anyllm.Tool` → `tools []llm.Tool`
- `RunServer` parameters updated to match

#### 4.2 Delete `newConversationHistory()`

This function initializes history with a `RoleSystem` message, which `pi-llm-go` does not support in the message list. Delete the function. In `handleWebSocket`, initialize history as an empty slice:

```go
history := []llm.Message{}
```

Pass `systemPrompt` via `Request.System` in `processConversation`, exactly as in Step 3.6.

#### 4.3 Update `processConversation`

Apply the same changes as the CLI (Step 3):
- Replace `s.llmClient.Completion(...)` with `llm.Complete(...)` using the `Request.System` field for the prompt
- Replace `choice.Message.ContentString()` with `messageText(*resp)`
- Replace `len(choice.Message.ToolCalls)` with `len(messageToolCalls(*resp))`
- Replace tool-result message construction with `ToolResultBlock` in `Content`
- Use plain `int` for `MaxTokens`

#### 4.4 Update `summarizeToolCalls`

Change signature from `[]anyllm.ToolCall` to `[]llm.ToolCallBlock` and replace `tc.Function.Name` with `tc.Name`.

#### 4.5 Note on `systemPrompt` Divergence

`cmd/cli/main.go` and `internal/webui/server.go` define `systemPrompt` as separate constants with slightly different content. This pre-existing divergence is unrelated to the migration but worth resolving — consider moving the canonical prompt to a shared package (e.g., `internal/llm/prompt.go`) during this refactor.

---

### Step 5: Update Tests

#### `internal/webui/server_test.go`

**`TestSummarizeToolCalls`** — constructs `anyllm.ToolCall{Function: anyllm.FunctionCall{Name: "search"}}` directly. After migration `summarizeToolCalls` takes `[]llm.ToolCallBlock`. Rewrite to use `llm.ToolCallBlock{Name: "search"}`.

**`TestConversationHistoryInitialization`** — tests that `newConversationHistory()` returns 1 message with `RoleSystem`. After migration `newConversationHistory` is deleted and history initializes empty. Delete this test (or replace with a trivial check that the initial history slice is empty).

**`setupTestServer`** — `anyTools: []anyllm.Tool{}` → `tools: []llm.Tool{}`.

#### `cmd/cli/main_test.go`

No changes needed. `TestNormalizeToolResultText`, `TestBuildMarkdownExport`, `TestExportFilename`, and `TestNormalizeMarkdownForTerminal` do not reference `anyllm` and will compile and pass unchanged.

---

## Verification & Testing Plan

1. **Local Build**:
   ```bash
   go build ./cmd/cli/main.go
   go build ./cmd/server/main.go
   go test ./internal/...
   ```
2. **Execute Model Test Script**:
   ```bash
   ./test-models.sh
   ```
   Confirm that OpenAI, Anthropic, and Gemini models can all register tools, receive prompts, and return correctly formatted answers.
3. **Web UI Validation**:
   ```bash
   ./elastic-cli --webui
   ```
   Open the interface in a browser and verify that conversational states, statuses, tool logs, and outputs load successfully over WebSockets.

---

## Opportunities: Exploiting `pi-llm-go` Features

After completing the raw port, you can implement the following enhancements:

1. **Token-Aware Pruning**: Switch from a message-count sliding window (15 messages) to exact token counts using the `llm.TokenCounter` interface.
2. **Reasoning Trace Visibility**: Extract `llm.ThinkingBlock` parts from Claude/Gemini responses and display them in the Web UI or TUI, replacing the generic "Thinking..." placeholder.
3. **Cost Projection**: Display session cost using `llm.ComputeCost`.
4. **Prompt Caching**: Set `Request.CacheRetention = llm.CacheRetentionShort` for Anthropic models to reduce latency and cost on repeated system prompts.
