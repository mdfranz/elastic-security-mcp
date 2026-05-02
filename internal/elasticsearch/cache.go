package elasticsearch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v9/typedapi/core/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
)

// ToolCache wraps a Redis client for caching MCP tool call results.
type ToolCache struct {
	client  *redis.Client
	enabled bool
}

// NewToolCache creates a ToolCache using REDIS_ADDR and CACHE_ENABLED env vars.
func NewToolCache() *ToolCache {
	enabled := CacheEnabled()
	var client *redis.Client
	if enabled {
		client = redis.NewClient(&redis.Options{
			Addr: RedisAddr(),
		})
	}
	return &ToolCache{
		client:  client,
		enabled: enabled,
	}
}

// CacheEnabled returns true unless CACHE_ENABLED is explicitly set to false, 0, no, or off.
func CacheEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CACHE_ENABLED"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func RedisAddr() string {
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		return v
	}
	return "localhost:6379"
}

func ListIndicesTTL() time.Duration {
	return ttlFromEnv("CACHE_LIST_INDICES_TTL", 3600)
}

func SearchElasticTTL() time.Duration {
	return ttlFromEnv("CACHE_SEARCH_ELASTIC_TTL", 600)
}

func SearchSecurityEventsTTL() time.Duration {
	return ttlFromEnv("CACHE_SEARCH_SECURITY_EVENTS_TTL", 600)
}

func ttlFromEnv(envVar string, defaultSecs int) time.Duration {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(defaultSecs) * time.Second
}

func cacheKey(toolName string, args any) (string, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("cache key marshal: %w", err)
	}
	sum := sha256.Sum256([]byte(toolName + ":" + string(argsJSON)))
	return fmt.Sprintf("%x", sum), nil
}

func (c *ToolCache) Get(ctx context.Context, key string) (string, bool) {
	val, err := c.client.Get(ctx, key).Result()
	if err == nil {
		return val, true
	}
	if !errors.Is(err, redis.Nil) {
		slog.Warn("redis get error, bypassing cache", "error", err)
	}
	return "", false
}

func (c *ToolCache) Set(ctx context.Context, key, text string, ttl time.Duration) {
	if err := c.client.Set(ctx, key, text, ttl).Err(); err != nil {
		slog.Warn("redis set error", "error", err)
	}
}

func (c *ToolCache) IndexSearchResult(ctx context.Context, result map[string]interface{}) {
	if !c.enabled {
		return
	}
	indexSearchResult(ctx, c.client, result)
}

func (c *ToolCache) IndexTypedSearchResult(ctx context.Context, result *search.Response) {
	if !c.enabled {
		return
	}
	indexTypedSearchResult(ctx, c.client, result)
}

func (c *ToolCache) LookupDomain(ctx context.Context, domain string) ([]string, error) {
	if !c.enabled || c.client == nil {
		return nil, nil
	}
	return c.client.ZRevRange(ctx, "dns:name:"+domain, 0, 99).Result()
}

func (c *ToolCache) LookupIP(ctx context.Context, ip string) (dnsAnswers []string, dnsQueries []string, err error) {
	if !c.enabled || c.client == nil {
		return nil, nil, nil
	}
	dnsAnswers, err = c.client.ZRevRange(ctx, "dns:ip:"+ip, 0, 99).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, nil, err
	}
	dnsQueries, err = c.client.ZRevRange(ctx, "ip:seen:"+ip, 0, 99).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, nil, err
	}
	return dnsAnswers, dnsQueries, nil
}

// WrapWithCache wraps a tool handler with cache lookup/store logic.
func WrapWithCache[A any](
	cache *ToolCache,
	toolName string,
	ttl time.Duration,
	inner func(context.Context, *mcp.CallToolRequest, A) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, A) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args A) (*mcp.CallToolResult, any, error) {
		if !cache.enabled {
			return inner(ctx, req, args)
		}

		key, err := cacheKey(toolName, args)
		if err != nil {
			slog.Warn("cache key computation failed, bypassing cache", "tool", toolName, "error", err)
			return inner(ctx, req, args)
		}

		if text, ok := cache.Get(ctx, key); ok {
			slog.Info("cache hit", "tool", toolName, "key_prefix", key[:8])
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: text},
				},
			}, nil, nil
		}

		slog.Info("cache miss", "tool", toolName, "key_prefix", key[:8])
		result, extra, err := inner(ctx, req, args)
		if err != nil {
			return nil, extra, err
		}

		if result != nil && len(result.Content) > 0 {
			if txt, ok := result.Content[0].(*mcp.TextContent); ok {
				cache.Set(ctx, key, txt.Text, ttl)
				slog.Debug("cache stored", "tool", toolName, "key_prefix", key[:8], "ttl_secs", int(ttl.Seconds()))
			}
		}
		return result, extra, nil
	}
}
