package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v9/esapi"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool Arguments
type ListIndicesArgs struct {
	Pattern string `json:"pattern,omitempty" jsonschema:"Optional index pattern to filter by (e.g. logs-*)"`
}

type SearchArgs struct {
	Index string `json:"index" jsonschema:"The index pattern to search (e.g. logs-* or .alerts-security.alerts-default)"`
	Query any    `json:"query" jsonschema:"The Elasticsearch JSON query DSL string or object"`
}

type LookupDomainArgs struct {
	Domain string `json:"domain" jsonschema:"The exact domain name to look up (e.g. connectivity-check.ubuntu.com)"`
}

type LookupIPArgs struct {
	IP string `json:"ip" jsonschema:"The IP address to look up (e.g. 8.8.8.8)"`
}

var maxResponseChars int

const defaultToolTimeout = 30 * time.Second

func init() {
	maxResponseChars = 20000
	if v := strings.TrimSpace(os.Getenv("MAX_RESPONSE_CHARS")); v != "" {
		if chars, err := strconv.Atoi(v); err == nil && chars > 0 {
			maxResponseChars = chars
		}
	}
}

func MaxResponseChars() int {
	return maxResponseChars
}

func ensureToolTimeout(ctx context.Context) context.Context {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultToolTimeout)
		_ = cancel
	}
	return ctx
}

func truncateResults(result map[string]interface{}) {
	hits, ok := result["hits"].(map[string]interface{})
	if !ok {
		return
	}
	hitsArr, ok := hits["hits"].([]interface{})
	if !ok || len(hitsArr) == 0 {
		return
	}

	maxChars := MaxResponseChars()

	// Marshal to check current size
	data, _ := json.Marshal(result)
	if len(data) <= maxChars {
		return
	}

	// Calculate how many hits to keep
	originalCount := len(hitsArr)
	// Conservative estimation (0.9 factor) to account for JSON overhead and Indent
	keepCount := (maxChars * originalCount) / (len(data) + 1)
	keepCount = (keepCount * 9) / 10

	if keepCount < 1 {
		keepCount = 1
	}
	if keepCount > originalCount {
		keepCount = originalCount
	}

	if keepCount >= originalCount {
		return
	}

	hits["hits"] = hitsArr[:keepCount]
	newHitsArr := hits["hits"].([]interface{})

	// Strip redundant metadata to save more space
	for i := range newHitsArr {
		if hit, ok := newHitsArr[i].(map[string]interface{}); ok {
			delete(hit, "_index")
			delete(hit, "_id")
			delete(hit, "_score")
		}
	}

	result["truncated"] = true
	result["original_size_bytes"] = len(data)
	result["note"] = fmt.Sprintf("Response truncated from %d to %d hits to stay within context limits.", originalCount, keepCount)
}

func truncateSlice[T any](s []T) ([]T, bool) {
	maxChars := MaxResponseChars()
	data, _ := json.Marshal(s)
	if len(data) <= maxChars {
		return s, false
	}
	originalCount := len(s)
	keepCount := (maxChars * originalCount) / len(data)
	if keepCount < 1 {
		keepCount = 1
	}
	if keepCount > originalCount {
		keepCount = originalCount
	}
	if keepCount >= originalCount {
		return s, false
	}
	return s[:keepCount], true
}

func recoverToolPanic(toolName string, err *error) {
	if r := recover(); r != nil {
		slog.Error("panic in tool handler", "tool", toolName, "panic", r)
		*err = fmt.Errorf("internal error: panic in tool %s: %v", toolName, r)
	}
}

func normalizeSearchArgs(args SearchArgs) SearchArgs {
	args.Index = strings.TrimSpace(args.Index)
	queryStr := util.StringifyJSON(args.Query)
	if strings.TrimSpace(queryStr) == "" {
		queryStr = `{"query":{"match_all":{}}}`
	}
	args.Query = util.NormalizeJSON(queryStr)
	return args
}

func RegisterTools(server *mcp.Server, es *Client) {
	cache := NewToolCache()
	RegisterSecuritySearchTool(server, es, cache)

	// Register List Indices Tool
	listHandler := WrapWithCache(cache, "list_indices", ListIndicesTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args ListIndicesArgs) (*mcp.CallToolResult, any, error) {
		ctx = ensureToolTimeout(ctx)
		slog.Info("list_indices called", "pattern", args.Pattern)

		opts := []func(*esapi.CatIndicesRequest){
			es.Raw.Cat.Indices.WithContext(ctx),
			es.Raw.Cat.Indices.WithFormat("json"),
			es.Raw.Cat.Indices.WithH("index", "docs.count", "store.size", "health"),
		}
		if args.Pattern != "" {
			opts = append(opts, es.Raw.Cat.Indices.WithIndex(args.Pattern))
		}

		res, err := es.Raw.Cat.Indices(opts...)
		if err != nil {
			slog.Error("list indices error", "error", err)
			return nil, nil, fmt.Errorf("list indices error: %w", err)
		}
		defer res.Body.Close()
		if res.IsError() {
			err := HttpError("cat indices", res)
			slog.Warn("list indices request failed", "error", err)
			return nil, nil, err
		}

		var indices []map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&indices); err != nil {
			slog.Error("failed to decode indices response", "error", err)
			return nil, nil, fmt.Errorf("failed to decode indices response: %w", err)
		}

		slog.Info("list_indices result", "count", len(indices))

		finalIndices, truncated := truncateSlice(indices)
		var output any = finalIndices
		if truncated {
			output = map[string]interface{}{
				"indices":        finalIndices,
				"truncated":      true,
				"original_count": len(indices),
				"note":           fmt.Sprintf("Index list truncated from %d to %d items to stay within context limits.", len(indices), len(finalIndices)),
			}
		}

		jsonOutput, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode indices response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonOutput)},
			},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_indices",
		Description: "List all available Elasticsearch indices",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListIndicesArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("list_indices", &err)
		args.Pattern = strings.TrimSpace(args.Pattern)
		return listHandler(ctx, req, args)
	})

	// Register Search Tool
	searchHandler := WrapWithCache(cache, "search_elastic", SearchElasticTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		ctx = ensureToolTimeout(ctx)
		if args.Index == "" {
			return nil, nil, fmt.Errorf("index is required")
		}
		queryStr := util.StringifyJSON(args.Query)
		if queryStr == "" {
			queryStr = `{"query": {"match_all": {}}}`
		}

		slog.Info("search_elastic called", "index", args.Index, "query_chars", len(queryStr))
		slog.Debug("search_elastic query", "index", args.Index, "query", queryStr)

		if !json.Valid([]byte(queryStr)) {
			slog.Warn("invalid query JSON", "index", args.Index, "query_chars", len(queryStr), "query", queryStr)
			return nil, nil, fmt.Errorf("query is not valid JSON — it may have been truncated; please retry with a complete, valid JSON query")
		}

		// Perform the search
		searchRes, err := es.Raw.Search(
			es.Raw.Search.WithContext(ctx),
			es.Raw.Search.WithIndex(args.Index),
			es.Raw.Search.WithBody(strings.NewReader(queryStr)),
		)
		if err != nil {
			slog.Error("search error", "error", err, "index", args.Index)
			return nil, nil, fmt.Errorf("search error: %w", err)
		}
		defer searchRes.Body.Close()
		if searchRes.IsError() {
			err := HttpError("search", searchRes)
			slog.Warn("search request failed", "index", args.Index, "error", err)
			errMsg := fmt.Sprintf("%v", err)
			if strings.Contains(errMsg, "all shards failed") {
				errMsg += " — index may be unhealthy or missing; try list_indices to verify the index exists"
			}
			return nil, nil, fmt.Errorf(errMsg)
		}

		// Parse the result
		var result map[string]interface{}
		if err := json.NewDecoder(searchRes.Body).Decode(&result); err != nil {
			slog.Error("failed to decode search response", "error", err)
			return nil, nil, fmt.Errorf("failed to decode search response: %w", err)
		}

		// Log summary of results
		took := result["took"]
		hits, _ := result["hits"].(map[string]interface{})
		total, _ := hits["total"].(map[string]interface{})
		value := total["value"]

		slog.Info("search_elastic result", "took", took, "hits", value)

		cache.IndexSearchResult(ctx, result)
		truncateResults(result)

		// Return formatted result
		jsonOutput, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode search response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: string(jsonOutput)},
			},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_elastic",
		Description: "Search Elasticsearch with a JSON query string or object. Important: 1. Fields containing colons (like MAC addresses) must be quoted in query_string queries (e.g., mac:\"00:11:22*\"). 2. IP fields do not support wildcards; use CIDR notation (e.g., '192.168.1.0/24') or range queries. 3. Prefer search_security_events for common filters as it handles these edge cases automatically.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("search_elastic", &err)
		args = normalizeSearchArgs(args)
		return searchHandler(ctx, req, args)
	})

	// Register Domain Lookup Tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "lookup_domain",
		Description: "Look up recent IP addresses and source activity seen for a domain in Zeek DNS logs.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LookupDomainArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("lookup_domain", &err)
		slog.Info("lookup_domain called", "domain", args.Domain)
		history, err := cache.LookupDomain(ctx, args.Domain)
		if history == nil && err == nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "No history found for domain (Redis indexing may be disabled or no events seen)."}},
			}, nil, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("lookup_domain error: %w", err)
		}

		recs := parseJSONStrings(history)
		out, _ := json.MarshalIndent(recs, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
		}, nil, nil
	})

	// Register IP Lookup Tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "lookup_ip",
		Description: "Look up recent DNS activity for an IP (answers and queries) seen in Zeek logs.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LookupIPArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("lookup_ip", &err)
		slog.Info("lookup_ip called", "ip", args.IP)
		answers, queries, err := cache.LookupIP(ctx, args.IP)
		if answers == nil && queries == nil && err == nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "No history found for IP (Redis indexing may be disabled or no events seen)."}},
			}, nil, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("lookup_ip error: %w", err)
		}

		results := map[string]interface{}{
			"dns_answers": parseJSONStrings(answers),
			"dns_queries": parseJSONStrings(queries),
		}
		out, _ := json.MarshalIndent(results, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
		}, nil, nil
	})
}

func parseJSONStrings(ss []string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(ss))
	for _, s := range ss {
		if json.Valid([]byte(s)) {
			out = append(out, json.RawMessage(s))
		} else {
			slog.Warn("skipping malformed JSON from Redis", "value", s)
		}
	}
	return out
}
