# 3rd Party Dependencies

This document lists and classifies the 3rd party dependencies used in the `elastic-security-mcp` project.

## Core Infrastructure

### Elasticsearch
*   **[github.com/elastic/go-elasticsearch/v9](https://github.com/elastic/go-elasticsearch)**: Official Go client for Elasticsearch. In this project it is the main datastore integration: `internal/elasticsearch/client.go` creates both raw and typed clients, `internal/elasticsearch/tools.go` uses the raw client for `_cat/indices` and free-form JSON DSL searches, and `internal/elasticsearch/security_search.go` uses the typed API to build safer ECS-oriented security searches with filters, highlighting, and sorting.
*   **[github.com/elastic/elastic-transport-go/v8](https://github.com/elastic/elastic-transport-go)**: HTTP transport for Elastic clients. This repo does not import it directly, but it is pulled in underneath `go-elasticsearch` and handles the actual request/response transport used by the raw and typed Elasticsearch clients.

### Model Context Protocol (MCP)
*   **[github.com/modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)**: Go SDK for implementing MCP servers and clients. It is the core application protocol layer here: `cmd/server/main.go` creates the MCP server over stdio, `internal/elasticsearch/tools.go` and `internal/elasticsearch/security_search.go` register tools, `cmd/cli/main.go` launches the server as a child process and calls tools through an MCP client session, `internal/webui/server.go` relays browser requests into MCP tool calls, and `cmd/test-mcp/main.go` is a minimal MCP smoke-test client.

### Caching
*   **[github.com/redis/go-redis/v9](https://github.com/redis/go-redis)**: Type-safe Redis client for Go. `internal/elasticsearch/cache.go` uses it to cache MCP tool results by hashed arguments and TTL, while `internal/elasticsearch/indexer.go` uses Redis sorted sets to build short-lived entity lookups from Zeek DNS hits such as domain-to-IP and IP-to-domain history.

## LLM & AI
*   **[github.com/tmc/langchaingo](https://github.com/tmc/langchaingo)**: Go implementation of LangChain for LLM orchestration. This repo uses its `llms` abstractions and tool-call types as the common interface across OpenAI, Anthropic, and the custom Gemini adapter, and uses `memory.ConversationBuffer` in both the TUI and Web UI to preserve conversational context between turns.
*   **[google.golang.org/api](https://google.golang.org/api)**: Google Cloud APIs for Go. In practice this repo only imports `googleapi.Error` to normalize Gemini API failures in `internal/llm/gemini_model.go` and in the CLI model selection path; the Gemini request/response flow itself is implemented manually with `net/http`.

## Command Line Interface (CLI)

### CLI Framework
*   **[github.com/spf13/cobra](https://github.com/spf13/cobra)**: A library for creating powerful modern CLI applications. `cmd/cli/main.go` uses Cobra for the main entrypoint, argument parsing, and flags such as `--model`, `--memory`, `--prompt`, `--webui`, and `--port`.
*   **[github.com/spf13/pflag](https://github.com/spf13/pflag)**: Drop-in replacement for Go's flag package, implementing POSIX/GNU-style `--flags`. It is not imported directly, but Cobra uses it under the hood for the CLI flag definitions declared in `cmd/cli/main.go`.

### Terminal UI (The Charm Stack)
*   **[github.com/charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)**: A powerful little TUI framework based on the Elm architecture. The interactive terminal assistant in `cmd/cli/main.go` is built as a Bubble Tea model with event-driven update and view loops.
*   **[github.com/charmbracelet/bubbles](https://github.com/charmbracelet/bubbles)**: Common TUI components for Bubble Tea. This repo uses `textinput` for prompt entry, `viewport` for scrolling conversation output, `spinner` for in-flight status, and `list` for interactive provider/model selection.
*   **[github.com/charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)**: Style definitions for nice terminal layouts. `cmd/cli/main.go` uses it to define message styles, widths, colors, and layout behavior for user, assistant, status, and system text in the terminal UI.
*   **[github.com/charmbracelet/glamour](https://github.com/charmbracelet/glamour)**: Markdown rendering for the terminal. Assistant responses are passed through a `glamour.TermRenderer` in `cmd/cli/main.go` so Markdown output, especially tables, is readable in the TUI.

## Web & Networking
*   **[github.com/gorilla/websocket](https://github.com/gorilla/websocket)**: A fast, tested, and widely used WebSocket implementation for Go. `internal/webui/server.go` upgrades browser connections, streams status and tool activity to the frontend, and receives user prompts over `/ws`.

## Frontend
The frontend is a lightweight Single Page Application (SPA) built with modern web standards.

*   **Vanilla JavaScript (ES6+)**: `internal/webui/assets/app.js` manages the browser-side state machine: WebSocket connection lifecycle, tool activity rendering, cache counters, session reset/export actions, and markdown/table post-processing.
*   **Vanilla CSS**: `internal/webui/assets/style.css` provides the full visual system for the browser UI, including the split-pane layout, status chips, tool trace cards, and responsive behavior.
*   **[Marked.js](https://marked.js.org/)**: A low-level markdown compiler loaded via CDN in `internal/webui/assets/index.html`. `internal/webui/assets/app.js` uses it to render assistant Markdown responses into HTML before inserting them into the conversation pane.

## Utility & Libraries

### Observability & Logging
*   **[go.opentelemetry.io/otel](https://go.opentelemetry.io/otel)**: OpenTelemetry Go API and SDK. This appears only as an indirect dependency; the repo does not currently initialize tracing or metrics directly.
*   **[github.com/go-logr/logr](https://github.com/go-logr/logr)**: A simple logging interface for Go. This is also indirect; the project's own logging uses the standard library `log/slog`, while `logr` is brought in by dependencies.

### Text Processing & Parsing
*   **[github.com/yuin/goldmark](https://github.com/yuin/goldmark)**: A markdown parser written in Go. The repo does not import it directly; it comes in transitively through the terminal markdown stack used by Glamour.
*   **[github.com/google/jsonschema-go](https://github.com/google/jsonschema-go)**: JSON Schema support for Go. This is not referenced directly in project code, but it underpins JSON schema generation in the MCP SDK for the tool argument structs that use `jsonschema` tags in `internal/elasticsearch/tools.go` and `internal/elasticsearch/security_search.go`.

### General Utilities
*   **[github.com/google/uuid](https://github.com/google/uuid)**: Go package for generating and inspecting UUIDs. It is currently only an indirect dependency; this repo generates tool call IDs with `crypto/rand` and `encoding/hex` instead of importing `uuid` directly.
*   **[golang.org/x/net](https://golang.org/x/net)**: Supplementary network libraries. Indirect dependency only, brought in by the Go HTTP / markdown / API client stack rather than imported by this repo directly.
*   **[golang.org/x/sys](https://golang.org/x/sys)**: Low-level operating system primitives. Indirect dependency only; the repo itself uses the standard library `syscall` package directly in `cmd/server/main.go` for file locking and signal handling.
