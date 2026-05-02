# 3rd Party Dependencies

This document lists and classifies the 3rd party dependencies used in the `elastic-security-mcp` project.

## Core Infrastructure

### Elasticsearch
*   **[github.com/elastic/go-elasticsearch/v9](https://github.com/elastic/go-elasticsearch)**: Official Go client for Elasticsearch.
*   **[github.com/elastic/elastic-transport-go/v8](https://github.com/elastic/elastic-transport-go)**: HTTP transport for Elastic clients.

### Model Context Protocol (MCP)
*   **[github.com/modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)**: Go SDK for implementing MCP servers and clients.

### Caching
*   **[github.com/redis/go-redis/v9](https://github.com/redis/go-redis)**: Type-safe Redis client for Go.

## LLM & AI
*   **[github.com/tmc/langchaingo](https://github.com/tmc/langchaingo)**: Go implementation of LangChain for LLM orchestration.
*   **[google.golang.org/api](https://google.golang.org/api)**: Google Cloud APIs for Go (used for Gemini integration).

## Command Line Interface (CLI)

### CLI Framework
*   **[github.com/spf13/cobra](https://github.com/spf13/cobra)**: A library for creating powerful modern CLI applications.
*   **[github.com/spf13/pflag](https://github.com/spf13/pflag)**: Drop-in replacement for Go's flag package, implementing POSIX/GNU-style --flags.

### Terminal UI (The Charm Stack)
*   **[github.com/charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)**: A powerful little TUI framework based on the Elm architecture.
*   **[github.com/charmbracelet/bubbles](https://github.com/charmbracelet/bubbles)**: Common TUI components for Bubble Tea.
*   **[github.com/charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)**: Style definitions for nice terminal layouts.
*   **[github.com/charmbracelet/glamour](https://github.com/charmbracelet/glamour)**: Markdown rendering for the terminal.

## Web & Networking
*   **[github.com/gorilla/websocket](https://github.com/gorilla/websocket)**: A fast, tested, and widely used WebSocket implementation for Go.

## Frontend
The frontend is a lightweight Single Page Application (SPA) built with modern web standards.

*   **Vanilla JavaScript (ES6+)**: Used for DOM manipulation, WebSocket communication, and application logic.
*   **Vanilla CSS**: Custom design system for the user interface.
*   **[Marked.js](https://marked.js.org/)**: A low-level markdown compiler for parsing markdown without caching or blocking for long periods of time. Loaded via CDN.

## Utility & Libraries

### Observability & Logging
*   **[go.opentelemetry.io/otel](https://go.opentelemetry.io/otel)**: OpenTelemetry Go API and SDK.
*   **[github.com/go-logr/logr](https://github.com/go-logr/logr)**: A simple logging interface for Go.

### Text Processing & Parsing
*   **[github.com/yuin/goldmark](https://github.com/yuin/goldmark)**: A markdown parser written in Go.
*   **[github.com/google/jsonschema-go](https://github.com/google/jsonschema-go)**: JSON Schema support for Go.

### General Utilities
*   **[github.com/google/uuid](https://github.com/google/uuid)**: Go package for generating and inspecting UUIDs.
*   **[golang.org/x/net](https://golang.org/x/net)**: Supplementary network libraries.
*   **[golang.org/x/sys](https://golang.org/x/sys)**: Low-level operating system primitives.
