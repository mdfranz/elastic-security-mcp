package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	typedsearch "github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/calendarinterval"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SecurityStatsArgs struct {
	Index              string `json:"index" jsonschema:"Index pattern to analyze, for example logs-zeek.*-* or logs-suricata.*-*"`
	Query              string `json:"query,omitempty" jsonschema:"Optional Elasticsearch query_string filter to apply within the required time range"`
	Field              string `json:"field,omitempty" jsonschema:"Field to aggregate. Required for terms and cardinality; use an aggregatable keyword/IP/numeric field (for example event.dataset, source.ip, or dns.question.name.keyword). Optional for date_histogram, which defaults to @timestamp."`
	AggregationType    string `json:"aggregation_type" jsonschema:"Aggregation type: terms, date_histogram, or cardinality"`
	Interval           string `json:"interval,omitempty" jsonschema:"date_histogram only: calendar 1m, 1h, 1d, 1w, 1M, 1q, or 1y; or a fixed interval such as 15m or 6h. Defaults to 1h. Retention is ~30 days, so short fixed intervals (15m/1h/4h/8h/24h) are the common case."`
	Start              string `json:"start" jsonschema:"RFC3339 inclusive lower bound for @timestamp"`
	End                string `json:"end" jsonschema:"RFC3339 inclusive upper bound for @timestamp"`
	Size               int    `json:"size,omitempty" jsonschema:"terms only: maximum buckets to return; default 10, maximum 100"`
	IncludeTotal       bool   `json:"include_total,omitempty" jsonschema:"Request an exact matching-document count. Defaults to false because exact totals can be expensive on large ranges."`
	PrecisionThreshold int    `json:"precision_threshold,omitempty" jsonschema:"cardinality only: optional Elasticsearch precision threshold from 1 to 40000; higher values use more memory and improve accuracy."`
}

const (
	defaultStatsSize      = 10
	maxStatsSize          = 100
	minPrecisionThreshold = 1
	maxPrecisionThreshold = 40000
	defaultStatsInterval  = "1h"
)

var validStatsAggregationTypes = map[string]bool{
	"terms":          true,
	"date_histogram": true,
	"cardinality":    true,
}

// calendarShorthandIntervals are the only calendar_interval values this tool
// accepts. Elasticsearch's calendar_interval only supports single multiples
// (no "2M"), and the case distinguishes calendar minute ("1m") from calendar
// month ("1M") — do not lowercase/uppercase this input.
var calendarShorthandIntervals = map[string]bool{
	"1m": true,
	"1h": true,
	"1d": true,
	"1w": true,
	"1M": true,
	"1q": true,
	"1y": true,
}

var fixedIntervalPattern = regexp.MustCompile(`^[1-9][0-9]*(ms|s|m|h|d)$`)

var statsMaxRangeHours int
var statsMaxBuckets int

func init() {
	statsMaxRangeHours = 31 * 24
	if v := strings.TrimSpace(os.Getenv("STATS_MAX_RANGE_HOURS")); v != "" {
		if hours, err := strconv.Atoi(v); err == nil && hours > 0 {
			statsMaxRangeHours = hours
		}
	}
	statsMaxBuckets = 250
	if v := strings.TrimSpace(os.Getenv("STATS_MAX_BUCKETS")); v != "" {
		if buckets, err := strconv.Atoi(v); err == nil && buckets > 0 {
			statsMaxBuckets = buckets
		}
	}
}

// StatsMaxRangeHours returns the maximum allowed start/end span for
// search_security_stats, configurable via STATS_MAX_RANGE_HOURS (default 744h / 31 days).
func StatsMaxRangeHours() int {
	return statsMaxRangeHours
}

// StatsMaxBuckets returns the maximum allowed estimated date_histogram bucket
// count for search_security_stats, configurable via STATS_MAX_BUCKETS (default 250).
func StatsMaxBuckets() int {
	return statsMaxBuckets
}

func isCalendarInterval(interval string) bool {
	return calendarShorthandIntervals[interval]
}

func isValidFixedInterval(interval string) bool {
	return fixedIntervalPattern.MatchString(interval)
}

// parseFixedIntervalDuration converts a validated fixed interval (e.g. "15m",
// "6h") into its exact Duration. Returns 0 if interval isn't a valid fixed
// interval — callers must validate with isValidFixedInterval first.
func parseFixedIntervalDuration(interval string) time.Duration {
	m := fixedIntervalPattern.FindStringSubmatch(interval)
	if m == nil {
		return 0
	}
	unit := m[1]
	n, err := strconv.Atoi(strings.TrimSuffix(interval, unit))
	if err != nil {
		return 0
	}
	switch unit {
	case "ms":
		return time.Duration(n) * time.Millisecond
	case "s":
		return time.Duration(n) * time.Second
	case "m":
		return time.Duration(n) * time.Minute
	case "h":
		return time.Duration(n) * time.Hour
	case "d":
		return time.Duration(n) * 24 * time.Hour
	}
	return 0
}

// addCalendarInterval advances t by one calendar-interval step. Month/quarter/year
// use AddDate so variable month lengths are handled exactly rather than approximated.
func addCalendarInterval(t time.Time, interval string) time.Time {
	switch interval {
	case "1m":
		return t.Add(time.Minute)
	case "1h":
		return t.Add(time.Hour)
	case "1d":
		return t.AddDate(0, 0, 1)
	case "1w":
		return t.AddDate(0, 0, 7)
	case "1M":
		return t.AddDate(0, 1, 0)
	case "1q":
		return t.AddDate(0, 3, 0)
	case "1y":
		return t.AddDate(1, 0, 0)
	default:
		return t.Add(time.Hour)
	}
}

// estimateBucketCount computes an upper bound on the number of date_histogram
// buckets a [start,end] range/interval combination would produce, bailing out
// early once it exceeds cap (the caller only needs to know "exceeds the limit",
// not the exact count for pathologically large ranges).
func estimateBucketCount(start, end time.Time, interval string, isCalendar bool, cap int) int {
	if !isCalendar {
		d := parseFixedIntervalDuration(interval)
		if d <= 0 {
			return 0
		}
		return int(end.Sub(start)/d) + 1
	}

	count := 0
	cursor := start
	for cursor.Before(end) {
		cursor = addCalendarInterval(cursor, interval)
		count++
		if count > cap {
			return count
		}
	}
	return count
}

func normalizeSecurityStatsArgs(args SecurityStatsArgs) (SecurityStatsArgs, error) {
	args.Index = strings.TrimSpace(args.Index)
	args.Query = strings.TrimSpace(args.Query)
	args.Field = strings.TrimSpace(args.Field)
	args.AggregationType = strings.ToLower(strings.TrimSpace(args.AggregationType))
	args.Interval = strings.TrimSpace(args.Interval)
	args.Start = strings.TrimSpace(args.Start)
	args.End = strings.TrimSpace(args.End)

	if args.Index == "" {
		return args, fmt.Errorf("index is required")
	}
	if !validStatsAggregationTypes[args.AggregationType] {
		return args, fmt.Errorf("aggregation_type must be one of terms, date_histogram, or cardinality")
	}

	start, err := time.Parse(time.RFC3339, args.Start)
	if err != nil {
		return args, fmt.Errorf("start must be a valid RFC3339 timestamp: %w", err)
	}
	end, err := time.Parse(time.RFC3339, args.End)
	if err != nil {
		return args, fmt.Errorf("end must be a valid RFC3339 timestamp: %w", err)
	}
	if end.Before(start) {
		return args, fmt.Errorf("end must not precede start")
	}
	maxRange := time.Duration(StatsMaxRangeHours()) * time.Hour
	if end.Sub(start) > maxRange {
		return args, fmt.Errorf("time range of %s exceeds the maximum of %s; use a shorter start/end window", end.Sub(start), maxRange)
	}

	switch args.AggregationType {
	case "terms":
		if args.Field == "" {
			return args, fmt.Errorf("field is required for terms aggregation")
		}
		switch {
		case args.Size <= 0:
			args.Size = defaultStatsSize
		case args.Size > maxStatsSize:
			args.Size = maxStatsSize
		}

	case "cardinality":
		if args.Field == "" {
			return args, fmt.Errorf("field is required for cardinality aggregation")
		}
		if args.PrecisionThreshold != 0 && (args.PrecisionThreshold < minPrecisionThreshold || args.PrecisionThreshold > maxPrecisionThreshold) {
			return args, fmt.Errorf("precision_threshold must be between %d and %d", minPrecisionThreshold, maxPrecisionThreshold)
		}

	case "date_histogram":
		if args.Field == "" {
			args.Field = "@timestamp"
		}
		if args.Interval == "" {
			args.Interval = defaultStatsInterval
		}
		isCalendar := isCalendarInterval(args.Interval)
		if !isCalendar && !isValidFixedInterval(args.Interval) {
			return args, fmt.Errorf("interval %q is not a supported calendar interval (1m, 1h, 1d, 1w, 1M, 1q, 1y) or fixed interval (e.g. 15m, 6h)", args.Interval)
		}
		maxBuckets := StatsMaxBuckets()
		if estimated := estimateBucketCount(start, end, args.Interval, isCalendar, maxBuckets); estimated > maxBuckets {
			return args, fmt.Errorf("date_histogram would produce approximately %d buckets, exceeding the maximum of %d; use a shorter time range or a coarser interval", estimated, maxBuckets)
		}
	}

	return args, nil
}

func buildSecurityStatsRequest(args SecurityStatsArgs) *typedsearch.Request {
	req := typedsearch.NewRequest()
	zero := 0
	req.Size = &zero
	req.TrackTotalHits = args.IncludeTotal

	filters := make([]types.Query, 0, 2)
	if ts := buildTimestampFilter(args.Start, args.End); ts != nil {
		filters = append(filters, *ts)
	}
	if args.Query != "" {
		filters = append(filters, types.Query{QueryString: &types.QueryStringQuery{Query: args.Query}})
	}
	req.Query = &types.Query{Bool: &types.BoolQuery{Filter: filters}}

	agg := types.Aggregations{}
	switch args.AggregationType {
	case "terms":
		agg.Terms = &types.TermsAggregation{Field: &args.Field, Size: &args.Size}

	case "date_histogram":
		dh := &types.DateHistogramAggregation{Field: &args.Field}
		if isCalendarInterval(args.Interval) {
			dh.CalendarInterval = &calendarinterval.CalendarInterval{Name: args.Interval}
		} else {
			dh.FixedInterval = args.Interval
		}
		agg.DateHistogram = dh

	case "cardinality":
		ca := &types.CardinalityAggregation{Field: &args.Field}
		if args.PrecisionThreshold > 0 {
			ca.PrecisionThreshold = &args.PrecisionThreshold
		}
		agg.Cardinality = ca
	}
	req.Aggregations = map[string]types.Aggregations{"stats": agg}

	return req
}

func runSecurityStatsSearch(ctx context.Context, es *Client, args SecurityStatsArgs) (map[string]interface{}, error) {
	if es == nil || es.Typed == nil {
		return nil, fmt.Errorf("typed elasticsearch client is not configured")
	}

	ctx, cancel := ensureSearchTimeout(ctx)
	defer cancel()

	req := buildSecurityStatsRequest(args)
	slog.Info("search_security_stats called", "index", args.Index, "aggregation_type", args.AggregationType, "field", args.Field, "interval", args.Interval, "start", args.Start, "end", args.End, "size", args.Size, "include_total", args.IncludeTotal)
	if queryJSON, err := json.Marshal(req); err == nil {
		slog.Debug("search_security_stats query", "index", args.Index, "query", string(queryJSON))
	}

	start := time.Now()
	resp, err := es.Typed.Search().
		Index(args.Index).
		Request(req).
		TypedKeys(true).
		Do(ctx)
	if err != nil {
		slog.Error("search_security_stats error", "index", args.Index, "latency_ms", time.Since(start).Milliseconds(), "error", err)
		errMsg := fmt.Sprintf("search_security_stats error: %v", err)
		if strings.Contains(err.Error(), "all shards failed") {
			errMsg += " (all shards failed — index may be unhealthy or missing fields; try with a specific index name or use list_indices to verify)"
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	if resp.TimedOut {
		return nil, fmt.Errorf("search_security_stats: search timed out before completion — narrow the time range or check cluster_health")
	}
	if resp.Shards_.Failed > 0 {
		return nil, fmt.Errorf("search_security_stats: %d of %d shards failed — results would be incomplete; check cluster_health or narrow the index pattern", resp.Shards_.Failed, resp.Shards_.Total)
	}

	output, err := shapeSecurityStatsResponse(resp, args)
	if err != nil {
		return nil, err
	}

	slog.Info("search_security_stats result", "took_ms", resp.Took, "latency_ms", time.Since(start).Milliseconds())
	return output, nil
}

func shapeSecurityStatsResponse(resp *typedsearch.Response, args SecurityStatsArgs) (map[string]interface{}, error) {
	agg, ok := resp.Aggregations["stats"]
	if !ok {
		return nil, fmt.Errorf("search_security_stats: response missing expected \"stats\" aggregation")
	}

	var stats map[string]interface{}
	var err error
	switch args.AggregationType {
	case "terms":
		stats, err = shapeTermsAggregate(agg, args)
	case "date_histogram":
		stats, err = shapeDateHistogramAggregate(agg, args)
	case "cardinality":
		stats, err = shapeCardinalityAggregate(agg, args)
	default:
		return nil, fmt.Errorf("search_security_stats: unsupported aggregation_type %q", args.AggregationType)
	}
	if err != nil {
		return nil, err
	}

	out := map[string]interface{}{
		"aggregation_type": args.AggregationType,
		"index":            args.Index,
		"time_range": map[string]interface{}{
			"start": args.Start,
			"end":   args.End,
		},
		"took_ms":      resp.Took,
		"total_events": formatStatsTotalEvents(resp.Hits.Total),
		"stats":        stats,
	}

	if args.AggregationType == "terms" {
		if err := truncateSecurityStatsBuckets(out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// formatStatsTotalEvents is distinct from formatTotalHits (security_search.go):
// that helper's nil-total case reports {value:0, relation:"eq"}, which is safe
// there because those callers always set TrackTotalHits=true. Here IncludeTotal
// defaults to false, so total is nil on nearly every call — reporting that as
// an exact zero would misrepresent "we didn't ask" as "we found nothing".
func formatStatsTotalEvents(total *types.TotalHits) map[string]interface{} {
	if total == nil {
		return map[string]interface{}{
			"value":    nil,
			"relation": "unknown",
			"exact":    false,
		}
	}
	relation := total.Relation.String()
	return map[string]interface{}{
		"value":    total.Value,
		"relation": relation,
		"exact":    relation == "eq",
	}
}

func shapeTermsAggregate(agg types.Aggregate, args SecurityStatsArgs) (map[string]interface{}, error) {
	buckets := []map[string]interface{}{}
	var sumOther *int64
	var docCountErr *int64
	unmapped := false

	switch a := agg.(type) {
	case *types.StringTermsAggregate:
		sumOther, docCountErr = a.SumOtherDocCount, a.DocCountErrorUpperBound
		raw, ok := a.Buckets.([]types.StringTermsBucket)
		if !ok {
			return nil, fmt.Errorf("search_security_stats: unexpected terms bucket shape for field %q", args.Field)
		}
		for _, b := range raw {
			buckets = append(buckets, map[string]interface{}{"key": b.Key, "doc_count": b.DocCount})
		}

	case *types.LongTermsAggregate:
		sumOther, docCountErr = a.SumOtherDocCount, a.DocCountErrorUpperBound
		raw, ok := a.Buckets.([]types.LongTermsBucket)
		if !ok {
			return nil, fmt.Errorf("search_security_stats: unexpected terms bucket shape for field %q", args.Field)
		}
		for _, b := range raw {
			buckets = append(buckets, map[string]interface{}{"key": b.Key, "doc_count": b.DocCount})
		}

	case *types.DoubleTermsAggregate:
		sumOther, docCountErr = a.SumOtherDocCount, a.DocCountErrorUpperBound
		raw, ok := a.Buckets.([]types.DoubleTermsBucket)
		if !ok {
			return nil, fmt.Errorf("search_security_stats: unexpected terms bucket shape for field %q", args.Field)
		}
		for _, b := range raw {
			buckets = append(buckets, map[string]interface{}{"key": b.Key, "doc_count": b.DocCount})
		}

	case *types.UnmappedTermsAggregate:
		// Field isn't present/mapped in the selected indices — an empty
		// result is the correct answer, not an error. Flagged explicitly
		// (below) so this doesn't look identical to a mapped field that
		// simply has zero matches in the time range.
		sumOther, docCountErr = a.SumOtherDocCount, a.DocCountErrorUpperBound
		unmapped = true

	default:
		return nil, fmt.Errorf("search_security_stats: unexpected terms aggregation response type %T", agg)
	}

	stats := map[string]interface{}{
		"field":   args.Field,
		"buckets": buckets,
	}
	if sumOther != nil {
		stats["sum_other_doc_count"] = *sumOther
	}
	if docCountErr != nil {
		stats["doc_count_error_upper_bound"] = *docCountErr
	}
	if unmapped {
		stats["unmapped"] = true
		stats["note"] = fmt.Sprintf("field %q is not mapped in the matched indices — check the field name (list_indices/search_elastic can confirm the mapping) rather than assuming this is an empty result", args.Field)
	}
	return stats, nil
}

func shapeDateHistogramAggregate(agg types.Aggregate, args SecurityStatsArgs) (map[string]interface{}, error) {
	a, ok := agg.(*types.DateHistogramAggregate)
	if !ok {
		return nil, fmt.Errorf("search_security_stats: unexpected date_histogram aggregation response type %T", agg)
	}
	raw, ok := a.Buckets.([]types.DateHistogramBucket)
	if !ok {
		return nil, fmt.Errorf("search_security_stats: unexpected date_histogram bucket shape")
	}

	buckets := make([]map[string]interface{}, 0, len(raw))
	for _, b := range raw {
		bucket := map[string]interface{}{"key": b.Key, "doc_count": b.DocCount}
		if b.KeyAsString != nil {
			bucket["key_as_string"] = *b.KeyAsString
		}
		buckets = append(buckets, bucket)
	}

	return map[string]interface{}{
		"field":    args.Field,
		"interval": args.Interval,
		"buckets":  buckets,
	}, nil
}

func shapeCardinalityAggregate(agg types.Aggregate, args SecurityStatsArgs) (map[string]interface{}, error) {
	a, ok := agg.(*types.CardinalityAggregate)
	if !ok {
		return nil, fmt.Errorf("search_security_stats: unexpected cardinality aggregation response type %T", agg)
	}

	stats := map[string]interface{}{
		"field":       args.Field,
		"value":       a.Value,
		"approximate": true,
	}
	if args.PrecisionThreshold > 0 {
		stats["precision_threshold"] = args.PrecisionThreshold
	}
	return stats, nil
}

// truncateSecurityStatsBuckets mirrors truncateSecuritySearchResults
// (security_search.go) rather than the generic truncateSlice helper in
// tools.go: truncateSlice sizes an array against the *entire* char budget with
// no margin, which is right when the array essentially *is* the whole payload
// (e.g. list_indices), but wrong here — the bucket list sits inside an
// envelope (time_range, total_events, ...) plus metadata this function itself
// adds (truncated/note), so sizing buckets alone against the full budget
// leaves no room for that overhead and would make the response reliably too
// big anyway. So, like truncateSecuritySearchResults, this marshals the whole
// envelope up front and applies a 10% margin. Returns an error — rather than
// silently emitting an ambiguous partial payload — if even the smallest
// possible bucket list still doesn't fit.
func truncateSecurityStatsBuckets(out map[string]interface{}) error {
	stats, ok := out["stats"].(map[string]interface{})
	if !ok {
		return nil
	}
	buckets, ok := stats["buckets"].([]map[string]interface{})
	if !ok || len(buckets) == 0 {
		return nil
	}

	maxChars := MaxResponseChars()
	data, err := json.Marshal(out)
	if err != nil || len(data) <= maxChars {
		return nil
	}

	originalCount := len(buckets)
	keepCount := (maxChars * originalCount) / len(data)
	keepCount = (keepCount * 9) / 10
	if keepCount < 1 {
		keepCount = 1
	}
	if keepCount > originalCount {
		keepCount = originalCount
	}
	if keepCount >= originalCount {
		return nil
	}

	stats["buckets"] = buckets[:keepCount]
	out["truncated"] = true
	out["original_bucket_count"] = originalCount
	out["note"] = fmt.Sprintf("Response truncated from %d to %d buckets to stay within context limits.", originalCount, keepCount)

	if data, err := json.Marshal(out); err == nil && len(data) > maxChars {
		return fmt.Errorf("search_security_stats: response cannot fit within the response size limit even after truncating to %d bucket(s) — reduce size or narrow the query/time range", keepCount)
	}
	return nil
}

func RegisterSecurityStatsTool(server *mcp.Server, es *Client, cache *ToolCache) {
	innerHandler := WrapWithCache(cache, "search_security_stats", SearchSecurityStatsTTL(), func(ctx context.Context, req *mcp.CallToolRequest, args SecurityStatsArgs) (*mcp.CallToolResult, any, error) {
		result, err := runSecurityStatsSearch(ctx, es, args)
		if err != nil {
			return nil, nil, err
		}
		jsonOutput, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode search_security_stats response: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(jsonOutput)}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_security_stats",
		Description: "Answer one bounded telemetry question over security event data without returning raw documents: top values (aggregation_type=terms), an event-rate timeline (date_histogram), or an approximate unique-value count (cardinality). Requires an explicit RFC3339 start/end window (max 31 days by default) — retention is ~30 days, so short windows (15m/1h/4h/8h/24h) are the common case. Use aggregatable keyword/IP/numeric fields (e.g. source.ip, event.dataset, dns.question.name.keyword — add .keyword for analyzed text). Exact total-hit counts are disabled by default (set include_total=true to enable); cardinality is always approximate. Use search_elastic for multiple, nested, or otherwise raw aggregations.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SecurityStatsArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("search_security_stats", &err)
		normalized, err := normalizeSecurityStatsArgs(args)
		if err != nil {
			return nil, nil, err
		}
		return innerHandler(ctx, req, normalized)
	})
}
