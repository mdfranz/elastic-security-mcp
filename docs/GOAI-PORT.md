# Porting Plan: `pi-llm-go` → `goai`

## Overview

Replace the `github.com/amit-timalsina/pi-llm-go` dependency with
`github.com/zendev-sh/goai` (located at `../goai` relative to this repo).

**Files affected:**

| File | Nature of change |
|------|-----------------|
| `go.mod` | swap module, add replace directive |
| `cmd/cli/main.go` | provider init, types, tool loop elimination |
| `internal/webui/server.go` | same patterns as CLI |
| `internal/webui/server_test.go` | type references |
| `internal/llm/memory.go` | delete or rewrite |

---

## Key Architectural Shift: Built-in Tool Loop

The biggest simplification: goai's `GenerateText` with `WithMaxSteps(n)` runs
the tool execution loop automatically. The current code has **four separate
manual tool-call loops**:

- `generateResponse()` + `executeTools()` in the TUI model
- `runSinglePrompt()` loop
- `processConversation()` in webui

All four collapse into one `goai.GenerateText(ctx, model, opts...)` call.

A second simplification: goai's MCP sub-package (`github.com/zendev-sh/goai/mcp`)
ships `ConvertTools(client, mcpTools)` which wraps MCP tools into `goai.Tool`
values with Execute functions pre-wired. This removes all manual tool-dispatch
code (`toolCallName`, `toolCallArguments`, `extractToolContent`, etc.).

---

## Type Mapping

| `pi-llm-go` | `goai` |
|-------------|--------|
| `llm.LLM` | `provider.LanguageModel` |
| `llm.Message` | `provider.Message` |
| `llm.Block` (interface) | `provider.Part` (struct) |
| `llm.TextBlock{Text: t}` | `goai.UserMessage(t)` / `goai.AssistantMessage(t)` |
| `llm.RoleUser` | `provider.RoleUser` |
| `llm.RoleAssistant` | `provider.RoleAssistant` |
| `llm.RoleTool` | `provider.RoleTool` |
| `llm.ToolCallBlock` | `provider.Part{Type: provider.PartToolCall}` |
| `llm.ToolResultBlock` | `provider.Part{Type: provider.PartToolResult}` |
| `llm.Tool{Name,Desc,InputSchema}` | `goai.Tool{Name,Desc,InputSchema,Execute}` |
| `llm.Complete(ctx, client, req)` | `goai.GenerateText(ctx, model, opts...)` |
| `llm.Request{Model,System,Messages,Tools}` | `goai.WithMessages()`, `goai.WithSystem()`, `goai.WithTools()` |

---

## Step-by-Step Plan

### Step 1 — `go.mod`

Remove the `pi-llm-go` require/replace lines, add goai:

```
require (
    github.com/zendev-sh/goai v0.0.0-00010101000000-000000000000
    ...
)

replace github.com/zendev-sh/goai => /home/mdfranz/github/goai
```

Run `go mod tidy` after each file change or at the end.

---

### Step 2 — `go.sum`

Re-generated automatically by `go mod tidy`. No manual edits needed.

---

### Step 3 — `internal/llm/memory.go`

**Delete the file.** The `ConversationBuffer` was an adapter for pi-llm-go's
`Message` type used to produce a text `history` summary. In goai the message
slice (`[]provider.Message`) is the source of truth; the summary rendering
is not used in any production path (only the `/memory` slash command in the
TUI). Replace that command's output by rendering from the existing
`m.history []provider.Message` slice directly in the TUI `Update` handler.

Update `internal/llm/` imports throughout if anything else referenced the
package (only `cmd/cli/main.go` and `internal/webui/server.go` do).

---

### Step 4 — `cmd/cli/main.go` imports

Replace:

```go
llm "github.com/amit-timalsina/pi-llm-go"
"github.com/amit-timalsina/pi-llm-go/providers/anthropic"
"github.com/amit-timalsina/pi-llm-go/providers/gemini"
"github.com/amit-timalsina/pi-llm-go/providers/openai"
internalLlm "github.com/mfranz/elastic-security-mcp/internal/llm"
```

With:

```go
"github.com/zendev-sh/goai"
goaimcp "github.com/zendev-sh/goai/mcp"
"github.com/zendev-sh/goai/provider"
"github.com/zendev-sh/goai/provider/anthropic"
"github.com/zendev-sh/goai/provider/google"
"github.com/zendev-sh/goai/provider/openai"
```

Drop `"github.com/modelcontextprotocol/go-sdk/mcp"` if switching to goai's
MCP client (see Step 5). Keep it if you prefer the official SDK.

---

### Step 5 — MCP client choice (decision point)

**Option A — Use goai's built-in MCP client (recommended)**

goai ships `github.com/zendev-sh/goai/mcp` with `StdioTransport`, which is
equivalent to `mcp.CommandTransport` from the official SDK. The benefit is
`mcp.ConvertTools(client, mcpTools)` which auto-wires Execute for every tool.

Current setup (using official SDK):
```go
cmd := exec.Command(serverPath)
transport := &mcp.CommandTransport{Command: cmd}
client := mcp.NewClient(&mcp.Implementation{Name: "elastic-cli", Version: "1.0.0"}, nil)
session, _ := client.Connect(ctx, transport, nil)
toolsResult, _ := session.ListTools(ctx, nil)
```

goai MCP equivalent:
```go
client := goaimcp.NewClient("elastic-cli", "1.0.0",
    goaimcp.WithTransport(&goaimcp.StdioTransport{Command: serverPath}),
)
_ = client.Connect(ctx)
listResult, _ := client.ListTools(ctx, nil)
tools := goaimcp.ConvertTools(client, listResult.Tools)
```

**Option B — Keep the official SDK, build goai tools manually**

If you need to keep `go-sdk/mcp` for other reasons (e.g. the server-side MCP
in `cmd/server/`), manually construct `goai.Tool` values with an Execute
closure that calls `session.CallTool`:

```go
tools := make([]goai.Tool, 0, len(toolsResult.Tools))
for _, t := range toolsResult.Tools {
    t := t
    tools = append(tools, goai.Tool{
        Name:        t.Name,
        Description: t.Description,
        InputSchema: json.RawMessage(t.InputSchema),
        Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
            var args map[string]any
            _ = json.Unmarshal(input, &args)
            resp, err := session.CallTool(ctx, &mcp.CallToolParams{
                Name: t.Name, Arguments: args,
            })
            if err != nil {
                return "", err
            }
            return extractToolContent(resp), nil
        },
    })
}
```

This removes `convertSchema` and `cleanSchema` entirely since goai accepts
`json.RawMessage` directly from the MCP server's schema.

---

### Step 6 — Provider initialization

Replace the `modelProvider()` switch + `llm.LLM` construction:

```go
// Current
var llmClient llm.LLM
switch modelProvider(modelName) {
case "openai":
    llmClient, err = openai.New(openai.Options{APIKey: openaiKey})
case "anthropic":
    llmClient, err = anthropic.New(anthropic.Options{APIKey: anthropicKey})
case "gemini":
    llmClient, err = gemini.New(gemini.Options{APIKey: geminiKey})
}
```

```go
// goai — reads env vars automatically
var model provider.LanguageModel
switch modelProvider(modelName) {
case "openai":
    model = openai.Chat(modelName)
case "anthropic":
    model = anthropic.Chat(modelName)
case "gemini":
    model = google.Chat(modelName)
}
```

The `model` value now carries the model ID; it no longer needs to be passed
separately in every `llm.Request`. The `modelName` field on the TUI `model`
struct is still useful for display.

---

### Step 7 — `model` struct fields

Change:

```go
llmClient  llm.LLM
tools      []llm.Tool
history    []llm.Message
```

To:

```go
llmModel  provider.LanguageModel
tools     []goai.Tool
history   []provider.Message
```

The `mem *internalLlm.ConversationBuffer` field is deleted (see Step 3).

---

### Step 8 — Message construction

Replace all `llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: t}}}` with:

```go
goai.UserMessage(t)     // for user messages
goai.AssistantMessage(t) // for assistant messages
```

For the history initialization with a system-level user message (the existing
code puts the system prompt in `Request.System`, not in history — this stays
the same in goai via `goai.WithSystem(systemPrompt)`).

---

### Step 9 — Collapse tool loop into `GenerateText`

**Current pattern** (simplified):
```go
for {
    resp, err = llm.Complete(ctx, m.llmClient, llm.Request{...Tools: m.tools...})
    // extract tool calls from resp.Content
    // call each tool via MCP session
    // append tool results to history
    // break when no tool calls
}
```

**goai pattern**:
```go
result, err := goai.GenerateText(ctx, m.llmModel,
    goai.WithMessages(m.history...),
    goai.WithSystem(systemPrompt),
    goai.WithTools(m.tools...),
    goai.WithMaxSteps(10),
    goai.WithTemperature(0),
    goai.WithMaxOutputTokens(4096),
)
// result.Text is the final answer
// result.Steps[n].Messages contains the full turn history
```

This applies to:
- `generateResponse()` in the TUI (replace goroutine body, `executeTools` is deleted)
- `runSinglePrompt()` (replace entire `for` loop)
- `processConversation()` in webui (replace the loop)

After the call, append the new turns to `m.history` using `result.Steps`.
Each `StepResult` has a `Messages` field (`[]provider.Message`) with the
assistant turn and any tool messages from that step.

---

### Step 10 — Extract text and tool calls from result

Functions like `messageText`, `messageToolCalls`, `toolCallName`,
`toolCallArguments`, `extractToolContent`, `summarizeToolCalls` are used to
inspect pi-llm-go message blocks. After the port:

- `messageText(msg)` → `result.Text` (direct field on `TextResult`)
- `messageToolCalls(msg)` → no longer needed (goai runs the loop)
- `summarizeToolCalls` → can be adapted to use step events if streaming, or
  removed since the tool loop is now opaque
- `extractToolContent` → moved inside the Execute closure (Step 5)

For the TUI status indicator ("Running `search_security_alerts`..."), switch
to `StreamText` with a channel consumer that inspects `ChunkToolCall` chunks
to update the status message in real time.

---

### Step 11 — TUI tea.Msg types

The existing Bubbletea message flow is:

```
generateMsg → llmResponseMsg → executeToolsMsg → toolsResultMsg
```

After the port, since the tool loop is synchronous inside `GenerateText`, the
flow simplifies to:

```
generateMsg → llmResponseMsg (carries result.Text + steps)
```

Delete `executeToolsMsg`, `toolsResultMsg`, `toolOutcome`. The `cacheHits`,
`cacheMisses`, `cacheStores` counters came from the tool result text prefix
(`✓`, `↓`); these can be preserved by inspecting step results or dropped.

---

### Step 12 — History pruning and the `/memory` command

`pruneHistory` trims `m.history` to `maxHistoryMessages`. The type changes to
`[]provider.Message` but the logic is identical.

The `/memory` command rendered the conversation buffer's text summary. Replace
with a simple loop over `m.history`:

```go
var sb strings.Builder
for _, msg := range m.history {
    for _, p := range msg.Content {
        if p.Type == provider.PartText {
            sb.WriteString(string(msg.Role) + ": " + p.Text + "\n")
        }
    }
}
```

---

### Step 13 — `internal/webui/server.go`

Same substitutions as CLI:
- `llmClient llm.LLM` → `llmModel provider.LanguageModel`
- `tools []llm.Tool` → `tools []goai.Tool`
- `history []llm.Message` → `history []provider.Message`
- `processConversation()` loop → single `goai.GenerateText()` call
- `RunServer` signature changes to accept `provider.LanguageModel` and `[]goai.Tool`

---

### Step 14 — `internal/webui/server_test.go`

The test constructs a `Server{}` struct directly. Update field types to match
the new struct definition. The test only exercises WebSocket origin checking
and doesn't call LLM methods, so no mock changes are needed.

---

### Step 15 — `go mod tidy` and build

```
cd /home/mdfranz/github/elastic-security-mcp
go mod tidy
go build ./...
go test ./...
```

---

## What Is Deleted

| Symbol | Reason |
|--------|--------|
| `executeTools()` | goai runs the tool loop internally |
| `executeToolsMsg` / `toolsResultMsg` / `toolOutcome` | no longer needed |
| `toolCallName()` / `toolCallArguments()` / `formatToolCallArguments()` | handled inside Execute closures |
| `messageToolCalls()` | goai returns text, not raw blocks |
| `extractToolContent()` | moved into Execute closure |
| `summarizeToolCalls()` | no separate execute phase; adapt for streaming chunks if desired |
| `convertSchema()` / `cleanSchema()` | goai accepts `json.RawMessage` directly |
| `internalLlm.ConversationBuffer` | history is a plain `[]provider.Message` slice |
| `modelProvider()` string switch | replaced by goai provider packages resolving env vars |

---

## What Is Preserved

- All Elasticsearch/Kibana internals (`internal/elasticsearch/`, `internal/kibana/`)
- MCP server code (`cmd/server/`, MCP tool definitions)
- Bubbletea TUI structure, styles, key bindings, history, export
- Retry logic in `internal/util/retry.go`
- `runWebUI` / WebSocket server structure

---

## Risk Notes

1. **goai's MCP client vs. official SDK**: The official `go-sdk/mcp` server in
   `cmd/server/main.go` uses a different API. If switching to goai's MCP client
   on the CLI side, the two MCP implementations coexist in the same binary only
   if both are imported. Consider Option B (Step 5) to avoid the dual-dependency
   if the server binary also links `cmd/cli`.

2. **Tool result caching markers (`✓ `, `↓ `)**: `normalizeToolResultText`
   strips these prefixes from MCP tool results. If the Elasticsearch cache
   layer in `internal/elasticsearch/cache.go` still emits them, the Execute
   closure in Step 5 must preserve this stripping before returning.

3. **`WithMaxSteps` and `pruneHistory`**: The current TUI prunes history to
   `maxHistoryMessages=15`. With goai's built-in loop, history appended by
   `GenerateText` via steps must still be pruned after each turn to bound
   context length.
