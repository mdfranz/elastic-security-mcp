package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// 1. Environment Variables
	elasticURL := os.Getenv("ELASTIC_URL")
	elasticKey := os.Getenv("ELASTIC_KEY")

	if elasticURL == "" || elasticKey == "" {
		log.Fatal("ELASTIC_URL and ELASTIC_KEY environment variables must be set")
	}

	// 2. Initialize Elasticsearch Client
	cfg := elasticsearch.Config{
		Addresses: []string{elasticURL},
		APIKey:    elasticKey,
	}
	es, err := elasticsearch.NewClient(cfg)
	if err != nil {
		log.Fatalf("Error creating the elasticsearch client: %s", err)
	}

	// Skip connectivity check in this version for simplicity, 
	// or make it optional. For now, let's keep it but handle it gracefully.
	res, err := es.Info()
	if err != nil {
		log.Printf("Warning: Could not connect to Elasticsearch: %s", err)
	} else {
		res.Body.Close()
	}

	// 3. Create MCP Server
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "elastic-mcp-server",
			Version: "1.0.0",
		},
		nil,
	)

	// 4. Define Search Tool Arguments
	type SearchArgs struct {
		Index string `json:"index" jsonschema:"The index to search in"`
		Query string `json:"query" jsonschema:"The JSON search DSL query string"`
	}

	// 5. Register Search Tool
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

		// Perform the search
		searchRes, err := es.Search(
			es.Search.WithContext(ctx),
			es.Search.WithIndex(args.Index),
			es.Search.WithBody(strings.NewReader(args.Query)),
			es.Search.WithPretty(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("search error: %w", err)
		}
		defer searchRes.Body.Close()

		// Parse the result
		var result map[string]interface{}
		if err := json.NewDecoder(searchRes.Body).Decode(&result); err != nil {
			return nil, nil, fmt.Errorf("failed to decode search response: %w", err)
		}

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
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
