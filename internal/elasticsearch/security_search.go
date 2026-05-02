package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	typedsearch "github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/operator"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sortorder"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var highlightStripper = regexp.MustCompile(`</?em>`)

var securityTextFields = []string{
	"dns.question.name^10",
	"dns.answers.data^8",
	"url.full^8",
	"url.domain^7",
	"suricata.eve.alert.signature^9",
	"tls.client.server_name^7",
	"tls.server.subject^6",
	"tls.server.x509.subject.common_name^6",
	"message^6",
	"event.original^3",
	"source.ip^7",
	"destination.ip^7",
	"client.ip^6",
	"server.ip^6",
	"host.name^4",
	"related.ip^4",
}

var highlightFields = []string{
	"message",
	"event.original",
	"dns.question.name",
	"dns.answers.data",
	"url.full",
	"url.domain",
	"suricata.eve.alert.signature",
	"tls.client.server_name",
	"tls.server.subject",
	"tls.server.x509.subject.common_name",
}

var summaryFallbackPaths = []string{
	"message",
	"suricata.eve.alert.signature",
	"dns.question.name",
	"url.full",
	"tls.client.server_name",
	"event.original",
}

var sourceIncludes = []string{
	"@timestamp",
	"event.dataset",
	"data_stream.dataset",
	"message",
	"network.protocol",
	"source.ip",
	"source.port",
	"destination.ip",
	"destination.port",
	"client.ip",
	"server.ip",
	"dns.question.name",
	"dns.answers.data",
	"url.full",
	"host.name",
	"tls.client.server_name",
	"tls.server.subject",
	"suricata.eve.alert.signature",
}

type SearchSecurityEventsArgs struct {
	Index   string `json:"index" jsonschema:"The index pattern to search, such as logs-* or packetbeat-*"`
	Text    string `json:"text,omitempty" jsonschema:"Optional free-text query across network-heavy security fields"`
	Start   string `json:"start,omitempty" jsonschema:"Optional RFC3339 lower bound for @timestamp"`
	End     string `json:"end,omitempty" jsonschema:"Optional RFC3339 upper bound for @timestamp"`
	IP      string `json:"ip,omitempty" jsonschema:"Optional exact IP filter across common source, destination, client, server, and related IP fields"`
	SrcIP   string `json:"src_ip,omitempty" jsonschema:"Optional exact source/client IP filter"`
	DstIP   string `json:"dst_ip,omitempty" jsonschema:"Optional exact destination/server IP filter"`
	Domain  string `json:"domain,omitempty" jsonschema:"Optional exact domain filter across DNS, URL domain, and TLS SNI fields"`
	URL     string `json:"url,omitempty" jsonschema:"Optional exact full URL filter"`
	Dataset string `json:"dataset,omitempty" jsonschema:"Optional exact event dataset filter, such as zeek.dns or suricata.eve"`
	Size    int    `json:"size,omitempty" jsonschema:"Optional result count, default 10, maximum 20"`
}

func RegisterSecuritySearchTool(server *mcp.Server, es *Client, cache *ToolCache) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_security_events",
		Description: "Search ECS-style Zeek and Suricata data with typed filters, tuned boosts, filter-context constraints, and snippets-first highlighting.",
	}, WrapWithCache(cache, "search_security_events", SearchSecurityEventsTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args SearchSecurityEventsArgs) (*mcp.CallToolResult, any, error) {
		result, err := runSecuritySearch(ctx, es, cache, args)
		if err != nil {
			return nil, nil, err
		}
		jsonOutput, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode search_security_events response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(jsonOutput)}},
		}, nil, nil
	}))
}

func runSecuritySearch(ctx context.Context, es *Client, cache *ToolCache, args SearchSecurityEventsArgs) (map[string]interface{}, error) {
	if es == nil || es.Typed == nil {
		return nil, fmt.Errorf("typed elasticsearch client is not configured")
	}

	normalized, err := normalizeSecuritySearchArgs(args)
	if err != nil {
		return nil, err
	}

	req := buildSecuritySearchRequest(normalized)
	slog.Info("search_security_events called", "index", normalized.Index, "size", normalized.Size, "text", normalized.Text != "", "filters", securityFilterCount(normalized))

	resp, err := es.Typed.Search().
		Index(normalized.Index).
		Request(req).
		Do(ctx)
	if err != nil {
		slog.Error("search_security_events error", "index", normalized.Index, "error", err)
		return nil, fmt.Errorf("search_security_events error: %w", err)
	}

	if cache != nil {
		cache.IndexTypedSearchResult(ctx, resp)
	}

	output, err := shapeSecuritySearchResponse(resp)
	if err != nil {
		return nil, err
	}

	slog.Info("search_security_events result", "took", resp.Took, "hits", totalHitsValue(resp.Hits.Total))
	return output, nil
}

func normalizeSecuritySearchArgs(args SearchSecurityEventsArgs) (SearchSecurityEventsArgs, error) {
	args.Index = strings.TrimSpace(args.Index)
	args.Text = strings.TrimSpace(args.Text)
	args.Start = strings.TrimSpace(args.Start)
	args.End = strings.TrimSpace(args.End)
	args.IP = strings.TrimSpace(args.IP)
	args.SrcIP = strings.TrimSpace(args.SrcIP)
	args.DstIP = strings.TrimSpace(args.DstIP)
	args.Domain = strings.TrimSpace(args.Domain)
	args.URL = strings.TrimSpace(args.URL)
	args.Dataset = strings.TrimSpace(args.Dataset)

	if args.Index == "" {
		return args, fmt.Errorf("index is required")
	}
	if !hasSecurityConstraint(args) {
		return args, fmt.Errorf("at least one of text, start, end, ip, src_ip, dst_ip, domain, url, or dataset is required")
	}

	switch {
	case args.Size <= 0:
		args.Size = 10
	case args.Size > 20:
		args.Size = 20
	}

	return args, nil
}

func hasSecurityConstraint(args SearchSecurityEventsArgs) bool {
	return args.Text != "" || args.Start != "" || args.End != "" || args.IP != "" || args.SrcIP != "" || args.DstIP != "" || args.Domain != "" || args.URL != "" || args.Dataset != ""
}

func securityFilterCount(args SearchSecurityEventsArgs) int {
	count := 0
	for _, v := range []string{args.Start, args.End, args.IP, args.SrcIP, args.DstIP, args.Domain, args.URL, args.Dataset} {
		if v != "" {
			count++
		}
	}
	return count
}

func buildSecuritySearchRequest(args SearchSecurityEventsArgs) *typedsearch.Request {
	req := typedsearch.NewRequest()
	req.Size = &args.Size
	req.TrackTotalHits = true
	req.Source_ = &types.SourceFilter{Includes: append([]string(nil), sourceIncludes...)}
	req.Highlight = buildSecurityHighlight()
	req.Query = buildSecurityQuery(args)
	req.Sort = buildSecuritySort(args.Text != "")
	return req
}

func buildSecurityQuery(args SearchSecurityEventsArgs) *types.Query {
	boolQuery := types.NewBoolQuery()
	boolQuery.Filter = buildSecurityFilters(args)
	if args.Text != "" {
		boolQuery.Must = []types.Query{{
			MultiMatch: &types.MultiMatchQuery{
				Query:    args.Text,
				Fields:   append([]string(nil), securityTextFields...),
				Operator: &operator.And,
			},
		}}
	}
	return &types.Query{Bool: boolQuery}
}

func buildSecurityFilters(args SearchSecurityEventsArgs) []types.Query {
	filters := make([]types.Query, 0, 8)

	if ts := buildTimestampFilter(args.Start, args.End); ts != nil {
		filters = append(filters, *ts)
	}
	if args.IP != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"source.ip",
			"destination.ip",
			"client.ip",
			"server.ip",
			"related.ip",
		}, args.IP))
	}
	if args.SrcIP != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"source.ip",
			"client.ip",
		}, args.SrcIP))
	}
	if args.DstIP != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"destination.ip",
			"server.ip",
		}, args.DstIP))
	}
	if args.Domain != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"dns.question.name",
			"dns.question.name.keyword",
			"url.domain",
			"url.domain.keyword",
			"tls.client.server_name",
			"tls.client.server_name.keyword",
		}, args.Domain))
	}
	if args.URL != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"url.full",
			"url.full.keyword",
		}, args.URL))
	}
	if args.Dataset != "" {
		filters = append(filters, buildAnyTermFilter([]string{
			"event.dataset",
			"data_stream.dataset",
		}, args.Dataset))
	}

	return filters
}

func buildTimestampFilter(start, end string) *types.Query {
	if start == "" && end == "" {
		return nil
	}

	dateRange := types.NewDateRangeQuery()
	if start != "" {
		dateRange.Gte = &start
	}
	if end != "" {
		dateRange.Lte = &end
	}

	return &types.Query{
		Range: map[string]types.RangeQuery{
			"@timestamp": dateRange,
		},
	}
}

func buildAnyTermFilter(fields []string, value string) types.Query {
	should := make([]types.Query, 0, len(fields))
	for _, field := range fields {
		should = append(should, buildTermQuery(field, value))
	}
	minimumShouldMatch := types.MinimumShouldMatch("1")
	return types.Query{
		Bool: &types.BoolQuery{
			Should:             should,
			MinimumShouldMatch: minimumShouldMatch,
		},
	}
}

func buildTermQuery(field, value string) types.Query {
	return types.Query{
		Term: map[string]types.TermQuery{
			field: {Value: value},
		},
	}
}

func buildSecuritySort(hasText bool) []types.SortCombinations {
	desc := sortorder.Desc
	if hasText {
		return []types.SortCombinations{
			map[string]types.FieldSort{
				"_score": {Order: &desc},
			},
			map[string]types.FieldSort{
				"@timestamp": {Order: &desc},
			},
		}
	}
	return []types.SortCombinations{
		map[string]types.FieldSort{
			"@timestamp": {Order: &desc},
		},
	}
}

func buildSecurityHighlight() *types.Highlight {
	fragmentSize := 180
	numberOfFragments := 2
	fields := make([]map[string]types.HighlightField, 0, len(highlightFields))
	for _, field := range highlightFields {
		fields = append(fields, map[string]types.HighlightField{
			field: {
				FragmentSize:      &fragmentSize,
				NumberOfFragments: &numberOfFragments,
			},
		})
	}
	return &types.Highlight{
		Fields: fields,
	}
}

func shapeSecuritySearchResponse(resp *typedsearch.Response) (map[string]interface{}, error) {
	hits := make([]interface{}, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		shaped, err := shapeSecurityHit(hit)
		if err != nil {
			return nil, err
		}
		hits = append(hits, shaped)
	}

	out := map[string]interface{}{
		"took":  resp.Took,
		"total": formatTotalHits(resp.Hits.Total),
		"hits":  hits,
	}
	truncateSecuritySearchResults(out)
	return out, nil
}

func shapeSecurityHit(hit types.Hit) (map[string]interface{}, error) {
	source := make(map[string]interface{})
	if len(hit.Source_) > 0 {
		if err := json.Unmarshal(hit.Source_, &source); err != nil {
			return nil, fmt.Errorf("failed to decode search hit source: %w", err)
		}
	}

	compactSource := projectSource(source, sourceIncludes)
	highlights := copyHighlights(hit.Highlight)
	timestamp := firstString(compactSource, "@timestamp")
	dataset := firstString(compactSource, "event.dataset", "data_stream.dataset")

	out := map[string]interface{}{
		"_index":     hit.Index_,
		"_id":        valueOrEmpty(hit.Id_),
		"timestamp":  timestamp,
		"dataset":    dataset,
		"summary":    buildSecuritySummary(highlights, compactSource),
		"highlights": highlights,
		"source":     compactSource,
	}
	if hit.Score_ != nil {
		out["_score"] = float64(*hit.Score_)
	}
	if len(highlights) == 0 {
		delete(out, "highlights")
	}
	return out, nil
}

func projectSource(source map[string]interface{}, includes []string) map[string]interface{} {
	out := make(map[string]interface{})
	for _, path := range includes {
		value, ok := lookupPath(source, path)
		if !ok {
			continue
		}
		assignPath(out, path, value)
	}
	return out
}

func lookupPath(source map[string]interface{}, path string) (interface{}, bool) {
	current := interface{}(source)
	parts := strings.Split(path, ".")
	for _, part := range parts {
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func assignPath(target map[string]interface{}, path string, value interface{}) {
	parts := strings.Split(path, ".")
	current := target
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part].(map[string]interface{})
		if !ok {
			next = make(map[string]interface{})
			current[part] = next
		}
		current = next
	}
}

func copyHighlights(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for field, snippets := range in {
		out[field] = append([]string(nil), snippets...)
	}
	return out
}

func buildSecuritySummary(highlights map[string][]string, source map[string]interface{}) string {
	for _, field := range highlightFields {
		if snippets := highlights[field]; len(snippets) > 0 {
			return cleanSnippet(snippets[0])
		}
	}
	for _, path := range summaryFallbackPaths {
		if value := firstString(source, path); value != "" {
			return truncateSummary(value)
		}
	}
	return ""
}

func cleanSnippet(s string) string {
	s = highlightStripper.ReplaceAllString(s, "")
	return truncateSummary(strings.Join(strings.Fields(s), " "))
}

func truncateSummary(s string) string {
	if len(s) <= 220 {
		return s
	}
	return strings.TrimSpace(s[:217]) + "..."
}

func firstString(source map[string]interface{}, paths ...string) string {
	for _, path := range paths {
		if value, ok := lookupPath(source, path); ok {
			switch typed := value.(type) {
			case string:
				if typed != "" {
					return typed
				}
			case []interface{}:
				for _, item := range typed {
					if str, ok := item.(string); ok && str != "" {
						return str
					}
				}
			case []string:
				for _, item := range typed {
					if item != "" {
						return item
					}
				}
			}
		}
	}
	return ""
}

func formatTotalHits(total *types.TotalHits) map[string]interface{} {
	if total == nil {
		return map[string]interface{}{
			"value":    0,
			"relation": "eq",
		}
	}
	return map[string]interface{}{
		"value":    total.Value,
		"relation": total.Relation.String(),
	}
}

func totalHitsValue(total *types.TotalHits) int64 {
	if total == nil {
		return 0
	}
	return total.Value
}

func truncateSecuritySearchResults(result map[string]interface{}) {
	hits, ok := result["hits"].([]interface{})
	if !ok || len(hits) == 0 {
		return
	}

	maxChars := MaxResponseChars()
	data, _ := json.Marshal(result)
	if len(data) <= maxChars {
		return
	}

	originalCount := len(hits)
	keepCount := (maxChars * originalCount) / (len(data) + 1)
	keepCount = (keepCount * 9) / 10
	if keepCount < 1 {
		keepCount = 1
	}
	if keepCount >= originalCount {
		keepCount = originalCount - 1
	}

	result["hits"] = hits[:keepCount]
	result["truncated"] = true
	result["original_size_bytes"] = len(data)
	result["note"] = fmt.Sprintf("Response truncated from %d to %d hits to stay within context limits.", originalCount, keepCount)
}
