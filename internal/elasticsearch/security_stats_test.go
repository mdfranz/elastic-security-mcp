package elasticsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v9/typedapi/types"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/totalhitsrelation"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- normalization ---

func TestNormalizeSecurityStatsArgsDefaultsAndCaps(t *testing.T) {
	args, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "  logs-zeek.*-*  ",
		AggregationType: "  TERMS ",
		Field:           "  source.ip  ",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Index != "logs-zeek.*-*" {
		t.Fatalf("unexpected index: %q", args.Index)
	}
	if args.AggregationType != "terms" {
		t.Fatalf("expected lowercased aggregation_type, got %q", args.AggregationType)
	}
	if args.Field != "source.ip" {
		t.Fatalf("unexpected field: %q", args.Field)
	}
	if args.Size != defaultStatsSize {
		t.Fatalf("expected default size %d, got %d", defaultStatsSize, args.Size)
	}

	args.Size = 9999
	args, err = normalizeSecurityStatsArgs(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Size != maxStatsSize {
		t.Fatalf("expected size capped to %d, got %d", maxStatsSize, args.Size)
	}
}

func TestNormalizeSecurityStatsArgsDateHistogramDefaults(t *testing.T) {
	args, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "date_histogram",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T04:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Field != "@timestamp" {
		t.Fatalf("expected default field @timestamp, got %q", args.Field)
	}
	if args.Interval != defaultStatsInterval {
		t.Fatalf("expected default interval %q, got %q", defaultStatsInterval, args.Interval)
	}
}

func TestNormalizeSecurityStatsArgsRequiresIndex(t *testing.T) {
	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		AggregationType: "cardinality",
		Field:           "dns.question.name.keyword",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for blank index")
	}
}

func TestNormalizeSecurityStatsArgsRejectsUnsupportedAggregationType(t *testing.T) {
	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "avg",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for unsupported aggregation_type")
	}
}

func TestNormalizeSecurityStatsArgsRequiresFieldForTermsAndCardinality(t *testing.T) {
	for _, aggType := range []string{"terms", "cardinality"} {
		_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
			Index:           "logs-*",
			AggregationType: aggType,
			Start:           "2026-01-01T00:00:00Z",
			End:             "2026-01-01T01:00:00Z",
		})
		if err == nil {
			t.Fatalf("expected error for missing field with aggregation_type=%s", aggType)
		}
	}
}

func TestNormalizeSecurityStatsArgsRejectsInvalidTimestamps(t *testing.T) {
	base := SecurityStatsArgs{Index: "logs-*", AggregationType: "cardinality", Field: "source.ip"}

	bad := base
	bad.Start = "not-a-time"
	bad.End = "2026-01-01T01:00:00Z"
	if _, err := normalizeSecurityStatsArgs(bad); err == nil {
		t.Fatal("expected error for invalid start")
	}

	bad = base
	bad.Start = "2026-01-01T00:00:00Z"
	bad.End = "not-a-time"
	if _, err := normalizeSecurityStatsArgs(bad); err == nil {
		t.Fatal("expected error for invalid end")
	}
}

func TestNormalizeSecurityStatsArgsRejectsReversedRange(t *testing.T) {
	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "cardinality",
		Field:           "source.ip",
		Start:           "2026-01-01T02:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for end before start")
	}
}

func TestNormalizeSecurityStatsArgsRejectsOversizedRange(t *testing.T) {
	orig := statsMaxRangeHours
	statsMaxRangeHours = 24
	defer func() { statsMaxRangeHours = orig }()

	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "cardinality",
		Field:           "source.ip",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-05T00:00:00Z",
	})
	if err == nil {
		t.Fatal("expected error for range exceeding STATS_MAX_RANGE_HOURS")
	}
}

func TestNormalizeSecurityStatsArgsRejectsInvalidInterval(t *testing.T) {
	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "date_histogram",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T04:00:00Z",
		Interval:        "3M", // calendar intervals only support single multiples
	})
	if err == nil {
		t.Fatal("expected error for invalid interval")
	}
}

func TestNormalizeSecurityStatsArgsRejectsInvalidPrecisionThreshold(t *testing.T) {
	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:              "logs-*",
		AggregationType:    "cardinality",
		Field:              "dns.question.name.keyword",
		Start:              "2026-01-01T00:00:00Z",
		End:                "2026-01-01T01:00:00Z",
		PrecisionThreshold: 50000,
	})
	if err == nil {
		t.Fatal("expected error for precision_threshold out of range")
	}
}

func TestNormalizeSecurityStatsArgsRejectsExcessiveBucketCount(t *testing.T) {
	orig := statsMaxBuckets
	statsMaxBuckets = 250
	defer func() { statsMaxBuckets = orig }()

	// 7 days at a 5-minute fixed interval is ~2016 buckets, well over the default cap —
	// a plausible mistake at the short-window scale this tool is designed for.
	_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "date_histogram",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-08T00:00:00Z",
		Interval:        "5m",
	})
	if err == nil {
		t.Fatal("expected error for excessive bucket count")
	}
}

func TestNormalizeSecurityStatsArgsAcceptsPrimaryTargetWindows(t *testing.T) {
	cases := []struct {
		name     string
		start    string
		end      string
		interval string
	}{
		{"15m window, 1m buckets", "2026-01-01T00:00:00Z", "2026-01-01T00:15:00Z", "1m"},
		{"1h window, default interval", "2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z", ""},
		{"4h window, 5m buckets", "2026-01-01T00:00:00Z", "2026-01-01T04:00:00Z", "5m"},
		{"8h window, 15m buckets", "2026-01-01T00:00:00Z", "2026-01-01T08:00:00Z", "15m"},
		{"24h window, 1h buckets", "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", "1h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeSecurityStatsArgs(SecurityStatsArgs{
				Index:           "logs-*",
				AggregationType: "date_histogram",
				Start:           tc.start,
				End:             tc.end,
				Interval:        tc.interval,
			})
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

// --- calendar/fixed interval classification ---

func TestIsCalendarIntervalPreservesCase(t *testing.T) {
	if !isCalendarInterval("1M") {
		t.Fatal("expected 1M to be a calendar interval (month)")
	}
	if !isCalendarInterval("1m") {
		t.Fatal("expected 1m to be a calendar interval (minute)")
	}
	if isCalendarInterval("1MO") {
		t.Fatal("did not expect 1MO to be recognized")
	}
}

func TestIsValidFixedIntervalAcceptsAndRejects(t *testing.T) {
	for _, ok := range []string{"15m", "6h", "30s", "500ms", "1d"} {
		if !isValidFixedInterval(ok) {
			t.Errorf("expected %q to be a valid fixed interval", ok)
		}
	}
	for _, bad := range []string{"1w", "1M", "0m", "abc", ""} {
		if isValidFixedInterval(bad) {
			t.Errorf("did not expect %q to be a valid fixed interval", bad)
		}
	}
}

func TestEstimateBucketCountCalendarMonthHandlesVariableLength(t *testing.T) {
	// Jan (31d) + Feb (28d in 2026, non-leap) — exactly 2 calendar-month buckets,
	// verifying month-length variability is handled exactly rather than via a
	// fixed day-count approximation.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got := estimateBucketCount(start, end, "1M", true, 1000)
	if got != 2 {
		t.Fatalf("expected 2 calendar-month buckets, got %d", got)
	}
}

func TestEstimateBucketCountFixedIntervalExact(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	got := estimateBucketCount(start, end, "15m", false, 1000)
	if got != 17 { // 4h / 15m = 16, +1
		t.Fatalf("expected 17 buckets, got %d", got)
	}
}

// --- request building ---

func TestBuildSecurityStatsRequestTerms(t *testing.T) {
	req := buildSecurityStatsRequest(SecurityStatsArgs{
		Index:           "logs-zeek.*-*",
		AggregationType: "terms",
		Field:           "source.ip",
		Size:            25,
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
		Query:           "event.dataset:zeek.dns",
	})

	if req.Size == nil || *req.Size != 0 {
		t.Fatalf("expected size 0 (aggregation-only), got %#v", req.Size)
	}
	if req.TrackTotalHits != false {
		t.Fatalf("expected track_total_hits to default false, got %v", req.TrackTotalHits)
	}

	if req.Query == nil || req.Query.Bool == nil || len(req.Query.Bool.Filter) != 2 {
		t.Fatalf("expected timestamp + query_string filters, got %#v", req.Query)
	}
	foundQueryString := false
	for _, f := range req.Query.Bool.Filter {
		if f.QueryString != nil && f.QueryString.Query == "event.dataset:zeek.dns" {
			foundQueryString = true
		}
	}
	if !foundQueryString {
		t.Fatal("expected query_string filter for optional Query field")
	}

	agg, ok := req.Aggregations["stats"]
	if !ok {
		t.Fatal("expected a \"stats\" aggregation")
	}
	if agg.Terms == nil || agg.Terms.Field == nil || *agg.Terms.Field != "source.ip" {
		t.Fatalf("unexpected terms aggregation: %#v", agg.Terms)
	}
	if agg.Terms.Size == nil || *agg.Terms.Size != 25 {
		t.Fatalf("unexpected terms size: %#v", agg.Terms.Size)
	}
}

func TestBuildSecurityStatsRequestDateHistogramCalendar(t *testing.T) {
	req := buildSecurityStatsRequest(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "date_histogram",
		Field:           "@timestamp",
		Interval:        "1M",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-02-01T00:00:00Z",
	})

	agg := req.Aggregations["stats"]
	if agg.DateHistogram == nil {
		t.Fatal("expected a date_histogram aggregation")
	}
	if agg.DateHistogram.CalendarInterval == nil || agg.DateHistogram.CalendarInterval.Name != "1M" {
		t.Fatalf("expected calendar_interval \"1M\" preserved verbatim, got %#v", agg.DateHistogram.CalendarInterval)
	}
	if agg.DateHistogram.FixedInterval != nil {
		t.Fatalf("did not expect fixed_interval set, got %#v", agg.DateHistogram.FixedInterval)
	}
}

func TestBuildSecurityStatsRequestDateHistogramFixed(t *testing.T) {
	req := buildSecurityStatsRequest(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "date_histogram",
		Field:           "@timestamp",
		Interval:        "15m",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T04:00:00Z",
	})

	agg := req.Aggregations["stats"]
	if agg.DateHistogram == nil {
		t.Fatal("expected a date_histogram aggregation")
	}
	if agg.DateHistogram.CalendarInterval != nil {
		t.Fatalf("did not expect calendar_interval set, got %#v", agg.DateHistogram.CalendarInterval)
	}
	if agg.DateHistogram.FixedInterval != "15m" {
		t.Fatalf("expected fixed_interval \"15m\", got %#v", agg.DateHistogram.FixedInterval)
	}
}

func TestBuildSecurityStatsRequestCardinality(t *testing.T) {
	req := buildSecurityStatsRequest(SecurityStatsArgs{
		Index:              "logs-*",
		AggregationType:    "cardinality",
		Field:              "dns.question.name.keyword",
		PrecisionThreshold: 3000,
		Start:              "2026-01-01T00:00:00Z",
		End:                "2026-01-01T01:00:00Z",
	})

	agg := req.Aggregations["stats"]
	if agg.Cardinality == nil || agg.Cardinality.Field == nil || *agg.Cardinality.Field != "dns.question.name.keyword" {
		t.Fatalf("unexpected cardinality aggregation: %#v", agg.Cardinality)
	}
	if agg.Cardinality.PrecisionThreshold == nil || *agg.Cardinality.PrecisionThreshold != 3000 {
		t.Fatalf("expected precision_threshold 3000, got %#v", agg.Cardinality.PrecisionThreshold)
	}
}

func TestBuildSecurityStatsRequestIncludeTotalOptIn(t *testing.T) {
	req := buildSecurityStatsRequest(SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "cardinality",
		Field:           "source.ip",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
		IncludeTotal:    true,
	})
	if req.TrackTotalHits != true {
		t.Fatalf("expected track_total_hits true when include_total is set, got %v", req.TrackTotalHits)
	}
}

// --- response shaping ---

func TestShapeTermsAggregateStringVariant(t *testing.T) {
	agg := &types.StringTermsAggregate{
		Buckets:          []types.StringTermsBucket{{Key: "10.0.0.5", DocCount: 42}},
		SumOtherDocCount: int64Ptr(11),
	}
	stats, err := shapeTermsAggregate(agg, SecurityStatsArgs{Field: "source.ip"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buckets := stats["buckets"].([]map[string]interface{})
	if len(buckets) != 1 || buckets[0]["key"] != "10.0.0.5" || buckets[0]["doc_count"] != int64(42) {
		t.Fatalf("unexpected buckets: %#v", buckets)
	}
	if stats["sum_other_doc_count"] != int64(11) {
		t.Fatalf("unexpected sum_other_doc_count: %#v", stats["sum_other_doc_count"])
	}
}

func TestShapeTermsAggregateLongVariant(t *testing.T) {
	agg := &types.LongTermsAggregate{
		Buckets: []types.LongTermsBucket{{Key: 443, DocCount: 7}},
	}
	stats, err := shapeTermsAggregate(agg, SecurityStatsArgs{Field: "destination.port"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buckets := stats["buckets"].([]map[string]interface{})
	if buckets[0]["key"] != int64(443) {
		t.Fatalf("unexpected key: %#v", buckets[0]["key"])
	}
}

func TestShapeTermsAggregateDoubleVariant(t *testing.T) {
	agg := &types.DoubleTermsAggregate{
		Buckets: []types.DoubleTermsBucket{{Key: types.Float64(1.5), DocCount: 3}},
	}
	stats, err := shapeTermsAggregate(agg, SecurityStatsArgs{Field: "some.float"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buckets := stats["buckets"].([]map[string]interface{})
	if buckets[0]["key"] != types.Float64(1.5) {
		t.Fatalf("unexpected key: %#v", buckets[0]["key"])
	}
}

func TestShapeTermsAggregateUnmappedVariant(t *testing.T) {
	agg := &types.UnmappedTermsAggregate{}
	stats, err := shapeTermsAggregate(agg, SecurityStatsArgs{Field: "not.mapped"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buckets := stats["buckets"].([]map[string]interface{})
	if len(buckets) != 0 {
		t.Fatalf("expected empty buckets for unmapped field, got %#v", buckets)
	}
	// An unmapped field must be distinguishable from a mapped field that
	// simply has zero matches — both would otherwise render as identical
	// empty bucket lists with no way to tell them apart.
	if stats["unmapped"] != true {
		t.Fatalf("expected unmapped:true, got %#v", stats["unmapped"])
	}
	if _, ok := stats["note"].(string); !ok {
		t.Fatalf("expected an explanatory note, got %#v", stats["note"])
	}
}

func TestShapeTermsAggregateMappedEmptyResultIsNotFlaggedUnmapped(t *testing.T) {
	agg := &types.StringTermsAggregate{Buckets: []types.StringTermsBucket{}}
	stats, err := shapeTermsAggregate(agg, SecurityStatsArgs{Field: "source.ip"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := stats["unmapped"]; ok {
		t.Fatalf("did not expect unmapped to be set for a mapped field with zero matches, got %#v", stats["unmapped"])
	}
}

func TestShapeDateHistogramAggregate(t *testing.T) {
	keyAsString := "2026-01-01T00:00:00.000Z"
	agg := &types.DateHistogramAggregate{
		Buckets: []types.DateHistogramBucket{{Key: 1767225600000, KeyAsString: &keyAsString, DocCount: 10}},
	}
	stats, err := shapeDateHistogramAggregate(agg, SecurityStatsArgs{Field: "@timestamp", Interval: "1h"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buckets := stats["buckets"].([]map[string]interface{})
	if len(buckets) != 1 || buckets[0]["key_as_string"] != keyAsString || buckets[0]["doc_count"] != int64(10) {
		t.Fatalf("unexpected buckets: %#v", buckets)
	}
	if stats["interval"] != "1h" {
		t.Fatalf("unexpected interval: %#v", stats["interval"])
	}
}

func TestShapeCardinalityAggregate(t *testing.T) {
	agg := &types.CardinalityAggregate{Value: 123}
	stats, err := shapeCardinalityAggregate(agg, SecurityStatsArgs{Field: "dns.question.name.keyword", PrecisionThreshold: 4000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats["value"] != int64(123) {
		t.Fatalf("unexpected value: %#v", stats["value"])
	}
	if stats["approximate"] != true {
		t.Fatal("expected approximate:true")
	}
	if stats["precision_threshold"] != 4000 {
		t.Fatalf("unexpected precision_threshold: %#v", stats["precision_threshold"])
	}
}

func TestFormatStatsTotalEventsNilTotalIsUnknownNotZero(t *testing.T) {
	out := formatStatsTotalEvents(nil)
	if out["exact"] != false || out["relation"] != "unknown" || out["value"] != nil {
		t.Fatalf("expected unknown/false/nil for a nil total (include_total not requested), got %#v", out)
	}
}

func TestFormatStatsTotalEventsExactTotal(t *testing.T) {
	out := formatStatsTotalEvents(&types.TotalHits{Value: 5, Relation: totalhitsrelation.Eq})
	if out["exact"] != true || out["value"] != int64(5) {
		t.Fatalf("unexpected result for exact total: %#v", out)
	}
}

// --- truncation ---

func TestTruncateSecurityStatsBucketsTruncatesWhenOversized(t *testing.T) {
	origMax := maxResponseChars
	// A large bucket count and a max well below the untruncated size, so the
	// ~150-byte fixed overhead of the envelope + truncated/note metadata this
	// function adds is a small fraction of the budget rather than dominating
	// it (a tighter ratio made an earlier version of this test flaky/failing
	// even though the underlying truncation logic was correct).
	maxResponseChars = 3000
	defer func() { maxResponseChars = origMax }()

	buckets := make([]map[string]interface{}, 0, 200)
	for i := 0; i < 200; i++ {
		buckets = append(buckets, map[string]interface{}{"key": "host-with-a-fairly-long-name-000", "doc_count": i})
	}
	out := map[string]interface{}{
		"stats": map[string]interface{}{
			"field":   "host.name",
			"buckets": buckets,
		},
	}

	if err := truncateSecurityStatsBuckets(out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["truncated"] != true {
		t.Fatal("expected truncated:true")
	}
	stats := out["stats"].(map[string]interface{})
	got := stats["buckets"].([]map[string]interface{})
	if len(got) == 0 || len(got) >= 200 {
		t.Fatalf("expected a reduced but non-empty bucket list, got %d", len(got))
	}
	if data, err := json.Marshal(out); err != nil || len(data) > maxResponseChars {
		t.Fatalf("expected final output to fit within maxResponseChars, got %d bytes (err=%v)", len(data), err)
	}
}

func TestTruncateSecurityStatsBucketsErrorsWhenCannotFit(t *testing.T) {
	origMax := maxResponseChars
	maxResponseChars = 10 // even a single bucket can't fit
	defer func() { maxResponseChars = origMax }()

	buckets := []map[string]interface{}{
		{"key": "a", "doc_count": 1},
		{"key": "b", "doc_count": 2},
	}
	out := map[string]interface{}{
		"stats": map[string]interface{}{
			"field":   "host.name",
			"buckets": buckets,
		},
	}

	if err := truncateSecurityStatsBuckets(out); err == nil {
		t.Fatal("expected an error when even a single-bucket payload cannot fit")
	}
}

// --- typed-client transport tests ---

func TestRunSecurityStatsSearchTermsAgainstTestServer(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("typed_keys") != "true" {
			t.Fatalf("expected typed_keys=true, got %q", r.URL.RawQuery)
		}
		var seenBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if seenBody["size"] != float64(0) {
			t.Fatalf("expected size:0, got %#v", seenBody["size"])
		}

		body := mustJSON(t, map[string]any{
			"took":      5,
			"timed_out": false,
			"_shards":   map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits":      map[string]any{"hits": []any{}},
			"aggregations": map[string]any{
				"sterms#stats": map[string]any{
					"doc_count_error_upper_bound": 0,
					"sum_other_doc_count":         3,
					"buckets": []any{
						map[string]any{"key": "10.0.0.5", "doc_count": 42},
					},
				},
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})

	client, err := newTestClient(transport)
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	out, err := runSecurityStatsSearch(context.Background(), client, SecurityStatsArgs{
		Index:           "logs-zeek.*-*",
		AggregationType: "terms",
		Field:           "source.ip",
		Size:            10,
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err != nil {
		t.Fatalf("runSecurityStatsSearch error: %v", err)
	}

	total := out["total_events"].(map[string]interface{})
	if total["relation"] != "unknown" {
		t.Fatalf("expected unknown total relation (track_total_hits defaulted false), got %#v", total)
	}

	stats := out["stats"].(map[string]interface{})
	buckets := stats["buckets"].([]map[string]interface{})
	if len(buckets) != 1 || buckets[0]["key"] != "10.0.0.5" {
		t.Fatalf("unexpected buckets: %#v", buckets)
	}
}

func TestRunSecurityStatsSearchDateHistogramAgainstTestServer(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := mustJSON(t, map[string]any{
			"took":      5,
			"timed_out": false,
			"_shards":   map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits":      map[string]any{"hits": []any{}},
			"aggregations": map[string]any{
				"date_histogram#stats": map[string]any{
					"buckets": []any{
						map[string]any{"key": 1767225600000, "key_as_string": "2026-01-01T00:00:00.000Z", "doc_count": 100},
					},
				},
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})

	client, err := newTestClient(transport)
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	out, err := runSecurityStatsSearch(context.Background(), client, SecurityStatsArgs{
		Index:           "logs-suricata.*-*",
		AggregationType: "date_histogram",
		Field:           "@timestamp",
		Interval:        "1h",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T04:00:00Z",
	})
	if err != nil {
		t.Fatalf("runSecurityStatsSearch error: %v", err)
	}

	stats := out["stats"].(map[string]interface{})
	buckets := stats["buckets"].([]map[string]interface{})
	if len(buckets) != 1 || buckets[0]["doc_count"] != int64(100) {
		t.Fatalf("unexpected buckets: %#v", buckets)
	}
}

func TestRunSecurityStatsSearchCardinalityAgainstTestServer(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := mustJSON(t, map[string]any{
			"took":      5,
			"timed_out": false,
			"_shards":   map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits":      map[string]any{"hits": []any{}},
			"aggregations": map[string]any{
				"cardinality#stats": map[string]any{"value": 321},
			},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})

	client, err := newTestClient(transport)
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	out, err := runSecurityStatsSearch(context.Background(), client, SecurityStatsArgs{
		Index:           "logs-zeek.*-*",
		AggregationType: "cardinality",
		Field:           "dns.question.name.keyword",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err != nil {
		t.Fatalf("runSecurityStatsSearch error: %v", err)
	}
	stats := out["stats"].(map[string]interface{})
	if stats["value"] != int64(321) || stats["approximate"] != true {
		t.Fatalf("unexpected cardinality stats: %#v", stats)
	}
}

func TestRunSecurityStatsSearchRejectsTimedOut(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := mustJSON(t, map[string]any{
			"took":         5,
			"timed_out":    true,
			"_shards":      map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits":         map[string]any{"hits": []any{}},
			"aggregations": map[string]any{},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})

	client, err := newTestClient(transport)
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	_, err = runSecurityStatsSearch(context.Background(), client, SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "cardinality",
		Field:           "source.ip",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err == nil {
		t.Fatal("expected an error for a timed-out search")
	}
}

func TestRunSecurityStatsSearchRejectsFailedShards(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := mustJSON(t, map[string]any{
			"took":         5,
			"timed_out":    false,
			"_shards":      map[string]any{"total": 2, "successful": 1, "skipped": 0, "failed": 1},
			"hits":         map[string]any{"hits": []any{}},
			"aggregations": map[string]any{},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"application/json"},
				"X-Elastic-Product": []string{"Elasticsearch"},
			},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})

	client, err := newTestClient(transport)
	if err != nil {
		t.Fatalf("newTestClient error: %v", err)
	}

	_, err = runSecurityStatsSearch(context.Background(), client, SecurityStatsArgs{
		Index:           "logs-*",
		AggregationType: "cardinality",
		Field:           "source.ip",
		Start:           "2026-01-01T00:00:00Z",
		End:             "2026-01-01T01:00:00Z",
	})
	if err == nil {
		t.Fatal("expected an error when shards failed")
	}
}

// --- registration/schema ---

func TestRegisterSecurityStatsToolExposesRequiredArgs(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	RegisterSecurityStatsTool(server, &Client{}, &ToolCache{})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErrCh := make(chan error, 1)
	go func() {
		_, err := server.Connect(ctx, serverTransport, nil)
		serverErrCh <- err
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect error: %v", err)
	}
	defer cs.Close()

	result, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}

	var tool *mcp.Tool
	for _, tl := range result.Tools {
		if tl.Name == "search_security_stats" {
			tool = tl
		}
	}
	if tool == nil {
		t.Fatal("expected search_security_stats to be registered")
	}
	// From the client side, InputSchema round-trips as the default JSON
	// marshaling of the server's schema — a map[string]any, not a
	// *jsonschema.Schema (see the doc comment on mcp.Tool.InputSchema).
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("expected InputSchema to decode as a map, got %T", tool.InputSchema)
	}
	requiredRaw, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("expected a \"required\" array in the schema, got %#v", schema["required"])
	}
	var required []string
	for _, r := range requiredRaw {
		if s, ok := r.(string); ok {
			required = append(required, s)
		}
	}
	for _, field := range []string{"index", "aggregation_type", "start", "end"} {
		if !slices.Contains(required, field) {
			t.Errorf("expected %q to be a required field, got %#v", field, required)
		}
	}
}

// --- helpers ---

func int64Ptr(v int64) *int64 { return &v }
