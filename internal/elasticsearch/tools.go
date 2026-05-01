package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
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

func RegisterTools(server *mcp.Server, es *elasticsearch.Client) {
	// Register List Indices Tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_indices",
		Description: "List all available Elasticsearch indices",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListIndicesArgs) (*mcp.CallToolResult, any, error) {
		slog.Info("list_indices called", "pattern", args.Pattern)

		opts := []func(*esapi.CatIndicesRequest){
			es.Cat.Indices.WithContext(ctx),
			es.Cat.Indices.WithFormat("json"),
			es.Cat.Indices.WithH("index", "docs.count", "store.size", "health"),
		}
		if args.Pattern != "" {
			opts = append(opts, es.Cat.Indices.WithIndex(args.Pattern))
		}

		res, err := es.Cat.Indices(opts...)
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
		jsonOutput, err := json.MarshalIndent(indices, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode indices response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonOutput)},
			},
		}, nil, nil
	})

	// Register Search Tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_elastic",
		Description: "Search Elasticsearch with a JSON query string",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		if args.Index == "" {
			return nil, nil, fmt.Errorf("index is required")
		}
		if args.Query == "" {
			args.Query = `{"query": {"match_all": {}}}`
		}

		slog.Info("search_elastic called", "index", args.Index, "query_chars", len(args.Query))

		// Perform the search
		searchRes, err := es.Search(
			es.Search.WithContext(ctx),
			es.Search.WithIndex(args.Index),
			es.Search.WithBody(strings.NewReader(args.Query)),
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
}
