# LLM Provider Migration & Integration History

This document provides a comprehensive analysis of the evolution of the LLM orchestration layer within `elastic-security-mcp`. Over the course of the project's lifecycle, the LLM client integration underwent four distinct phases to optimize developer velocity, reduce binary size, natively support advanced model features (like Gemini's thought signatures), and streamline tool-calling execution.

---

## 🗺️ The Orchestration Evolution

The project transitioned through four LLM client implementations, moving from a heavy feature-rich framework to a highly automated, minimal provider library:

```mermaid
graph TD
    subgraph Phase 1: Heavy Framework [langchaingo]
        L_Model[llms.Model]
        L_Loop[Manual Tool Loop in TUI/WebUI]
        L_Gemini[Custom gemini_model.go\n567 LoC for Thought Extraction]
        L_Mem[langchaingo/memory\nConversationBuffer]
    end

    subgraph Phase 2: Lightweight Wrapper [any-llm-go]
        A_Model[anyllm.Provider]
        A_Loop[Manual Tool Loop]
        A_Gemini[Native genai Wrapper\nDeleted gemini_model.go]
        A_Mem[Custom internal/llm/memory.go\n68 LoC]
    end

    subgraph Phase 3: Minimal & Block-Based [pi-llm-go]
        P_Model[llm.LLM]
        P_Loop[Manual Tool Loop]
        P_Block[Message Content as []llm.Block]
        P_Mem[Custom Memory Adapter]
    end

    subgraph Phase 4: Automatic & Streamlined [goai]
        G_Model[provider.LanguageModel]
        G_Loop[Automatic Tool Loop\nGenerateText + MaxSteps]
        G_MCP[goaimcp.ConvertTools\nAuto-wires Execute Closure]
        G_Mem[Deleted Memory Package\nDirect []provider.Message Slice]
    end

    L_Model --> A_Model
    A_Model --> P_Model
    P_Model --> G_Model
```

---

## 📊 Comprehensive Provider Feature Comparison

| Aspect | Phase 1: `langchaingo` | Phase 2: `any-llm-go` | Phase 3: `pi-llm-go` | Phase 4: `goai` (Current) |
| :--- | :--- | :--- | :--- | :--- |
| **Philosophy** | All-in-one generic AI framework | Lightweight client wrappers | Minimalist provider-agnostic model calls | Automated, developer-first AI wrapper |
| **Model Interface** | `llms.Model` | `anyllm.Provider` | `llm.LLM` | `provider.LanguageModel` |
| **Message Structure** | `llms.MessageContent` | `anyllm.Message` (Flat String) | `llm.Message` (Slice of `llm.Block` interfaces) | `provider.Message` (Slice of `provider.Part` structs) |
| **System Prompt** | Kept in message slice with role `System` | Kept in message slice with role `System` | Managed on `llm.Request.System` parameter | Passed as a functional option (`goai.WithSystem`) |
| **Tool Parameters** | `llms.Tool` / `llms.ToolCall` | `anyllm.Tool` / `anyllm.ToolCall` | `llm.Tool` (Flat) / `llm.ToolCallBlock` | `goai.Tool` (Flat + `Execute` callback) / `provider.ToolCall` |
| **Tool Execution** | Handled manually in frontend | Handled manually in frontend | Handled manually in frontend | **Automated internally** (`WithMaxSteps`) |
| **Thought Signatures** | Manual extraction via 567 LoC custom provider | Native GenAI SDK support | Supports `llm.ThinkingBlock` | Native support via `provider.Part` structures |
| **Memory Buffer** | Heavy framework-specific memory import | Custom `internal/llm/memory.go` (68 LoC) | Custom block-based `internal/llm/memory.go` | **No custom memory package** (Direct slice operations) |
| **Dependency Weight** | Heavy (includes vector DBs, HTML parsers, SDKs) | Medium (direct GenAI SDK bindings) | Minimal (clean, raw HTTP bindings) | Light/Medium (direct SDK adapters, no bloat) |

---

## 🔍 Core Learnings and Implementation Details

### 1. The Burden of Heavy Frameworks (`langchaingo`)
Initially, the project adopted `langchaingo` for its pre-packaged abstractions. However, this brought two primary issues:
* **Dependency Bloat**: Importing the framework pulled in transitives for vector databases, cloud SDKs, HTML parsers, and various packages that `elastic-security-mcp` never needed. This ballooned compilation times and security surface area.
* **Lack of Specialized API Support**: Models like Gemini require custom handling for features such as thought signatures (for reasoning models) and distinct tool calling formats. Because `langchaingo` generalized everything to the lowest common denominator, the team had to implement a **567-line custom HTTP client wrapper** (`internal/llm/gemini_model.go`) to manually parse HTTP responses and extract thought signatures.

### 2. Streamlining with Raw Wrappers (`any-llm-go`)
To resolve dependency bloat and support Gemini features natively, the codebase migrated to `any-llm-go`.
* **Gemini Native Support**: It wrapped Google's official `google.golang.org/genai` library, which allowed the team to completely delete the manual `gemini_model.go` file.
* **Custom Memory Isolation**: The framework-specific `memory.ConversationBuffer` was replaced by a custom 68-line `internal/llm/memory.go` script that implemented the same simple interface without external library dependencies.

### 3. Transitioning to Block-Based Content (`pi-llm-go`)
Although `any-llm-go` simplified the dependencies, it represented message content as flat strings. The next evolution, `pi-llm-go`, shifted to a provider-agnostic, block-based message format:
* **Interface Simplification**: The chat completion response choices wrapper was eliminated. Calling `Complete` returned the assistant's `*llm.Message` directly.
* **Content Blocks**: Content was structured as a slice of `llm.Block` interfaces (e.g. `TextBlock`, `ToolCallBlock`, `ToolResultBlock`).
* **System Prompt Isolation**: The system prompt was pulled out of the message history slice and passed strictly via `Request.System`.

### 4. Zero-Overhead Orchestration (`goai`)
The final migration to `goai` achieved the highest degree of code simplification and safety by resolving structural layout tasks directly in the LLM execution layer:
* **Internal Tool Loop**: Instead of requiring the frontends (TUI, CLI, and Web UI) to implement manual loop checks to catch tool calls, execute them, and feedback results, `goai` supports `GenerateText(ctx, model, WithMaxSteps(n))` which automates the loop.
* **MCP Tool Mapping**: Pre-wired MCP client integration (`goaimcp.ConvertTools`) maps MCP tool listings directly into executable `goai.Tool` types. This deleted all manual dispatcher logic (`toolCallName`, `toolCallArguments`, `extractToolContent`).
* **Memory Elimination**: Because `goai` accepts standard `[]provider.Message` slices, the helper memory package `internal/llm/memory.go` was deleted entirely. Frontends now hold conversation state directly as simple message slices.

---

## 🛠️ Code Pattern Comparisons

### Generation & Tool Loop Integration

#### The Manual Way (`langchaingo` / `any-llm-go` / `pi-llm-go`)
In previous setups, the frontends had to maintain a manual state loop to run tool calls, parse inputs, issue MCP calls, and feed outputs back to the LLM:

```go
// Manual Tool Loop inside TUI/Web UI
for {
    resp, err := llm.Complete(ctx, client, request)
    toolCalls := messageToolCalls(resp)
    if len(toolCalls) == 0 {
        break // Final response
    }
    for _, tc := range toolCalls {
        res, err := mcpSession.CallTool(ctx, tc.Name, tc.Arguments)
        // Format and append tool result blocks to history
    }
}
```

#### The Automated Way (`goai`)
With `goai`, this entire loop collapse into a single configuration call:

```go
result, err := goai.GenerateText(ctx, engine.Model,
    goai.WithMessages(history...),
    goai.WithSystem(SystemPrompt),
    goai.WithTools(engine.Tools...),
    goai.WithMaxSteps(10),
)
// result.Text is the final answer
// result.Steps[n].Messages contains the full turn history
```

---

## 📉 Code Footprint Impact Summary

| File / Symbol | Change Type | Reason |
| :--- | :--- | :--- |
| `internal/llm/gemini_model.go` | **Deleted (-567 LoC)** | Replaced by native Gemini SDK support. |
| `internal/llm/memory.go` | **Deleted (-68 LoC)** | State represented directly as `[]provider.Message`. |
| `executeTools()` | **Deleted** | Loop executed internally by the `goai` library. |
| `toolCallName()`, `toolCallArguments()`, `extractToolContent()` | **Deleted** | Handled transparently by the automated MCP client. |
| `convertSchema()`, `cleanSchema()` | **Deleted** | `goai` accepts raw `json.RawMessage` schemas directly from MCP. |
| `internal/agent` | **Added** | Shared engine to run the `goai.GenerateText` call and unify TUI and Web UI loops. |

---

## 💡 Lessons Learned for Go LLM Integration

1. **Avoid Early Generalization**: Standard framework packages like `langchaingo` make it easy to start, but their rigid structures limit your ability to exploit specialized features (like Gemini's thought signatures or Anthropic's prompt caching) without writing complex bypass code.
2. **Prefer Runnable Tool Definitions**: Binding execution logic directly to the tool definition (as `goai` does via `Execute func(context.Context, json.RawMessage) (string, error)`) yields significantly cleaner code than dividing tool registration from tool execution routing.
3. **Decouple Orchestration from UI Presentation**: Moving the tool-execution engine into a dedicated `internal/agent` package shared by both the Bubble Tea TUI and the WebSocket Web UI ensured feature parity, simplified testing, and prevented divergent bug patterns.
