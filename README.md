# Elastic Security MCP Server

An implementation of the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that provides tools to interact with Elasticsearch, specifically designed for security use cases.

## Features

- **list_indices**: Tool to see what indices are available in your Elasticsearch cluster, with optional pattern filtering.
- **search_elastic**: Tool to search Elasticsearch indices using the full Query DSL.

## Key Libraries

This project leverages several powerful libraries:

- [**Elasticsearch Go Client**](https://github.com/elastic/go-elasticsearch): The official Go client for Elasticsearch.
- [**Model Context Protocol (MCP) SDK**](https://github.com/modelcontextprotocol/go-sdk): SDK for building MCP servers and clients.
- [**Bubble Tea**](https://github.com/charmbracelet/bubbletea): A powerful TUI framework for Go.
- [**Lip Gloss**](https://github.com/charmbracelet/lipgloss): Style and layout primitives for the terminal.
- [**LangChainGo**](https://github.com/tmc/langchaingo): A framework for building LLM-powered applications in Go.
- [**Cobra**](https://github.com/spf13/cobra): A library for creating powerful modern CLI applications.
- [**Glamour**](https://github.com/charmbracelet/glamour): Markdown rendering for the terminal.

## Prerequisites

- Go 1.26.2 or higher
- Access to an Elasticsearch cluster (URL and API Key)
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
- `MCP_LOG_FILE`: Log file path for either binary.
- `MCP_LOG_LEVEL`: `debug`, `info`, `warn`, or `error`. Default is `info`.
- `MCP_LOG_PAYLOADS`: Set to `true` to log full LLM request/response payloads. Default is off.

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

### Running the server standalone

The server communicates over Standard Input/Output (stdio) and can be used with any MCP host.

```bash
./elastic-mcp-server
```

## Troubleshooting

The CLI and Server log to files for debugging:
- `elastic-cli.log`: Contains LLM interaction details and tool call logs.
- `elastic-mcp-server.log`: Contains server-side logs and Elasticsearch interaction details.

You can change the log file locations by setting `MCP_LOG_FILE`.
Set `MCP_LOG_LEVEL=debug` for more detail.
Set `MCP_LOG_PAYLOADS=true` only when you explicitly want full request/response payload logging.

## Development

Smoke-test the MCP server tool registration:

```bash
go run ./cmd/test-mcp
```

## License

[MIT](LICENSE)
