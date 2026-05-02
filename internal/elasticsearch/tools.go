package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

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
	Query string `json:"query" jsonschema:"The Elasticsearch JSON query DSL string"`
}

type LookupDomainArgs struct {
	Domain string `json:"domain" jsonschema:"The exact domain name to look up (e.g. connectivity-check.ubuntu.com)"`
}

type LookupIPArgs struct {
	IP string `json:"ip" jsonschema:"The IP address to look up (e.g. 8.8.8.8)"`
}

func MaxResponseChars() int {
	if v := strings.TrimSpace(os.Getenv("MAX_RESPONSE_CHARS")); v != "" {
		if chars, err := strconv.Atoi(v); err == nil && chars > 0 {
			return chars
		}
	}
	return 20000
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
	if keepCount >= originalCount {
		keepCount = originalCount - 1
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
	if keepCount >= originalCount {
		keepCount = originalCount - 1
	}
	return s[:keepCount], true
}

func RegisterTools(server *mcp.Server, es *Client) {
	cache := NewToolCache()
	RegisterSecuritySearchTool(server, es, cache)

	// Register List Indices Tool
	listHandler := WrapWithCache(cache, "list_indices", ListIndicesTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args ListIndicesArgs) (*mcp.CallToolResult, any, error) {
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListIndicesArgs) (*mcp.CallToolResult, any, error) {
		args.Pattern = strings.TrimSpace(args.Pattern)
		return listHandler(ctx, req, args)
	})

	// Register Search Tool
	searchHandler := WrapWithCache(cache, "search_elastic", SearchElasticTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		if args.Index == "" {
			return nil, nil, fmt.Errorf("index is required")
		}
		if args.Query == "" {
			args.Query = `{"query": {"match_all": {}}}`
		}

		slog.Info("search_elastic called", "index", args.Index, "query_chars", len(args.Query))
		slog.Debug("search_elastic query", "index", args.Index, "query", args.Query)

		if !json.Valid([]byte(args.Query)) {
			slog.Warn("invalid query JSON", "index", args.Index, "query_chars", len(args.Query), "query", args.Query)
			return nil, nil, fmt.Errorf("query is not valid JSON — it may have been truncated; please retry with a complete, valid JSON query")
		}

		// Perform the search
		searchRes, err := es.Raw.Search(
			es.Raw.Search.WithContext(ctx),
			es.Raw.Search.WithIndex(args.Index),
			es.Raw.Search.WithBody(strings.NewReader(args.Query)),
		)
		if err != nil {
			slog.Error("search error", "error", err, "index", args.Index)
			return nil, nil, fmt.Errorf("search error: %w", err)
		}
		defer searchRes.Body.Close()
		if searchRes.IsError() {
			err := HttpError("search", searchRes)
			slog.Warn("search request failed", "index", args.Index, "error", err)
			return nil, nil, err
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

		truncateResults(result)
		cache.IndexSearchResult(ctx, result)

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
		Description: "Search Elasticsearch with a JSON query string",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		args.Index = strings.TrimSpace(args.Index)
		if strings.TrimSpace(args.Query) == "" {
			args.Query = `{"query": {"match_all": {}}}`
		}
		args.Query = util.NormalizeJSON(args.Query)
		return searchHandler(ctx, req, args)
	})

	// Register lookup_domain tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "lookup_domain",
		Description: "Check local cache for DNS activity history for a specific domain name. Always call this before search_elastic when investigating a domain. Returns recent DNS queries, source IPs, and resolved addresses from previously observed traffic.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LookupDomainArgs) (*mcp.CallToolResult, any, error) {
		args.Domain = util.NormalizeDomain(args.Domain)
		if args.Domain == "" {
			return nil, nil, fmt.Errorf("domain is required")
		}
		slog.Info("lookup_domain called", "domain", args.Domain)
		records, err := cache.LookupDomain(ctx, args.Domain)
		if err != nil {
			return nil, nil, fmt.Errorf("lookup_domain error: %w", err)
		}
		slog.Info("lookup_domain result", "domain", args.Domain, "records", len(records))
		out, _ := json.MarshalIndent(map[string]interface{}{
			"domain":  args.Domain,
			"records": parseJSONStrings(records),
			"total":   len(records),
		}, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
		}, nil, nil
	})

	// Register lookup_ip tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "lookup_ip",
		Description: "Check local cache for any observed activity involving an IP address. Always call this before search_elastic when investigating a specific IP. Returns DNS records where this IP appeared as an answer and DNS queries made by this IP as a source.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LookupIPArgs) (*mcp.CallToolResult, any, error) {
		args.IP = strings.TrimSpace(args.IP)
		if args.IP == "" {
			return nil, nil, fmt.Errorf("ip is required")
		}
		slog.Info("lookup_ip called", "ip", args.IP)
		dnsAnswers, dnsQueries, err := cache.LookupIP(ctx, args.IP)
		if err != nil {
			return nil, nil, fmt.Errorf("lookup_ip error: %w", err)
		}
		slog.Info("lookup_ip result", "ip", args.IP, "dns_answers", len(dnsAnswers), "dns_queries", len(dnsQueries))
		out, _ := json.MarshalIndent(map[string]interface{}{
			"ip":          args.IP,
			"dns_answers": parseJSONStrings(dnsAnswers),
			"dns_queries": parseJSONStrings(dnsQueries),
			"total":       len(dnsAnswers) + len(dnsQueries),
		}, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
		}, nil, nil
	})
}

func parseJSONStrings(ss []string) []json.RawMessage {
	out := make([]json.RawMessage, len(ss))
	for i, s := range ss {
		out[i] = json.RawMessage(s)
	}
	return out
}
