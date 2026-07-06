# Testing

This project uses a layered testing approach. The default path is fast,
package-local Go tests that do not require live Elasticsearch, Kibana, Redis,
LLM providers, or an MCP host. Live smoke checks and model/provider checks are
available separately when those external dependencies are intentionally
configured.

## Default Go tests

Run the default test suite with:

```bash
make test
```

The `Makefile` runs:

```bash
GOCACHE=/tmp/elastic-security-mcp-go-build go test ./...
```

You can narrow the package set or pass Go test flags without editing the
Makefile:

```bash
make test TEST_PACKAGES=./internal/elasticsearch
make test GO_TEST_FLAGS="-run TestBuildSecuritySearchRequest -v"
```

These tests are intended to be local and deterministic. They prefer pure
helpers, in-memory fakes, `httptest`, custom transports, and in-process fake
services over live infrastructure.

## Unit and helper tests

Most packages test pure behavior directly, often using table-driven tests.
Examples include:

- `internal/util/normalization_test.go`: JSON and domain normalization.
- `internal/elasticsearch/normalization_test.go`: argument normalization for
  Elasticsearch tools.
- `internal/kibana/tools_test.go`: Kibana path, method, and response-format
  helper behavior.
- `internal/agent/agent_test.go`: tool-result normalization, tool-call
  summaries, history rendering, and stalling detection.
- `cmd/cli/main_test.go`: model-provider selection, input history, memory
  pruning, markdown export helpers, terminal markdown normalization, and tool
  argument formatting.

This style is preferred when behavior can be isolated without constructing a
network client or starting a process.

## Request-building and response-shaping tests

The Elasticsearch tool tests separate request construction from network
execution where possible. This keeps query semantics testable without requiring
a live cluster.

Representative files:

- `internal/elasticsearch/process_search_test.go`: process search request
  defaults, filters, pagination, result shaping, and all-shards-failed hinting.
- `internal/elasticsearch/security_search_test.go`: security event query
  construction, filter behavior, highlighting, summary fallback, term escaping,
  truncation behavior, and directional IP filters.
- `internal/elasticsearch/security_alerts_test.go`: alert argument
  normalization, request construction, and response shaping.

When adding new search behavior, prefer extracting a small request-building or
response-shaping helper and testing that helper directly.

## HTTP and WebSocket tests

Code that needs protocol behavior uses local test servers instead of real
services.

- `internal/kibana/client_test.go` uses `httptest.NewServer` to verify Kibana
  request headers, status handling, and path handling.
- `internal/webui/server_test.go` uses `httptest.NewServer` plus a WebSocket
  dialer to verify origin checks, setup messages, reset behavior, and message
  serialization.
- Some Elasticsearch tests use custom `http.RoundTripper` implementations to
  inspect outgoing requests and return controlled Elasticsearch-like JSON.

These tests validate integration boundaries while keeping the suite independent
from external services.

## Redis indexing tests

Redis-backed DNS indexing is tested with an in-process fake Redis server rather
than a container or live Redis instance.

`internal/elasticsearch/indexer_test.go` uses
`github.com/alicebob/miniredis/v2` with a real `*redis.Client` to cover:

- indexing only `zeek.dns` events,
- domain normalization before key creation,
- resolved-IP and source-IP index population,
- invalid timestamp fallback,
- history trimming,
- TTL apply and refresh behavior,
- malformed typed search result handling.

These tests are still part of the default `go test ./...` path because
`miniredis` runs in process.

## Manual MCP and cache checks

The repository includes a direct stdio script for exercising the built MCP
server:

```bash
make build
./test_cache_direct.sh
```

The script sends an MCP `initialize` request followed by two `list_indices`
tool calls. It is useful for manually checking server startup, MCP stdio
handling, and cache behavior. It expects `./elastic-mcp-server` to exist and
requires the normal runtime environment for any tool calls it exercises.

## Python smoke test client

`tools/pydantic_ai_test_mcp.py` is a Python smoke/integration harness using
`pydantic-ai`. It launches the built MCP server as a subprocess and runs fixed
security-investigation tasks through an LLM-backed agent.

Run it through the Makefile:

```bash
make build
make smoketest
```

Or run it directly:

```bash
uv run tools/pydantic_ai_test_mcp.py claude-haiku-4-5
```

This is not part of the default Go test suite. It requires:

- `ELASTIC_URL`
- `ELASTIC_KEY`
- at least one matching provider key such as `ANTHROPIC_API_KEY`,
  `OPENAI_API_KEY`, `GEMINI_API_KEY`, or `GOOGLE_API_KEY`
- optional `KIBANA_URL` and Kibana credentials if Kibana-backed behavior should
  be available

Logs and token totals are written to `pydantic_ai_test.log`.

## Model/provider checks

`test-models.sh` and the `modeltest` Make target build both binaries and run
the CLI prompt path against each configured provider model.

```bash
make modeltest
make modeltest MODELTEST_PROMPT="list indices"
```

The script runs only the providers that have API keys in the environment:

- `OPENAI_API_KEY`
- `ANTHROPIC_API_KEY`
- `GEMINI_API_KEY`

It writes per-model client and server logs such as
`test_client_<model>_<timestamp>.log` and
`test_server_<model>_<timestamp>.log`. Treat this as an explicit live
compatibility check, not a default correctness test.

## Coverage planning

Detailed coverage status and remaining test gaps are tracked in
`docs/TEST-COVERAGE-PLAN.md`. That document is the place to update phase status
or describe future test work. This file summarizes how the current test
approaches fit together and how to run them.
