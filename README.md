# Elastic Security MCP Server

An implementation of the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that provides tools to interact with Elasticsearch, specifically designed for security use cases.

## Features

- **list_indices**: Tool to see what indices are available in your Elasticsearch cluster, with optional pattern filtering.
- **search_elastic**: Tool to search Elasticsearch indices using the full Query DSL.

## Prerequisites

- Go 1.26.2 or higher
- Access to an Elasticsearch cluster (URL and API Key)
- Anthropic API Key (for the CLI)

## Installation

```bash
make all
```

This will build both `elastic-mcp-server` and `elastic-cli`.

## Configuration

The server and CLI require the following environment variables:

- `ELASTIC_URL`: The full URL of your Elasticsearch cluster.
- `ELASTIC_KEY`: A valid API Key for authentication.
- `ANTHROPIC_API_KEY`: Your Anthropic API key (required for `elastic-cli`).

## Usage

### Running the CLI (Recommended)

The CLI provides an agentic experience to interact with your security data.

```bash
export ELASTIC_URL="your_url"
export ELASTIC_KEY="your_api_key"
export ANTHROPIC_API_KEY="your_anthropic_key"
./elastic-cli
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

## License

[MIT](LICENSE)
