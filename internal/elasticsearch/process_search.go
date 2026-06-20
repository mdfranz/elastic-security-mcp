package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	typedsearch "github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sortorder"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchProcessesArgs struct {
	Executable  string `json:"executable,omitempty" jsonschema:"Optional exact executable path to filter (e.g., /usr/lib/systemd/systemd-executor)"`
	CommandLine string `json:"command_line,omitempty" jsonschema:"Optional command line substring to filter (e.g., systemd-executor --deserialize)"`
	ProcessName string `json:"process_name,omitempty" jsonschema:"Optional process name to filter (e.g., systemd-executor)"`
	ParentName  string `json:"parent_name,omitempty" jsonschema:"Optional parent process name to filter (e.g., systemd)"`
	User        string `json:"user,omitempty" jsonschema:"Optional username to filter (e.g., clickhouse)"`
	PID         int    `json:"pid,omitempty" jsonschema:"Optional exact process ID to filter"`
	ParentPID   int    `json:"parent_pid,omitempty" jsonschema:"Optional exact parent process ID to filter"`
	HashSHA256  string `json:"hash_sha256,omitempty" jsonschema:"Optional SHA256 hash to filter (useful for malware detection)"`
	Host        string `json:"host,omitempty" jsonschema:"Optional hostname to filter (e.g., hp-desktop-g2)"`
	Start       string `json:"start,omitempty" jsonschema:"Optional RFC3339 lower bound for @timestamp"`
	End         string `json:"end,omitempty" jsonschema:"Optional RFC3339 upper bound for @timestamp"`
	Size        int    `json:"size,omitempty" jsonschema:"Optional result count, default 20, maximum 100"`
}

func RegisterProcessSearchTool(server *mcp.Server, es *Client, cache *ToolCache) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_processes",
		Description: "Search endpoint process events with flexible filtering by executable, command line, process/parent name, user, PID, hash, host, and time range. Returns process details including parent process info, user/group, and file hash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchProcessesArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("search_processes", &err)
		result, err := runProcessSearch(ctx, es, cache, args)
		if err != nil {
			return nil, nil, err
		}
		jsonOutput, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode search_processes response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(jsonOutput)}},
		}, nil, nil
	})
}

func runProcessSearch(ctx context.Context, es *Client, cache *ToolCache, args SearchProcessesArgs) (map[string]interface{}, error) {
	if es == nil || es.Typed == nil {
		return nil, fmt.Errorf("typed elasticsearch client is not configured")
	}

	ctx = ensureSearchTimeout(ctx)
	slog.Info("search_processes called",
		"executable", args.Executable,
		"command_line", args.CommandLine,
		"process_name", args.ProcessName,
		"parent_name", args.ParentName,
		"user", args.User,
		"pid", args.PID,
		"parent_pid", args.ParentPID,
		"hash_sha256", args.HashSHA256,
		"host", args.Host,
		"start", args.Start,
		"end", args.End,
		"size", args.Size,
	)

	// Build query
	var filters []types.Query

	if args.Executable != "" {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"process.executable": {Value: args.Executable},
			},
		})
	}

	if args.CommandLine != "" {
		filters = append(filters, types.Query{
			Match: map[string]types.MatchQuery{
				"process.command_line": {Query: args.CommandLine},
			},
		})
	}

	if args.ProcessName != "" {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"process.name": {Value: args.ProcessName},
			},
		})
	}

	if args.ParentName != "" {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"process.parent.name": {Value: args.ParentName},
			},
		})
	}

	if args.User != "" {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"user.name": {Value: args.User},
			},
		})
	}

	if args.PID > 0 {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"process.pid": {Value: int64(args.PID)},
			},
		})
	}

	if args.ParentPID > 0 {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"process.parent.pid": {Value: int64(args.ParentPID)},
			},
		})
	}

	if args.HashSHA256 != "" {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"process.hash.sha256": {Value: strings.ToLower(args.HashSHA256)},
			},
		})
	}

	if args.Host != "" {
		filters = append(filters, types.Query{
			Term: map[string]types.TermQuery{
				"host.name": {Value: args.Host},
			},
		})
	}

	// Add time range filter
	if args.Start != "" || args.End != "" {
		dateRange := types.NewDateRangeQuery()
		if args.Start != "" {
			dateRange.Gte = &args.Start
		}
		if args.End != "" {
			dateRange.Lte = &args.End
		}
		filters = append(filters, types.Query{
			Range: map[string]types.RangeQuery{
				"@timestamp": dateRange,
			},
		})
	}

	// Set default size
	size := args.Size
	if size == 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}

	// Build the request using builder pattern
	req := typedsearch.NewRequest()
	req.Size = &size
	req.TrackTotalHits = true
	req.Source_ = &types.SourceFilter{
		Includes: []string{
			"@timestamp",
			"process.name",
			"process.pid",
			"process.entity_id",
			"process.executable",
			"process.command_line",
			"process.args",
			"process.working_directory",
			"process.hash.sha256",
			"process.parent.name",
			"process.parent.pid",
			"process.parent.entity_id",
			"process.parent.command_line",
			"process.parent.executable",
			"user.name",
			"user.id",
			"group.name",
			"group.id",
			"host.name",
			"host.id",
			"agent.id",
			"event.action",
			"event.outcome",
		},
	}

	// Add sort by timestamp descending
	desc := sortorder.Desc
	req.Sort = []types.SortCombinations{
		map[string]types.FieldSort{
			"@timestamp": {Order: &desc},
		},
	}

	// Add filters if any exist
	boolQuery := types.NewBoolQuery()
	if len(filters) > 0 {
		boolQuery.Filter = filters
	}
	req.Query = &types.Query{
		Bool: boolQuery,
	}

	// Execute search on process events index
	resp, err := es.Typed.Search().
		Index("logs-endpoint.events.process-*").
		Request(req).
		Do(ctx)
	if err != nil {
		slog.Error("search_processes error", "error", err)
		errMsg := fmt.Sprintf("search_processes error: %v", err)
		if strings.Contains(err.Error(), "all shards failed") {
			errMsg += " (no matching process event indices found; ensure Elastic Agent is collecting endpoint process data)"
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	if cache != nil {
		cache.IndexTypedSearchResult(ctx, resp)
	}

	// Shape response
	output := map[string]interface{}{
		"took": resp.Took,
		"hits": map[string]interface{}{
			"total": totalHitsValue(resp.Hits.Total),
			"data":  shapeProcessResults(resp.Hits.Hits),
		},
	}

	slog.Info("search_processes result", "took", resp.Took, "hits", totalHitsValue(resp.Hits.Total))
	return output, nil
}

func shapeProcessResults(hits []types.Hit) []map[string]interface{} {
	results := make([]map[string]interface{}, 0)
	for _, hit := range hits {
		if hit.Source_ == nil {
			continue
		}

		var source map[string]interface{}
		if err := json.Unmarshal(hit.Source_, &source); err != nil {
			continue
		}

		results = append(results, source)
	}
	return results
}
