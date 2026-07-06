package kibana

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool Arguments
type KibanaAPIRequestArgs struct {
	Method string `json:"method" jsonschema:"HTTP method (GET, POST, PUT, DELETE, PATCH). Defaults to GET."`
	Path   string `json:"path" jsonschema:"The Kibana API path, starting with / (e.g. /api/saved_objects/_find)"`
	Body   any    `json:"body,omitempty" jsonschema:"Optional request body, encoded as a JSON string"`
}

// jsonStringSchema is used for the Body argument (the handler also tolerates a raw
// JSON object via json.Marshal, but the declared schema must commit to "string": Go's
// `any` otherwise infers to an unconstrained schema with no "type" key, which OpenAI's
// structured tool-calling rejects ("schema must have a 'type' key"); declaring "object"
// instead runs into a second OpenAI strict-mode requirement that object schemas set
// "additionalProperties": false, which would forbid arbitrary request body shapes.
var jsonStringSchema = &jsonschema.Schema{Type: "string"}

// kibanaAPIRequestArgsSchema is the explicit input schema for KibanaAPIRequestArgs,
// overriding the inferred schema for the Body field (see jsonStringSchema).
var kibanaAPIRequestArgsSchema = mustSchemaFor[KibanaAPIRequestArgs]()

func mustSchemaFor[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[any](): jsonStringSchema,
		},
	})
	if err != nil {
		panic(err)
	}
	return s
}

type ListKibanaSpacesArgs struct{}

type ListDetectionRulesArgs struct {
	Page    int `json:"page,omitempty" jsonschema:"Optional page number of results to retrieve"`
	PerPage int `json:"per_page,omitempty" jsonschema:"Optional number of rules per page (default: 20, max: 100)"`
}

type GetDetectionRuleArgs struct {
	Id     string `json:"id,omitempty" jsonschema:"Optional internal Kibana saved object ID of the rule"`
	RuleId string `json:"rule_id,omitempty" jsonschema:"Optional user-specified rule ID (UUID)"`
}

type ListAgentsArgs struct {
	Page    int    `json:"page,omitempty" jsonschema:"Optional page number"`
	PerPage int    `json:"perPage,omitempty" jsonschema:"Optional number of agents per page"`
	Kuery   string `json:"kuery,omitempty" jsonschema:"Optional Kibana Query Language (KQL) filter string (e.g., local_metadata.host.name: \"my-host\")"`
}

func RegisterTools(server *mcp.Server, client *Client) {
	// 1. Register kibana_api_request
	mcp.AddTool(server, &mcp.Tool{
		Name:        "kibana_api_request",
		Description: "Execute an arbitrary HTTP request against the Kibana REST API. Useful for endpoints not covered by other tools, such as saved objects, spaces, alerting connectors, or custom Kibana configurations.",
		InputSchema: kibanaAPIRequestArgsSchema,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args KibanaAPIRequestArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("kibana_api_request", &err)

		method, path, err := normalizeKibanaAPIRequest(args)
		if err != nil {
			return nil, nil, err
		}

		if args.Body != nil {
			if bodyJSON, err := json.Marshal(args.Body); err == nil {
				slog.Debug("kibana_api_request body", "method", method, "path", path, "body", string(bodyJSON))
			}
		}
		slog.Info("kibana_api_request called", "method", method, "path", path)
		start := time.Now()
		respBody, statusCode, err := client.DoRequest(ctx, method, path, args.Body)
		if err != nil {
			slog.Error("kibana_api_request error", "method", method, "path", path, "latency_ms", time.Since(start).Milliseconds(), "error", err)
			return nil, nil, fmt.Errorf("kibana_api_request error: %w", err)
		}
		slog.Info("kibana_api_request response", "method", method, "path", path, "status_code", statusCode, "latency_ms", time.Since(start).Milliseconds(), "response_bytes", len(respBody))

		return formatResponse(respBody, statusCode)
	})

	// 2. Register list_kibana_spaces
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_kibana_spaces",
		Description: "List all available Kibana spaces.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListKibanaSpacesArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("list_kibana_spaces", &err)

		slog.Info("list_kibana_spaces called")
		start := time.Now()
		respBody, statusCode, err := client.DoRequest(ctx, "GET", "/api/spaces/space", nil)
		if err != nil {
			slog.Error("list_kibana_spaces error", "latency_ms", time.Since(start).Milliseconds(), "error", err)
			return nil, nil, fmt.Errorf("list_kibana_spaces error: %w", err)
		}
		slog.Info("list_kibana_spaces response", "status_code", statusCode, "latency_ms", time.Since(start).Milliseconds(), "response_bytes", len(respBody))

		return formatResponse(respBody, statusCode)
	})

	// 3. Register list_detection_rules
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_detection_rules",
		Description: "Retrieve a list of detection engine rules from the Elastic Security app, including their enabled status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListDetectionRulesArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("list_detection_rules", &err)

		path := buildListDetectionRulesPath(args)

		slog.Info("list_detection_rules called", "page", args.Page, "per_page", args.PerPage, "path", path)
		start := time.Now()
		respBody, statusCode, err := client.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			slog.Error("list_detection_rules error", "path", path, "latency_ms", time.Since(start).Milliseconds(), "error", err)
			return nil, nil, fmt.Errorf("list_detection_rules error: %w", err)
		}
		slog.Info("list_detection_rules response", "path", path, "status_code", statusCode, "latency_ms", time.Since(start).Milliseconds(), "response_bytes", len(respBody))

		return formatResponse(respBody, statusCode)
	})

	// 4. Register get_detection_rule
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_detection_rule",
		Description: "Get details of a specific Elastic Security detection engine rule by its ID (internal saved object ID) or rule_id (user-defined unique ID). Provide at least one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetDetectionRuleArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("get_detection_rule", &err)

		path, err := buildGetDetectionRulePath(args)
		if err != nil {
			return nil, nil, err
		}

		slog.Info("get_detection_rule called", "id", args.Id, "rule_id", args.RuleId, "path", path)
		start := time.Now()
		respBody, statusCode, err := client.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			slog.Error("get_detection_rule error", "path", path, "latency_ms", time.Since(start).Milliseconds(), "error", err)
			return nil, nil, fmt.Errorf("get_detection_rule error: %w", err)
		}
		slog.Info("get_detection_rule response", "path", path, "status_code", statusCode, "latency_ms", time.Since(start).Milliseconds(), "response_bytes", len(respBody))

		return formatResponse(respBody, statusCode)
	})

	// 5. Register list_agents
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_agents",
		Description: "Retrieve Elastic Agents from Fleet using the Kibana Fleet API.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListAgentsArgs) (res *mcp.CallToolResult, extra any, err error) {
		defer recoverToolPanic("list_agents", &err)

		path := buildListAgentsPath(args)

		slog.Info("list_agents called", "page", args.Page, "perPage", args.PerPage, "kuery", args.Kuery, "path", path)
		start := time.Now()
		respBody, statusCode, err := client.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			slog.Error("list_agents error", "path", path, "latency_ms", time.Since(start).Milliseconds(), "error", err)
			return nil, nil, fmt.Errorf("list_agents error: %w", err)
		}
		slog.Info("list_agents response", "path", path, "status_code", statusCode, "latency_ms", time.Since(start).Milliseconds(), "response_bytes", len(respBody))

		return formatResponse(respBody, statusCode)
	})
}

func normalizeKibanaAPIRequest(args KibanaAPIRequestArgs) (method string, path string, err error) {
	method = strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = "GET"
	}

	path = strings.TrimSpace(args.Path)
	if path == "" {
		return "", "", fmt.Errorf("path is required")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return method, path, nil
}

func buildListDetectionRulesPath(args ListDetectionRulesArgs) string {
	path := "/api/detection_engine/rules/_find"
	var params []string
	if args.Page > 0 {
		params = append(params, fmt.Sprintf("page=%d", args.Page))
	}
	if args.PerPage > 0 {
		params = append(params, fmt.Sprintf("per_page=%d", args.PerPage))
	}
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}
	return path
}

func buildGetDetectionRulePath(args GetDetectionRuleArgs) (string, error) {
	if args.Id == "" && args.RuleId == "" {
		return "", fmt.Errorf("either id or rule_id must be provided")
	}

	path := "/api/detection_engine/rules"
	if args.Id != "" {
		path += "?id=" + url.QueryEscape(args.Id)
	} else {
		path += "?rule_id=" + url.QueryEscape(args.RuleId)
	}
	return path, nil
}

func buildListAgentsPath(args ListAgentsArgs) string {
	path := "/api/fleet/agents"
	var params []string
	if args.Page > 0 {
		params = append(params, fmt.Sprintf("page=%d", args.Page))
	}
	if args.PerPage > 0 {
		params = append(params, fmt.Sprintf("perPage=%d", args.PerPage))
	}
	if args.Kuery != "" {
		params = append(params, "kuery="+url.QueryEscape(args.Kuery))
	}
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}
	return path
}

// recoverToolPanic logs and converts panics inside tool handlers into errors
func recoverToolPanic(toolName string, err *error) {
	if r := recover(); r != nil {
		slog.Error("panic in kibana tool handler", "tool", toolName, "panic", r)
		*err = fmt.Errorf("internal error: panic in kibana tool %s: %v", toolName, r)
	}
}

func formatResponse(respBody []byte, statusCode int) (*mcp.CallToolResult, any, error) {
	// If the response is valid JSON, format it nicely
	var raw interface{}
	if err := json.Unmarshal(respBody, &raw); err == nil {
		formatted, err := json.MarshalIndent(raw, "", "  ")
		if err == nil {
			respBody = formatted
		}
	}

	// On HTTP error status codes, return a structured error instead of
	// embedding the HTTP error text into a successful tool result. This
	// keeps Kibana tools consistent with Elasticsearch tool semantics.
	if statusCode >= 400 {
		// Return a trimmed preview in the error message to aid debugging but
		// avoid returning large blobs as part of the error.
		preview := string(respBody)
		if len(preview) > 4096 {
			preview = preview[:4096] + "..."
		}
		return nil, nil, fmt.Errorf("HTTP %d: %s", statusCode, preview)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(respBody)},
		},
	}, nil, nil
}
