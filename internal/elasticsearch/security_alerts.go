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

var alertsSourceIncludes = []string{
	"@timestamp",
	"message",
	"kibana.alert.rule.name",
	"kibana.alert.rule.uuid",
	"kibana.alert.rule.parameters.severity",
	"kibana.alert.rule.parameters.risk_score",
	"kibana.alert.rule.parameters.description",
	"agent.id",
	"host.name",
	"process.name",
	"process.executable",
	"process.command_line",
	"process.hash.sha256",
	"process.parent.name",
	"process.parent.executable",
	"process.parent.command_line",
}

type SearchSecurityAlertsArgs struct {
	Query    string `json:"query,omitempty" jsonschema:"Optional free-text query string (e.g. 'Malware' or 'wget')"`
	Severity string `json:"severity,omitempty" jsonschema:"Optional severity filter (e.g. low, medium, high, critical)"`
	RuleName string `json:"rule_name,omitempty" jsonschema:"Optional rule name filter (wildcards supported, e.g. *Malware*)"`
	Host     string `json:"host,omitempty" jsonschema:"Optional host name filter"`
	Start    string `json:"start,omitempty" jsonschema:"Optional RFC3339 lower bound for @timestamp"`
	End      string `json:"end,omitempty" jsonschema:"Optional RFC3339 upper bound for @timestamp"`
	Size     int    `json:"size,omitempty" jsonschema:"Optional result count, default 10, maximum 50"`
}

func RegisterSecurityAlertsTool(server *mcp.Server, es *Client, cache *ToolCache) {
	innerHandler := WrapWithCache(cache, "search_security_alerts", SearchSecurityEventsTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args SearchSecurityAlertsArgs) (*mcp.CallToolResult, any, error) {
		result, err := runSecurityAlertsSearch(ctx, es, args)
		if err != nil {
			return nil, nil, err
		}
		jsonOutput, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode search_security_alerts response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(jsonOutput)}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_security_alerts",
		Description: "Search Elastic Security detection alerts stored in .alerts-security.alerts-* indices.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchSecurityAlertsArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("search_security_alerts", &err)
		normalized := normalizeSecurityAlertsArgs(args)
		return innerHandler(ctx, req, normalized)
	})
}

func runSecurityAlertsSearch(ctx context.Context, es *Client, args SearchSecurityAlertsArgs) (map[string]interface{}, error) {
	if es == nil || es.Typed == nil {
		return nil, fmt.Errorf("typed elasticsearch client is not configured")
	}

	ctx = ensureSearchTimeout(ctx)
	req := buildSecurityAlertsRequest(args)
	indexPattern := ".alerts-security.alerts-*"

	slog.Info("search_security_alerts called", "query", args.Query, "severity", args.Severity, "rule_name", args.RuleName, "host", args.Host)

	resp, err := es.Typed.Search().
		Index(indexPattern).
		Request(req).
		Do(ctx)
	if err != nil {
		slog.Error("search_security_alerts error", "error", err)
		return nil, fmt.Errorf("search_security_alerts error: %w", err)
	}

	output, err := shapeSecurityAlertsResponse(resp)
	if err != nil {
		return nil, err
	}

	slog.Info("search_security_alerts result", "took", resp.Took, "hits", totalHitsValue(resp.Hits.Total))
	return output, nil
}

func normalizeSecurityAlertsArgs(args SearchSecurityAlertsArgs) SearchSecurityAlertsArgs {
	args.Query = strings.TrimSpace(args.Query)
	args.Severity = strings.TrimSpace(args.Severity)
	args.RuleName = strings.TrimSpace(args.RuleName)
	args.Host = strings.TrimSpace(args.Host)
	args.Start = strings.TrimSpace(args.Start)
	args.End = strings.TrimSpace(args.End)

	if args.Size <= 0 {
		args.Size = 10
	} else if args.Size > 50 {
		args.Size = 50
	}

	return args
}

func buildSecurityAlertsRequest(args SearchSecurityAlertsArgs) *typedsearch.Request {
	req := typedsearch.NewRequest()
	req.Size = &args.Size
	req.TrackTotalHits = true
	req.Source_ = &types.SourceFilter{Includes: append([]string(nil), alertsSourceIncludes...)}
	req.Query = buildSecurityAlertsQuery(args)
	desc := sortorder.Desc
	req.Sort = []types.SortCombinations{
		map[string]types.FieldSort{
			"@timestamp": {Order: &desc},
		},
	}
	return req
}

func buildSecurityAlertsQuery(args SearchSecurityAlertsArgs) *types.Query {
	boolQuery := types.NewBoolQuery()
	filters := make([]types.Query, 0, 6)

	if ts := buildTimestampFilter(args.Start, args.End); ts != nil {
		filters = append(filters, *ts)
	}

	if args.Severity != "" {
		filters = append(filters, buildTermQuery("kibana.alert.rule.parameters.severity", args.Severity))
	}

	if args.RuleName != "" {
		filters = append(filters, buildTermQuery("kibana.alert.rule.name", args.RuleName))
	}

	if args.Host != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"host.name",
			"host.name.keyword",
		}, args.Host))
	}

	boolQuery.Filter = filters

	if args.Query != "" {
		boolQuery.Must = []types.Query{{
			QueryString: &types.QueryStringQuery{
				Query: args.Query,
			},
		}}
	}

	return &types.Query{Bool: boolQuery}
}

func shapeSecurityAlertsResponse(resp *typedsearch.Response) (map[string]interface{}, error) {
	hits := make([]interface{}, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		source := make(map[string]interface{})
		if len(hit.Source_) > 0 {
			if err := json.Unmarshal(hit.Source_, &source); err != nil {
				return nil, fmt.Errorf("failed to decode search hit source: %w", err)
			}
		}

		compactSource := projectSource(source, alertsSourceIncludes)
		timestamp := firstString(compactSource, "@timestamp")
		ruleName := firstString(compactSource, "kibana.alert.rule.name")
		message := firstString(compactSource, "message")
		severity := firstString(compactSource, "kibana.alert.rule.parameters.severity")

		out := map[string]interface{}{
			"_index":     hit.Index_,
			"_id":        valueOrEmpty(hit.Id_),
			"timestamp":  timestamp,
			"rule_name":  ruleName,
			"severity":   severity,
			"message":    message,
			"source":     compactSource,
		}
		hits = append(hits, out)
	}

	out := map[string]interface{}{
		"took":  resp.Took,
		"total": formatTotalHits(resp.Hits.Total),
		"hits":  hits,
	}
	return out, nil
}
