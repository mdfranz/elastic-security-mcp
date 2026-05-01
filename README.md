# Elastic Security MCP Server

An implementation of the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that provides tools to interact with Elasticsearch, specifically designed for security use cases.

## Features

- **search_elastic**: Tool to search Elasticsearch indices using the full Query DSL.

## Prerequisites

- Go 1.22 or higher
- Access to an Elasticsearch cluster (URL and API Key)

## Installation

```bash
go build -o elastic-mcp-server main.go
```

Or using the provided Makefile:

```bash
make build
```

## Configuration

The server requires the following environment variables to be set:

- `ELASTIC_URL`: The full URL of your Elasticsearch cluster (e.g., `https://your-cluster.es.us-east-1.aws.found.io:9243`)
- `ELASTIC_KEY`: A valid API Key for authentication.

## Usage

### Running the server

```bash
export ELASTIC_URL="your_url"
export ELASTIC_KEY="your_api_key"
./elastic-mcp-server
```

The server communicates over Standard Input/Output (stdio).

### Tools

#### `search_elastic`

Searches Elasticsearch with a JSON query string.

**Arguments:**
- `index` (string, required): The index or data stream to search in.
- `query` (string, optional): The JSON search DSL query string. Defaults to a `match_all` query if omitted.

**Example Query:**
```json
{
  "index": "logs-zeek.connection-default",
  "query": "{\"query\": {\"match_all\": {}}, \"size\": 5}"
}
```

## License

[MIT](LICENSE)
