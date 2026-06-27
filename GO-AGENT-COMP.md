# Go Agent Implementation Comparison: `elastic-security-mcp`

This document provides a comparative analysis of the LLM orchestration, conversation memory, and agent orchestration implementations across the different development branches of `elastic-security-mcp`.

The codebase has evolved through four major architectural paradigms, each using a different Go LLM library or approach:

1. **`main` (Legacy)**: Based on `github.com/tmc/langchaingo`.
2. **`any-llm-go`**: Based on `github.com/mozilla-ai/any-llm-go` (migrated in commit `a59d51e`).
3. **`pi-llm-port`**: Based on `github.com/amit-timalsina/pi-llm-go` (minimalist, block-based API).
4. **`zendev-goai` (Active)**: Based on `github.com/zendev-sh/goai` (modern, part-structured API).

---

## 🗺️ Architectural Topology Comparison

The diagram below shows how the CLI application communicates with the LLM API and handles state across the different branches.

#### `main` branch (`langchaingo`)
```mermaid
graph TD
    A1[cli/main.go] -->|llms.Model| B1[GenerateContent]
    A1 -->|memory.ConversationBuffer| C1[langchaingo/memory]
    A1 -->|567 LoC Custom Provider| D1[internal/llm/gemini_model.go]
    D1 -->|Google API| E1[Gemini Thought Signatures]
```

#### `any-llm-go` branch
```mermaid
graph TD
    A2[cli/main.go] -->|anyllm.Provider| B2[Completion]
    A2 -->|68 LoC Custom Adapter| C2[internal/llm/memory.go]
    B2 -->|any-llm-go| E2[Native Gemini/Anthropic/OpenAI]
```

#### `pi-llm-port` branch
```mermaid
graph TD
    A3[cli/main.go] -->|llm.LLM| B3[Complete]
    A3 -->|Content Block Adapter| C3[internal/llm/memory.go]
    B3 -->|pi-llm-go| E3[Block-Structured Request/Response]
```

#### `zendev-goai` branch (Current)
```mermaid
graph TD
    A4[cli/main.go] -->|provider.LanguageModel| B4[goai.GenerateText]
    A4 -->|In-memory message slice pruning| C4[No internal/llm/memory.go needed]
    B4 -->|goai| E4[Structured Messages & Message Parts]
```

---

## 🔌 Provider API Analysis

The Provider API defines how the agent establishes sessions, formats prompt requests, and extracts text or tool calls from the model responses.

### 1. `langchaingo` (`main`)
*   **Abstractions**: `llms.Model` interface with `GenerateContent(ctx, messages, options...)`.
*   **Response Structure**: `llms.ContentResponse` containing a list of `Choices`, where each choice has a flat text `Content` field and a separate `ToolCalls` slice.
*   **Gemini Support**: Missing native support for Gemini thought signatures/reasoning processes. The team had to build and maintain a custom model provider wrapper (`geminiModel`) in `internal/llm/gemini_model.go` (567 lines) that directly parsed raw candidate parts via the HTTP API.

### 2. `any-llm-go` (`any-llm-go`)
*   **Abstractions**: `anyllm.Provider` interface with `Completion(ctx, CompletionParams)`.
*   **Response Structure**: `anyllm.ChatCompletion` containing `Choices`, with each choice returning an `anyllm.Message` having a flat `.ContentString()` and a `.ToolCalls` slice.
*   **Gemini Support**: Native wrapper around `google.golang.org/genai`. Supported thought signatures, allowing the deletion of the custom 567 LoC provider file.

### 3. `pi-llm-go` (`pi-llm-port`)
*   **Abstractions**: `llm.LLM` interface with a package-level entry point `llm.Complete(ctx, provider, request)`.
*   **Response Structure**: Highly flat. It completely removes the `Choices` array, returning a single `*llm.Message` containing a slice of content blocks (`[]llm.Block`).
*   **Block-Based Output**: Tool calls and text outputs are not separate fields; they are part of the sequential `Content` slice as `llm.ToolCallBlock` or `llm.TextBlock`.

### 4. `goai` (`zendev-goai`)
*   **Abstractions**: `provider.LanguageModel` interface, orchestrated via a high-level helper `goai.GenerateText(ctx, model, options...)`.
*   **Response Structure**: Returns a `*goai.TextResult`, which wraps the final response text, raw tool calls, and the generated response messages.
*   **Integration Model**: Handlers are cleanly packaged under `provider/openai`, `provider/anthropic`, and `provider/google` (representing Gemini).

---

## 🧠 Memory API Analysis

Conversational history must be preserved, formatted, and pruned during interactive CLI or Web UI sessions to stay within context windows.

### 1. `langchaingo` (`main`)
*   **Approach**: Uses langchaingo's native `memory.ConversationBuffer`.
*   **Integration**: Leverages standard memory interfaces. While convenient, it forces importing the massive `langchaingo` codebase just to retain a buffer of conversation history.
*   **State Format**: Managed via `llms.MessageContent` slices.

### 2. `any-llm-go` (`any-llm-go`)
*   **Approach**: A lightweight, custom `ConversationBuffer` implemented in `internal/llm/memory.go` (68 LoC).
*   **Integration**: Mimics the exact methods of the langchaingo memory buffer (`SaveContext` and `LoadMemoryVariables`) to prevent breaking consumers in the CLI TUI (`cmd/cli/main.go`) and the Web UI server (`internal/webui/server.go`).
*   **State Format**: Managed via slices of `anyllm.Message` containing simple strings.

### 3. `pi-llm-go` (`pi-llm-port`)
*   **Approach**: Custom `ConversationBuffer` in `internal/llm/memory.go` rewritten to utilize `llm.Message` and content block structures.
*   **Integration**: Uses the block-centric API. Text inputs are stored as `llm.TextBlock`s. When loading history, it loops through message content blocks to reconstruct standard role-based transcript segments.
*   **State Format**: Slices of `llm.Message` containing `[]llm.Block`.

### 4. `goai` (`zendev-goai`)
*   **Approach**: Direct in-memory slice manipulation. The codebase **deletes** `internal/llm/memory.go` entirely.
*   **Integration**: Instead of wrapping memory in an abstract buffer object, history is maintained directly in the CLI's Bubble Tea model (`history []provider.Message`) and the Web UI's WebSocket loop (`history []provider.Message`).
*   **State Format**: Slices of `provider.Message` containing `provider.Part` elements (such as `PartText`, `PartToolCall`, and `PartToolResult`).
*   **Pruning**: Implemented procedurally via a simple helper function `pruneHistory()` in the CLI and UI, keeping the last 15 messages when memory is disabled or when sliding windows are applied.

---

## 🤖 Agent API & Loop Orchestration

The agent loop represents the core execution engine: sending the prompt, intercepting tool calls, invoking local MCP commands, posting results back, and preventing loop execution failures (like stalling/narration).

| Branch | Tool Call Representation | Tool Result Handling | Stalling Detection |
| :--- | :--- | :--- | :--- |
| **`main`** | `llms.ToolCall` (nested structure) | `llms.ToolCallResponse` content parts | Procedural check for narrative phrases ("I will search", etc.), appending a direct prompt to force action. |
| **`any-llm-go`** | `anyllm.ToolCall` (function parameters) | `anyllm.Message` with `RoleTool` and `ToolCallID` | Same procedural check, appending instruction to `history` slice. |
| **`pi-llm-port`** | `llm.ToolCallBlock` inside `Message.Content` | `llm.Message` containing `llm.ToolResultBlock` | Same check, wrapping text in `llm.TextBlock`. |
| **`zendev-goai`** | `provider.ToolCall` (integrated slice) | `provider.Message` containing `provider.PartToolResult` parts | Same check, appending user correction messages directly to history. |

In all four implementations, the agent loop itself is **inlined directly within the application layer** (`cmd/cli/main.go` and `internal/webui/server.go`). There is no independent "Agent struct" or separate framework-driven agent runner; orchestration is handled by:
1. Bubble Tea messages (`llmResponseMsg` and `toolsResultMsg` triggers) in the TUI CLI.
2. An infinite loop over WebSocket requests (`processConversation`) in the Web UI.

---

## 💻 Concrete Code Examples

### 1. Provider & Tool Setup Comparison

This snippet demonstrates how tools discovered from the MCP server are converted into each library's native format.

#### `main` (`langchaingo`)
```go
lcTools := make([]llms.Tool, 0, len(toolsResult.Tools))
for _, t := range toolsResult.Tools {
    lcTools = append(lcTools, llms.Tool{
        Type: "function",
        Function: &llms.FunctionDefinition{
            Name:        t.Name,
            Description: t.Description,
            Parameters:  t.InputSchema,
        },
    })
}
```

#### `any-llm-go`
```go
anyTools := make([]anyllm.Tool, 0, len(toolsResult.Tools))
for _, t := range toolsResult.Tools {
    anyTools = append(anyTools, anyllm.Tool{
        Type: "function",
        Function: anyllm.Function{
            Name:        t.Name,
            Description: t.Description,
            Parameters:  convertSchema(t.InputSchema), // requires a schema fixer helper
        },
    })
}
```

#### `pi-llm-port`
```go
tools := make([]llm.Tool, 0, len(toolsResult.Tools))
for _, t := range toolsResult.Tools {
    schemaBytes, _ := json.Marshal(t.InputSchema)
    tools = append(tools, llm.Tool{
        Name:        t.Name,
        Description: t.Description,
        InputSchema: json.RawMessage(schemaBytes), // flattened parameters
    })
}
```

#### `zendev-goai` (Current)
```go
tools := make([]goai.Tool, 0, len(toolsResult.Tools))
for _, t := range toolsResult.Tools {
    tools = append(tools, goai.Tool{
        Name:        t.Name,
        Description: t.Description,
        InputSchema: t.InputSchema, // direct JSON schema mapping
    })
}
```

---

### 2. Completion Request & Execution

This section contrasts how prompt messages, system prompts, temperature controls, and tools are supplied during model invocation.

#### `main` (`langchaingo`)
```go
resp, err := llmClient.GenerateContent(ctx, history, llms.WithTools(lcTools))
choice := resp.Choices[0]
content := choice.Content
```

#### `any-llm-go`
```go
resp, err := llmClient.Completion(ctx, anyllm.CompletionParams{
    Model:    modelName,
    Messages: history,
    Tools:    anyTools,
})
choice := resp.Choices[0]
content := choice.Message.ContentString()
```

#### `pi-llm-port`
```go
resp, err := llm.Complete(ctx, llmClient, llm.Request{
    Model:       modelName,
    System:      systemPrompt,             // system prompt moved out of messages slice
    Messages:    m.history,
    Tools:       m.tools,
    Temperature: &tempZero,
    MaxTokens:   4096,                     // plain int value
})
// Complete returns the message object directly (no choices array wrapper)
```

#### `zendev-goai` (Current)
```go
result, err := goai.GenerateText(m.ctx, m.llmModel,
    goai.WithMessages(m.history...),
    goai.WithSystem(systemPrompt),         // configuration via functional options
    goai.WithTools(m.tools...),
    goai.WithTemperature(0),
    goai.WithMaxOutputTokens(4096),
)
content := result.Text
```

---

### 3. Memory & History Management

How conversation memory and sliding history window pruning are handled in the CLI.

#### `main` (`langchaingo`)
```go
// Loading history from ConversationBuffer
vars, err := m.mem.LoadMemoryVariables(m.ctx, nil)
hist, _ := vars["history"].(string)

// Appending user prompt to structured history
m.history = append(m.history, llms.MessageContent{
    Role:  llms.ChatMessageTypeHuman,
    Parts: []llms.ContentPart{llms.TextContent{Text: input}},
})
```

#### `any-llm-go` (Custom Memory Adapter)
```go
type ConversationBuffer struct {
    mu       sync.Mutex
    messages []anyllm.Message
}

func (cb *ConversationBuffer) SaveContext(ctx context.Context, input map[string]any, output map[string]any) error {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    inStr, _ := input["input"].(string)
    outStr, _ := output["output"].(string)
    if inStr != "" {
        cb.messages = append(cb.messages, anyllm.Message{Role: anyllm.RoleUser, Content: inStr})
    }
    if outStr != "" {
        cb.messages = append(cb.messages, anyllm.Message{Role: anyllm.RoleAssistant, Content: outStr})
    }
    return nil
}
```

#### `pi-llm-port` (Block-based Memory)
```go
func (cb *ConversationBuffer) SaveContext(ctx context.Context, input map[string]any, output map[string]any) error {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    inStr, _ := input["input"].(string)
    outStr, _ := output["output"].(string)
    if inStr != "" {
        cb.messages = append(cb.messages, llm.Message{
            Role:    llm.RoleUser,
            Content: []llm.Block{llm.TextBlock{Text: inStr}}, // block wrapper
        })
    }
    // (similar for AI/Assistant response block)
    return nil
}
```

#### `zendev-goai` (Current - Direct Memory Pruning)
```go
// User prompt is appended directly as a goai message struct
m.history = append(m.history, goai.UserMessage(input))

if !m.useMemory {
    m.pruneHistory()
}

// Slice-based sliding window history pruning in cmd/cli/main.go
func (m *model) pruneHistory() {
    if len(m.history) <= maxHistoryMessages {
        return
    }
    m.history = m.history[len(m.history)-maxHistoryMessages:]
}
```

---

## 📊 Summary Comparison Matrix

| Feature / Metric | `main` (langchaingo) | `any-llm-go` | `pi-llm-port` | `zendev-goai` (Current) |
| :--- | :--- | :--- | :--- | :--- |
| **Orchestration Core** | Framework-based | Lightweight Client | Minimalist Client | Structured Part Client |
| **Module Footprint** | Extremely Heavy (vector DBs, cloud SDKs) | Moderate (Standard API) | Minimal (No extra transitive deps) | Light (Clean, modular) |
| **Custom Model Wrappers** | Yes (`gemini_model.go`, 567 lines) | No (native wrapper) | No (native wrapper) | No (native wrapper) |
| **Memory Adapter** | Langchaingo library | Custom Adapter (68 LoC) | Custom Adapter (72 LoC) | None (Direct TUI/Web UI slice handling) |
| **System Prompt Delivery**| Prepended to message history | Prepended to message history | Specified in `llm.Request.System` | Passed to `goai.WithSystem()` |
| **Tool Definition Format**| Typed nested structures | Typed nested structures | Flat struct with schema bytes | Flat `goai.Tool` with MCP schema |
| **Web UI Integration** | Websocket / Raw JSON | Websocket / Raw JSON | Websocket / Raw JSON | Websocket / Raw JSON |

---

## 📝 Integration Quality and Developer Velocity Assessment

### 1. Langchaingo (`main`)
*   **Pros**: Uses a standard framework with broad recognition.
*   **Cons**: Massive binary footprint and transitive dependencies. The lack of native Gemini features created a massive maintenance burden, requiring a custom 567 LoC implementation of the Gemini API client just to extract reasoning traces.

### 2. `any-llm-go`
*   **Pros**: Solved the Gemini thought signature parsing issue natively, clean API wrappers, allowed dropping a lot of boilerplates.
*   **Cons**: Custom memory wrapper kept the interface identical to langchaingo but carried over some API complexity (e.g. nested parameters object).

### 3. `pi-llm-port`
*   **Pros**: Extremely clean and modern block-based API model. Moving the system prompt out of the message array aligns perfectly with Anthropic and Gemini native API shapes.
*   **Cons**: Requires a full rewrite of message-handling helpers (like `summarizeHistoryForLog`) to accommodate `[]llm.Block` type-switching, slightly increasing CLI code complexity in exchange for backend simplicity.

### 4. `zendev-goai` (Active Branch)
*   **Pros**: Excellent balance of structure and simplicity. It allows removing memory files altogether in favor of native Go slices. Integrates directly with MCP schemas without manual mapping.
*   **Cons**: The lack of a shared helper/adapter layer for memory leads to duplicated history-rendering code in `cmd/cli/main.go` and `internal/webui/server.go`. Moving the system prompt to options (`goai.WithSystem`) and message mapping works elegantly.
