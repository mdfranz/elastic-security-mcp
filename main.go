package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// 1. Logging Setup
	logFile := os.Getenv("MCP_LOG_FILE")
	if logFile == "" {
		logFile = "elastic-mcp-server.log"
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logFile, err)
		os.Exit(1)
	}
	defer f.Close()

	// Initialize slog to write to the file so it doesn't corrupt stdio MCP transport
	logger := slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// 2. Environment Variables
	elasticURL := os.Getenv("ELASTIC_URL")
	elasticKey := os.Getenv("ELASTIC_KEY")

	if elasticURL == "" || elasticKey == "" {
		slog.Error("ELASTIC_URL and ELASTIC_KEY environment variables must be set")
		os.Exit(1)
	}

	// 2. Initialize Elasticsearch Client
	cfg := elasticsearch.Config{
		Addresses: []string{elasticURL},
		APIKey:    elasticKey,
	}
	es, err := elasticsearch.NewClient(cfg)
	if err != nil {
		slog.Error("Error creating the elasticsearch client", "error", err)
		os.Exit(1)
	}

	// Skip connectivity check in this version for simplicity,
	// or make it optional. For now, let's keep it but handle it gracefully.
	res, err := es.Info()
	if err != nil {
		slog.Warn("Could not connect to Elasticsearch", "error", err)
	} else {
		res.Body.Close()
	}

	slog.Info("Starting elastic-mcp-server", "url", elasticURL)

	// 3. Create MCP Server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "elastic-mcp-server",
			Version: "1.0.0",
		},
		nil,
	)

	// 4. Define Tool Arguments
	type ListIndicesArgs struct{}

	type SearchArgs struct {
		Index string `json:"index" jsonschema:"The index pattern to search (e.g. logs-* or .alerts-security.alerts-default)"`
		Query string `json:"query" jsonschema:"The Elasticsearch JSON query DSL string"`
	}

	// 5a. Register List Indices Tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_indices",
		Description: "List all available Elasticsearch indices",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListIndicesArgs) (*mcp.CallToolResult, any, error) {
		slog.Info("list_indices called")
		res, err := es.Cat.Indices(
			es.Cat.Indices.WithContext(ctx),
			es.Cat.Indices.WithFormat("json"),
			es.Cat.Indices.WithH("index", "docs.count", "store.size", "health"),
		)
		if err != nil {
			slog.Error("list indices error", "error", err)
			return nil, nil, fmt.Errorf("list indices error: %w", err)
		}
		defer res.Body.Close()

		var indices []map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&indices); err != nil {
			slog.Error("failed to decode indices response", "error", err)
			return nil, nil, fmt.Errorf("failed to decode indices response: %w", err)
		}

		slog.Info("list_indices result", "count", len(indices))
		jsonOutput, _ := json.MarshalIndent(indices, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonOutput)},
			},
		}, nil, nil
	})

	// 5b. Register Search Tool
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

		slog.Info("search_elastic called", "index", args.Index, "query", args.Query)

		// Perform the search
		searchRes, err := es.Search(
			es.Search.WithContext(ctx),
			es.Search.WithIndex(args.Index),
			es.Search.WithBody(strings.NewReader(args.Query)),
			es.Search.WithPretty(),
		)
		if err != nil {
			slog.Error("search error", "error", err, "index", args.Index)
			return nil, nil, fmt.Errorf("search error: %w", err)
		}
		defer searchRes.Body.Close()

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
		jsonOutput, _ := json.MarshalIndent(result, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: string(jsonOutput),
				},
			},
		}, nil, nil
	})

	// 6. Run Server over Stdio
	slog.Info("Server listening on stdio")
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		slog.Error("server run error", "error", err)
		os.Exit(1)
	}
}
