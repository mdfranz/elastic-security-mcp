package elasticsearch

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/mfranz/elastic-security-mcp/internal/util"
	"github.com/redis/go-redis/v9"
)

const entityTTL = 24 * time.Hour
const maxEntityHistory = 500

type dnsRecord struct {
	Ts      string   `json:"ts"`
	Src     string   `json:"src"`
	Answers []string `json:"answers"`
}

type ipRecord struct {
	Ts     string `json:"ts"`
	Domain string `json:"domain"`
	Src    string `json:"src,omitempty"`
	Type   string `json:"type"`
}

func indexSearchResult(ctx context.Context, client *redis.Client, result map[string]interface{}) {
	hits, ok := result["hits"].(map[string]interface{})
	if !ok {
		return
	}
	hitList, ok := hits["hits"].([]interface{})
	if !ok {
		return
	}

	pipe := client.Pipeline()
	indexed := 0
	for _, h := range hitList {
		hit, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		source, ok := hit["_source"].(map[string]interface{})
		if !ok {
			continue
		}
		if indexZeekDNSHit(ctx, pipe, source) {
			indexed++
		}
	}

	if indexed > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			slog.Warn("redis batch index error", "error", err)
		} else {
			slog.Info("indexed search hits", "count", indexed)
		}
	}
}

func indexTypedSearchResult(ctx context.Context, client *redis.Client, result *search.Response) {
	if result == nil {
		return
	}

	pipe := client.Pipeline()
	indexed := 0
	for _, hit := range result.Hits.Hits {
		source := make(map[string]interface{})
		if len(hit.Source_) == 0 {
			continue
		}
		if err := json.Unmarshal(hit.Source_, &source); err != nil {
			slog.Warn("failed to decode typed search hit", "index", hit.Index_, "id", valueOrEmpty(hit.Id_), "error", err)
			continue
		}
		if indexZeekDNSHit(ctx, pipe, source) {
			indexed++
		}
	}

	if indexed > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			slog.Warn("redis batch index error", "error", err)
		} else {
			slog.Info("indexed typed search hits", "count", indexed)
		}
	}
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func indexZeekDNSHit(ctx context.Context, pipe redis.Pipeliner, source map[string]interface{}) bool {
	ds, _ := source["data_stream"].(map[string]interface{})
	if ds == nil {
		return false
	}
	if dataset, _ := ds["dataset"].(string); dataset != "zeek.dns" {
		return false
	}

	tsStr, _ := source["@timestamp"].(string)
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		ts = time.Now()
	}
	score := float64(ts.UnixMilli())

	dns, _ := source["dns"].(map[string]interface{})
	if dns == nil {
		return false
	}
	question, _ := dns["question"].(map[string]interface{})
	domain, _ := question["name"].(string)
	domain = util.NormalizeDomain(domain)
	if domain == "" {
		return false
	}

	srcIP := ""
	if src, ok := source["source"].(map[string]interface{}); ok {
		srcIP, _ = src["ip"].(string)
	}

	var resolvedIPs []string
	if rips, ok := dns["resolved_ip"].([]interface{}); ok {
		for _, ip := range rips {
			if s, ok := ip.(string); ok {
				resolvedIPs = append(resolvedIPs, s)
			}
		}
	}

	// domain → query records
	recJSON, _ := json.Marshal(dnsRecord{Ts: tsStr, Src: srcIP, Answers: resolvedIPs})
	dnsKey := "dns:name:" + domain
	pipe.ZAdd(ctx, dnsKey, redis.Z{Score: score, Member: string(recJSON)})
	pipe.ZRemRangeByRank(ctx, dnsKey, 0, int64(-maxEntityHistory-1))
	pipe.Expire(ctx, dnsKey, entityTTL)

	// resolved IP → domains it answered for
	for _, ip := range resolvedIPs {
		ipJSON, _ := json.Marshal(ipRecord{Ts: tsStr, Domain: domain, Src: srcIP, Type: "dns_answer"})
		ipKey := "dns:ip:" + ip
		pipe.ZAdd(ctx, ipKey, redis.Z{Score: score, Member: string(ipJSON)})
		pipe.ZRemRangeByRank(ctx, ipKey, 0, int64(-maxEntityHistory-1))
		pipe.Expire(ctx, ipKey, entityTTL)
	}

	// source IP → DNS queries it made
	if srcIP != "" {
		srcJSON, _ := json.Marshal(ipRecord{Ts: tsStr, Domain: domain, Type: "dns_query"})
		srcKey := "ip:seen:" + srcIP
		pipe.ZAdd(ctx, srcKey, redis.Z{Score: score, Member: string(srcJSON)})
		pipe.ZRemRangeByRank(ctx, srcKey, 0, int64(-maxEntityHistory-1))
		pipe.Expire(ctx, srcKey, entityTTL)
	}

	return true
}
