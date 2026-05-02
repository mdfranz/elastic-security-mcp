# Project Evolution: Elastic Security MCP Server

## Overview

Elastic Security MCP is an implementation of the [Model Context Protocol (MCP)](https://modelcontextprotocol.org/) that connects Large Language Models (LLMs) with Elasticsearch security data. The project provides a suite of tools for security investigations, focusing on ECS-compatible data streams like Zeek and Suricata.

---

## Phase 1: Foundation & MCP Infrastructure (Commits 30bb772 → 374666f)

**Theme:** MCP server runtime and Elasticsearch integration

### Key Accomplishments
- **MCP Server Implementation**: Developed an MCP server that communicates over stdio.
- **Elasticsearch Integration**: Integrated the official Go Elasticsearch client.
- **Foundational Tools**: Implemented `list_indices` and `search_elastic` for raw query access.
- **Testing Utilities**: Added `test-mcp` to verify tool registration and protocol compliance.

### Technical Decisions
- Chose Go for performance and official client support.
- Used stdin/stdout for MCP communication to minimize external dependencies.
- Exposed raw Elasticsearch Query DSL for flexible querying.

### Output
- MCP server (`elastic-mcp-server`) compatible with MCP hosts.
- Tool set for basic Elasticsearch interaction.

---

## Phase 2: CLI & Multi-Provider LLM Support (Commits 28a8b01 → 22e1a3a)

**Theme:** CLI development and multi-provider LLM integration

### Key Accomplishments
- **CLI Development**: Built `elastic-cli` using the Bubble Tea TUI framework.
- **Multi-Provider Support**: Integrated OpenAI, Anthropic, and Google Gemini.
- **Conversation Management**: Implemented history management, tool-use loops, and iterative refinement.
- **Logging Infrastructure**: Added configurable logging for both server and client components.

### Technical Decisions
- Used LangChainGo for a consistent abstraction across LLM providers.
- Implemented separate logging for client and server to assist in debugging.
- Configuration managed via environment variables (e.g., `ELASTIC_MODEL`).

### Output
- CLI with model selection and interactive conversation.
- Support for multiple LLM providers via a single interface.

---

## Phase 3: Security-Focused Search & Caching (Commits 6e8917a → 5243e30)

**Theme:** Domain-specific search tools and caching

### Key Accomplishments
- **ECS-Aware Search**: Introduced `search_security_events` with field boosting for Zeek and Suricata data (source.ip, destination.ip, dns.question.name, etc.).
- **Redis Caching**: Implemented a Redis-backed cache for tool results to reduce Elasticsearch load.
- **Indicator Lookups**: Added `lookup_domain` and `lookup_ip` tools for fast retrieval of previously observed activity.
- **Passive Indexing**: Search results from `zeek.dns` data are automatically indexed into Redis for later lookup.
- **Result Highlighting**: Added server-side highlighting and snippet generation to surface relevant data to the LLM.

### Technical Decisions
- Redis for fast, key-value storage of tool results and security indicators.
- Standardized on ECS (Elastic Common Schema) to ensure consistent behavior across different datasets.
- Implemented "snippets-first" approach to keep LLM context focused.

### Output
- Security-focused search tool with CIDR support and field-level priority.
- Redis-backed lookup tools for domains and IP addresses.
- Passive population of indicator cache from search results.

---

## Phase 4: Performance & Optimization (Commits ad3ecd6 → 64fd61c)

**Theme:** Resource management and performance tuning

### Key Accomplishments
- **Cache Optimization**: Adjusted TTLs based on data volatility (e.g., 1 hour for indices, 10 minutes for searches).
- **Response Truncation**: Implemented logic to truncate large tool responses (default 20,000 characters) to prevent context window overflow.
- **History Pruning**: Added rolling-window history pruning (default 15 messages) in the CLI to maintain long-running sessions.
- **Token Efficiency**: Reduced redundant data in tool responses by stripping metadata from truncated results.

### Technical Decisions
- Tuned TTLs to balance data freshness with cache efficiency.
- Used a rolling window for history to preserve recent context while staying within token limits.

### Output
- Improved performance for repeated queries.
- Stable long-running CLI sessions through active context management.

---

## Architectural Themes

### 1. **Layered Abstraction**
The project provides multiple levels of access to Elasticsearch:
- **Raw** (`search_elastic`): Direct Query DSL access.
- **Typed** (`search_security_events`): Pre-configured filters and boosts for security data.
- **Cached** (`lookup_domain`, `lookup_ip`): Instant retrieval of observed indicators.

### 2. **Caching and Passive Indexing**
Redis serves two purposes:
- **Result Cache**: Caches the full output of tool calls (e.g., `list_indices`).
- **Entity Index**: Stores specific security entities (IPs, domains) extracted from search results for fast cross-referencing.

### 3. **Provider-Agnostic Design**
The CLI abstracts differences between LLM providers:
- Common tool definitions across OpenAI, Anthropic, and Gemini.
- Custom handling for provider-specific features, such as Gemini's `thoughtSignature`.

### 4. **ECS-Centric Tooling**
Assumes data follows the Elastic Common Schema:
- Tools are optimized for fields like `source.ip`, `destination.ip`, and `dns.question.name`.
- Normalization ensures consistent querying regardless of the underlying data source.

---

## Current Capabilities

### Tools
- **list_indices**: List and filter indices with health and size metadata.
- **search_security_events**: ECS-aware search with support for CIDR, MAC, and domain filters.
- **lookup_domain**: Retrieve recent DNS records and source IPs for a domain from the Redis cache.
- **lookup_ip**: Retrieve DNS activity (queries and answers) associated with an IP from the Redis cache.
- **search_elastic**: Raw Elasticsearch DSL search with response truncation.

### Configuration
- **ELASTIC_MODEL**: Default LLM model ID.
- **CACHE_ENABLED**: Toggle Redis caching (default: true).
- **REDIS_ADDR**: Redis server address (default: localhost:6379).
- **MAX_RESPONSE_CHARS**: Maximum characters returned per tool call (default: 20000).

---

## Known Limitations & Future Work

### Current Limitations
1. **Single-Cluster**: Only supports a single Elasticsearch cluster connection.
2. **Stateless Server**: The MCP server does not maintain session state; state is managed by the client or Redis.
3. **Passive Population Only**: `lookup_*` tools only contain data from previous `search_*` calls.

### Future Work
- **Token-Aware Pruning**: Switch from message-count pruning to token-count pruning for better context management.
- **Multi-Cluster Support**: Support for querying multiple Elasticsearch clusters.
- **Cache Warming**: Background tasks to pre-populate the cache for common queries.
- **Adaptive TTLs**: Dynamically adjust TTLs based on index update frequency.
