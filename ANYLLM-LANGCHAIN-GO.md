# Migration Analysis: `any-llm-go` vs. `langchaingo`

This document details the architectural, implementation, and dependency differences between using **`github.com/tmc/langchaingo`** and **`github.com/mozilla-ai/any-llm-go`** (`any-llm-go`) for orchestrating LLM requests, conversation history, memory management, and tool calling within `elastic-security-mcp`. 

The codebase was migrated to `any-llm-go` in commit [a59d51e](file:///home/mdfranz/github/elastic-security-mcp) to simplify dependency charts, natively support Gemini features (including thought signatures), and streamline custom integrations.

---

## 🗺️ Architectural Abstraction Comparison

| Aspect | `langchaingo` (Previous) | `any-llm-go` (Current) |
| :--- | :--- | :--- |
| **Model Interface** | `llms.Model` | `anyllm.Provider` |
| **Chat Response** | `llms.ContentResponse` | `anyllm.ChatCompletion` |
| **History & Message Struct** | `llms.MessageContent` | `anyllm.Message` |
| **Message Roles** | `llms.ChatMessageTypeSystem`, `llms.ChatMessageTypeHuman`, `llms.ChatMessageTypeAI`, `llms.ChatMessageTypeTool` | `anyllm.RoleSystem`, `anyllm.RoleUser`, `anyllm.RoleAssistant`, `anyllm.RoleTool` |
| **Tool Parameters** | `llms.Tool` and `llms.ToolCall` | `anyllm.Tool` and `anyllm.ToolCall` |

### Code Comparison: LLM Generation

In **`langchaingo`**, the model instantiation was wrapped in provider-specific initialization packages, and the generation used `GenerateContent`:
```go
// langchaingo
resp, err := llmClient.GenerateContent(ctx, history, llms.WithTools(lcTools))
choice := resp.Choices[0]
content := choice.Content
```

In **`any-llm-go`**, the generation interface is unified under `Completion` using a standard completion configuration struct:
```go
// any-llm-go
resp, err := llmClient.Completion(ctx, anyllm.CompletionParams{
    Model:    modelName,
    Messages: history,
    Tools:    anyTools,
})
choice := resp.Choices[0]
content := choice.Message.ContentString()
```

---

## 🤖 Gemini Integration & Thought Signatures

One of the largest pain points in the previous `langchaingo` setup was support for Google's Gemini models—specifically, extracting and maintaining **Gemini thought signatures** (for reasoning traces in models like Gemini 3.x/Flash) and native tool calling formats.

*   **Before (`langchaingo`)**: The team had to implement a custom model provider wrapper (`geminiModel`) from scratch in `internal/llm/gemini_model.go` (totaling **567 lines of code**). This manually handled HTTP requests to the Google Gemini API, processed raw JSON responses, and parsed out `thoughtSignature` elements from the candidate parts.
*   **After (`any-llm-go`)**: `any-llm-go` supports Gemini natively. It wraps Google's official `google.golang.org/genai` library and handles model responses cleanly, allowing the deletion of the entire custom `gemini_model.go` file.

---

## 🧠 Memory Management Refactoring

The CLI requires conversational memory to retain previous user prompts, tool results, and assistant responses.

*   **Before (`langchaingo`)**: The project imported `github.com/tmc/langchaingo/memory` and used its built-in `memory.ConversationBuffer` to store history.
*   **After (`any-llm-go`)**: To avoid importing the massive `langchaingo` framework just for its memory module, a lightweight replacement was implemented in [internal/llm/memory.go](file:///home/mdfranz/github/elastic-security-mcp/internal/llm/memory.go). This custom `ConversationBuffer` retains the exact same signature methods to prevent breaking consumers in [cmd/cli/main.go](file:///home/mdfranz/github/elastic-security-mcp/cmd/cli/main.go) and [internal/webui/server.go](file:///home/mdfranz/github/elastic-security-mcp/internal/webui/server.go):
    ```go
    type ConversationBuffer struct {
        mu       sync.Mutex
        messages []anyllm.Message
    }

    func (cb *ConversationBuffer) SaveContext(ctx context.Context, input map[string]any, output map[string]any) error
    func (cb *ConversationBuffer) LoadMemoryVariables(ctx context.Context, _ map[string]any) (map[string]any, error)
    ```

---

## 📊 Logging & Observability

Observability patterns differ significantly between the two approaches.

### Procedural `slog` Logging (`elastic-security-mcp`)
This project leverages procedural logging utilizing Go’s native `log/slog` library.
*   **Trace points**: Logs are emitted at specific code locations (e.g., prior to LLM submission and right after receiving tool responses).
*   **Configuration**: Simple log handlers are bound to file writers depending on the environment variables (`CLIENT_LOG_FILE`, `SERVER_LOG_FILE`).
*   **Pros**: Explicit control, zero framework overhead, and trivial code paths.

### Lifecycle Callback Handlers (`langchaingo`)
In contrast, `langchaingo` uses a lifecycle callback mechanism (`callbacks` package).
*   **Trace points**: Emits events at structural lifecycle boundaries (`HandleLLMStart`, `HandleToolStart`, `HandleChainEnd`).
*   **Configuration**: Callbacks can be aggregated via `CombiningHandler` and directed to external observability backends (e.g., OpenTelemetry, Datadog).
*   **Pros**: Clean separation of logging/tracing logic from business code, standardizing APM instrumentation.

---

## 📦 Dependency and Footprint Impact

Moving from `langchaingo` to `any-llm-go` resulted in a significant simplification of the code footprint and dependency graph:

### Lines of Code (LoC) Changes
*   **Removed**: `internal/llm/gemini_model.go` (**-567 lines**)
*   **Added**: `internal/llm/memory.go` (**+68 lines**)
*   **Refactored**: `cmd/cli/main.go` and `internal/webui/server.go` net reduction in lines due to cleaner abstractions.

### Dependency Graph Simplification
`langchaingo` carries a very deep dependency chain, pulling in broad clouds SDKs, vector databases, and HTML parsers. By switching to `any-llm-go` (which targets standard SDK wrappers), `go.mod` was cleaned up considerably, leading to faster build times, smaller binary footprints, and a more maintainable security posture.
