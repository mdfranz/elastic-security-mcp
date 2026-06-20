package kibana

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool Arguments
type KibanaAPIRequestArgs struct {
	Method string `json:"method" jsonschema:"HTTP method (GET, POST, PUT, DELETE, PATCH). Defaults to GET."`
	Path   string `json:"path" jsonschema:"The Kibana API path, starting with / (e.g. /api/saved_objects/_find)"`
	Body   any    `json:"body,omitempty" jsonschema:"Optional request body object or JSON string"`
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, args KibanaAPIRequestArgs) (res *mcp.CallToolResult, extra any, err error) {
		method := strings.ToUpper(strings.TrimSpace(args.Method))
		if method == "" {
			method = "GET"
		}
		path := strings.TrimSpace(args.Path)
		if path == "" {
			return nil, nil, fmt.Errorf("path is required")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}

		slog.Info("kibana_api_request called", "method", method, "path", path)
		respBody, statusCode, err := client.DoRequest(ctx, method, path, args.Body)
		if err != nil {
			slog.Error("kibana_api_request error", "error", err)
			return nil, nil, fmt.Errorf("kibana_api_request error: %w", err)
		}

		return formatResponse(respBody, statusCode)
	})

	// 2. Register list_kibana_spaces
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_kibana_spaces",
		Description: "List all available Kibana spaces.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListKibanaSpacesArgs) (res *mcp.CallToolResult, extra any, err error) {
		slog.Info("list_kibana_spaces called")
		respBody, statusCode, err := client.DoRequest(ctx, "GET", "/api/spaces/space", nil)
		if err != nil {
			slog.Error("list_kibana_spaces error", "error", err)
			return nil, nil, fmt.Errorf("list_kibana_spaces error: %w", err)
		}

		return formatResponse(respBody, statusCode)
	})

	// 3. Register list_detection_rules
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_detection_rules",
		Description: "Retrieve a list of detection engine rules from the Elastic Security app, including their enabled status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListDetectionRulesArgs) (res *mcp.CallToolResult, extra any, err error) {
		slog.Info("list_detection_rules called", "page", args.Page, "per_page", args.PerPage)

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

		slog.Info("list_detection_rules making request", "path", path)
		respBody, statusCode, err := client.DoRequest(ctx, "GET", path, nil)
		slog.Info("list_detection_rules response", "statusCode", statusCode, "errorPresent", err != nil)
		if err != nil {
			slog.Error("list_detection_rules error", "error", err)
			return nil, nil, fmt.Errorf("list_detection_rules error: %w", err)
		}

		return formatResponse(respBody, statusCode)
	})

	// 4. Register get_detection_rule
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_detection_rule",
		Description: "Get details of a specific Elastic Security detection engine rule by its ID (internal saved object ID) or rule_id (user-defined unique ID). Provide at least one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetDetectionRuleArgs) (res *mcp.CallToolResult, extra any, err error) {
		slog.Info("get_detection_rule called", "id", args.Id, "rule_id", args.RuleId)
		
		if args.Id == "" && args.RuleId == "" {
			return nil, nil, fmt.Errorf("either id or rule_id must be provided")
		}

		path := "/api/detection_engine/rules"
		if args.Id != "" {
			path += "?id=" + args.Id
		} else {
			path += "?rule_id=" + args.RuleId
		}

		respBody, statusCode, err := client.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			slog.Error("get_detection_rule error", "error", err)
			return nil, nil, fmt.Errorf("get_detection_rule error: %w", err)
		}

		return formatResponse(respBody, statusCode)
	})

	// 5. Register list_agents
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_agents",
		Description: "Retrieve Elastic Agents from Fleet using the Kibana Fleet API.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListAgentsArgs) (res *mcp.CallToolResult, extra any, err error) {
		slog.Info("list_agents called", "page", args.Page, "perPage", args.PerPage, "kuery", args.Kuery)
		
		path := "/api/fleet/agents"
		var params []string
		if args.Page > 0 {
			params = append(params, fmt.Sprintf("page=%d", args.Page))
		}
		if args.PerPage > 0 {
			params = append(params, fmt.Sprintf("perPage=%d", args.PerPage))
		}
		if args.Kuery != "" {
			params = append(params, fmt.Sprintf("kuery=%s", args.Kuery))
		}
		if len(params) > 0 {
			path += "?" + strings.Join(params, "&")
		}

		respBody, statusCode, err := client.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			slog.Error("list_agents error", "error", err)
			return nil, nil, fmt.Errorf("list_agents error: %w", err)
		}

		return formatResponse(respBody, statusCode)
	})
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

	resultText := string(respBody)
	// Prepend status code info if it is an HTTP error status code
	if statusCode >= 400 {
		resultText = fmt.Sprintf("HTTP Error %d:\n%s", statusCode, resultText)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: resultText},
		},
	}, nil, nil
}
