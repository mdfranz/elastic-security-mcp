
# Elastic Security MCP

This project implements the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) to provide a bridge between Large Language Models and Elasticsearch security data.

It consists of two main components:
1. **Elastic MCP Server**: A standalone server that exposes Elasticsearch tools via the MCP protocol.
2. **Elastic CLI**: A feature-rich client (TUI and Web UI) that uses the MCP server to provide an AI-powered security analyst experience.

For a detailed look at how these components interact, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Components Overview

## Elastic Security Assistant (Web UI)

![Elastic Security Web UI](elastic-ndr-webui.png)


## Elastic Security Assistant (CLI)

The project includes a powerful, agentic CLI that acts as a security analyst assistant.

- **Interactive TUI**: Built with Bubble Tea and Lip Gloss for a modern terminal experience.
- **Multi-Provider Support**: Seamlessly switch between OpenAI, Anthropic, and Google Gemini models.
- **Interactive Model Selection**: Pick your preferred provider and model on startup if not pre-configured.
- **Conversation Memory**: Built-in context management for long-running investigations (type `/memory` to view).
- **One-Shot Execution**: Run quick queries and exit using the `--prompt` or `-p` flag.
- **Markdown Rendering**: High-quality rendering of tables and analysis results using Glamour.
- **Optional Web UI**: Use the `--webui` flag to start a local web server with a similar look and feel to the terminal experience.




## Server Tools

The MCP server provides the following tools to any compatible host:

- **list_indices**: Tool to see what indices are available in your Elasticsearch cluster, with optional pattern filtering.
- **search_security_events**: Structured, snippets-first search for ECS-style Zeek and Suricata data with typed filters (`text`, `start`, `end`, `ip`, `src_ip`, `dst_ip`, `domain`, `url`, `dataset`), boosted network fields, and highlighting.
- **lookup_domain**: Check local Redis cache for DNS activity history for a specific domain name. Returns recent DNS queries, source IPs, and resolved addresses from previously observed traffic.
- **lookup_ip**: Check local Redis cache for any observed activity involving an IP address. Returns DNS records where this IP appeared as an answer and DNS queries made by this IP as a source.
- **search_elastic**: Raw Elasticsearch Query DSL access for advanced or unsupported queries.

## Key Libraries

This project leverages several powerful libraries:

- [**Elasticsearch Go Client**](https://github.com/elastic/go-elasticsearch): The official Go client for Elasticsearch.
- [**Model Context Protocol (MCP) SDK**](https://github.com/modelcontextprotocol/go-sdk): SDK for building MCP servers and clients.
- [**Redis Go Client**](https://github.com/redis/go-redis): Type-safe Redis client for Go.
- [**Bubble Tea**](https://github.com/charmbracelet/bubbletea): A powerful TUI framework for Go.
- [**Lip Gloss**](https://github.com/charmbracelet/lipgloss): Style and layout primitives for the terminal.
- [**LangChainGo**](https://github.com/tmc/langchaingo): A framework for building LLM-powered applications in Go.
- [**Cobra**](https://github.com/spf13/cobra): A library for creating powerful modern CLI applications.
- [**Glamour**](https://github.com/charmbracelet/glamour): Markdown rendering for the terminal.

## Prerequisites

- Go 1.26.2 or higher
- Access to an Elasticsearch cluster (URL and API Key)
- Redis server (running on `localhost:6379` by default) for caching and lookup tools
- At least one LLM API key for the CLI:
  - `OPENAI_API_KEY`
  - `ANTHROPIC_API_KEY`
  - `GEMINI_API_KEY`

## Installation

```bash
make all
```

This will build both `elastic-mcp-server` and `elastic-cli`.

## Configuration

The server and CLI require the following environment variables:

- `ELASTIC_URL`: The full URL of your Elasticsearch cluster.
- `ELASTIC_KEY`: A valid API Key for authentication.

The CLI also requires one of the following, depending on the model you choose:

- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `GEMINI_API_KEY`

Optional variables:

- `ELASTIC_MODEL`: Default CLI model ID if you do not pass `--model`.
- `ELASTIC_MCP_SERVER`: Path to the MCP server binary for the CLI and smoke-test client.
- `CLIENT_LOG_FILE`: Log file path for the CLI. Default is `elastic-cli.log`.
- `CLIENT_LOG_LEVEL`: `debug`, `info`, `warn`, or `error` for the CLI. Default is `info`.
- `CLIENT_LOG_PAYLOADS`: Set to `true` to log full CLI LLM request/response payloads. Default is off.
- `CLIENT_HISTORY_FILE`: Path to the CLI command history file. Default is `~/.elastic-cli-history`.
- `SERVER_LOG_FILE`: Log file path for the MCP server. Default is `elastic-mcp-server.log`.
- `SERVER_LOG_LEVEL`: `debug`, `info`, `warn`, or `error` for the MCP server. Default is `info`.
- `CACHE_ENABLED`: Set to `false` to disable Redis caching. Default is `true`.
- `REDIS_ADDR`: Address of the Redis server. Default is `localhost:6379`.
- `CACHE_SEARCH_SECURITY_EVENTS_TTL`: Cache TTL in seconds for `search_security_events`. Default is `600`.
- `CACHE_SEARCH_ELASTIC_TTL`: Cache TTL in seconds for `search_elastic`. Default is `600`.
- `CACHE_LIST_INDICES_TTL`: Cache TTL in seconds for `list_indices`. Default is `3600`.
- `MAX_RESPONSE_CHARS`: Maximum JSON response size returned by search tools before truncation. Default is `20000`.

## Usage

### Running the CLI (Recommended)

The CLI provides an agentic experience to interact with your security data.

```bash
export ELASTIC_URL="your_url"
export ELASTIC_KEY="your_api_key"
export OPENAI_API_KEY="your_openai_key"
./elastic-cli
```

You can also pick a model explicitly:

```bash
./elastic-cli --model gpt-5
```

### Optional Web UI

If you prefer a browser-based interface that maintains the same "security terminal" aesthetic:

```bash
./elastic-cli --webui --port 8080
```

Open `http://localhost:8080` in your browser to start.

The Web UI provides a specialized workspace for security investigations:

- **Interactive Security Console**: A modern, responsive interface designed for deep-dive security analysis.
- **Dual-Panel Workspace**:
    - **Investigation Feed**: A real-time conversation stream with the AI analyst. Includes full Markdown support for high-quality reports, data tables, and formatted analysis.
    - **Execution Trace (Tool Activity)**: A dedicated sidebar that provides visibility into the agent's thought process. Monitor tool calls as they happen, with expandable cards showing input arguments and raw output results.
- **Real-time Feedback**: Powered by WebSockets to provide immediate updates on tool progress ("Analyzing request", "Running search_security_events", etc.) and streaming responses.
- **Command History**: Efficiently navigate previous queries using `Up/Down` arrow keys, with history persisted across browser sessions.
- **Session Management**: Quickly clear context and start fresh investigations with a single click.
- **Export to Markdown**: Save your entire investigation, including both your queries and the AI's analysis, as a formatted Markdown file for easy documentation or reporting.
- **Agentic Intelligence**: The same powerful security analyst from the CLI, tuned to prefer structured tools like `search_security_events` for accurate data retrieval.

### Running the server standalone

The server communicates over Standard Input/Output (stdio) and can be used with any MCP host.

```bash
./elastic-mcp-server
```

## Troubleshooting

The CLI and Server log to files for debugging:
- `elastic-cli.log`: Contains CLI-side LLM interaction details and tool call logs (overridden by `CLIENT_LOG_FILE`).
- `elastic-mcp-server.log`: Contains MCP server-side logs and Elasticsearch interaction details (overridden by `SERVER_LOG_FILE`).

You can change the log file locations independently with `CLIENT_LOG_FILE` and `SERVER_LOG_FILE`.
Set `CLIENT_LOG_LEVEL=debug` or `SERVER_LOG_LEVEL=debug` for more detail in the corresponding process.
Set `CLIENT_LOG_PAYLOADS=true` only when you explicitly want full CLI request/response payload logging.
