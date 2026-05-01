package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/llms"
	"google.golang.org/api/googleapi"
)

type geminiModel struct {
	apiKey       string
	defaultModel string
	httpClient   *http.Client

	mu                sync.Mutex
	thoughtSignatures map[string]json.RawMessage
}

type geminiGenerateContentRequest struct {
	Model             string                 `json:"model"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	Contents          []geminiContent        `json:"contents,omitempty"`
	Tools             []geminiTool           `json:"tools,omitempty"`
	SafetySettings    []geminiSafetySetting  `json:"safetySettings,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature json.RawMessage         `json:"thoughtSignature,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Parameters  *geminiSchema `json:"parameters,omitempty"`
}

type geminiSchema struct {
	Type        int                      `json:"type,omitempty"`
	Format      string                   `json:"format,omitempty"`
	Description string                   `json:"description,omitempty"`
	Nullable    bool                     `json:"nullable,omitempty"`
	Enum        []string                 `json:"enum,omitempty"`
	Items       *geminiSchema            `json:"items,omitempty"`
	Properties  map[string]*geminiSchema `json:"properties,omitempty"`
	Required    []string                 `json:"required,omitempty"`
}

type geminiSafetySetting struct {
	Category  int `json:"category"`
	Threshold int `json:"threshold"`
}

type geminiGenerationConfig struct {
	CandidateCount  int     `json:"candidateCount,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
	TopP            float64 `json:"topP,omitempty"`
	TopK            int     `json:"topK,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates    []geminiCandidate   `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
	ModelVersion  string              `json:"modelVersion"`
	ResponseID    string              `json:"responseId"`
}

type geminiCandidate struct {
	Content       geminiContent `json:"content"`
	FinishReason  any           `json:"finishReason"`
	SafetyRatings any           `json:"safetyRatings"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type geminiErrorEnvelope struct {
	Error struct {
		Code    int           `json:"code"`
		Message string        `json:"message"`
		Status  string        `json:"status"`
		Details []interface{} `json:"details"`
	} `json:"error"`
}

func newGeminiModel(apiKey, defaultModel string, httpClient *http.Client) llms.Model {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &geminiModel{
		apiKey:            apiKey,
		defaultModel:      defaultModel,
		httpClient:        httpClient,
		thoughtSignatures: make(map[string]json.RawMessage),
	}
}

func (g *geminiModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return llms.GenerateFromSinglePrompt(ctx, g, prompt, options...)
}

func (g *geminiModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	opts := llms.CallOptions{
		Model:          g.defaultModel,
		CandidateCount: 1,
		MaxTokens:      2048,
		Temperature:    0.5,
		TopP:           0.95,
		TopK:           3,
	}
	for _, opt := range options {
		opt(&opts)
	}
	if opts.Model == "" {
		opts.Model = g.defaultModel
	}

	reqBody, err := g.buildRequest(messages, opts)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", opts.Model, g.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	slog.Debug("Raw Gemini HTTP response",
		"model", opts.Model,
		"status_code", resp.StatusCode,
		"has_thought_signature", bytes.Contains(respBody, []byte("thoughtSignature")),
	)
	if payloadLoggingEnabled() {
		slog.Debug("Raw Gemini HTTP response body",
			"model", opts.Model,
			"body", truncateForLog(string(respBody), maxLoggedPayloadChars),
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope geminiErrorEnvelope
		_ = json.Unmarshal(respBody, &envelope)
		return nil, &googleapi.Error{
			Code:    resp.StatusCode,
			Message: envelope.Error.Message,
			Details: envelope.Error.Details,
			Body:    string(respBody),
			Header:  resp.Header.Clone(),
		}
	}

	var parsed geminiGenerateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Candidates) == 0 {
		slog.Warn("Gemini returned no candidates", "model", opts.Model, "response_id", parsed.ResponseID)
	}

	return g.convertResponse(parsed), nil
}

func (g *geminiModel) buildRequest(messages []llms.MessageContent, opts llms.CallOptions) (*geminiGenerateContentRequest, error) {
	req := &geminiGenerateContentRequest{
		Model: fmt.Sprintf("models/%s", opts.Model),
		SafetySettings: []geminiSafetySetting{
			{Category: 10, Threshold: 3},
			{Category: 7, Threshold: 3},
			{Category: 8, Threshold: 3},
			{Category: 9, Threshold: 3},
		},
		GenerationConfig: geminiGenerationConfig{
			CandidateCount:  opts.CandidateCount,
			MaxOutputTokens: opts.MaxTokens,
			Temperature:     opts.Temperature,
			TopP:            opts.TopP,
			TopK:            opts.TopK,
		},
	}

	tools, err := convertGeminiTools(opts.Tools)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	for _, msg := range messages {
		content, err := g.convertMessage(msg)
		if err != nil {
			return nil, err
		}
		switch msg.Role {
		case llms.ChatMessageTypeSystem:
			if req.SystemInstruction == nil {
				req.SystemInstruction = content
			} else {
				req.SystemInstruction.Parts = append(req.SystemInstruction.Parts, content.Parts...)
			}
		default:
			req.Contents = append(req.Contents, *content)
		}
	}

	return req, nil
}

func (g *geminiModel) convertMessage(msg llms.MessageContent) (*geminiContent, error) {
	parts := make([]geminiPart, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case llms.TextContent:
			parts = append(parts, geminiPart{Text: p.Text})
		case llms.ToolCall:
			args := json.RawMessage("{}")
			if p.FunctionCall != nil && strings.TrimSpace(p.FunctionCall.Arguments) != "" {
				args = json.RawMessage(p.FunctionCall.Arguments)
			}
			fc := &geminiFunctionCall{
				Name: p.FunctionCall.Name,
				Args: args,
			}
			part := geminiPart{FunctionCall: fc}
			if sig := g.lookupThoughtSignature(p.ID); len(sig) > 0 {
				part.ThoughtSignature = sig
				slog.Debug("Reattaching Gemini thought signature",
					"tool_call_id", p.ID,
					"name", p.FunctionCall.Name,
					"signature_len", len(sig),
				)
			} else {
				slog.Debug("No Gemini thought signature found for tool call",
					"tool_call_id", p.ID,
					"name", p.FunctionCall.Name,
				)
			}
			parts = append(parts, part)
		case llms.ToolCallResponse:
			parts = append(parts, geminiPart{
				FunctionResponse: &geminiFunctionResponse{
					Name: p.Name,
					Response: map[string]any{
						"response": p.Content,
					},
				},
			})
		default:
			return nil, fmt.Errorf("unsupported Gemini message part %T", part)
		}
	}

	return &geminiContent{
		Role:  geminiRoleForMessage(msg.Role),
		Parts: parts,
	}, nil
}

func (g *geminiModel) convertResponse(resp geminiGenerateContentResponse) *llms.ContentResponse {
	out := &llms.ContentResponse{}
	for _, candidate := range resp.Candidates {
		var sb strings.Builder
		toolCalls := make([]llms.ToolCall, 0)

		for _, part := range candidate.Content.Parts {
			switch {
			case part.Text != "":
				sb.WriteString(part.Text)
			case part.FunctionCall != nil:
				toolID := randomToolCallID()
				g.storeThoughtSignature(toolID, part.ThoughtSignature)
				slog.Debug("Captured Gemini function call",
					"tool_call_id", toolID,
					"name", part.FunctionCall.Name,
					"has_thought_signature", len(part.ThoughtSignature) > 0,
					"thought_signature_len", len(part.ThoughtSignature),
				)
				toolCalls = append(toolCalls, llms.ToolCall{
					ID:   toolID,
					Type: "tool_call",
					FunctionCall: &llms.FunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: compactJSON(part.FunctionCall.Args),
					},
				})
			}
		}

		out.Choices = append(out.Choices, &llms.ContentChoice{
			Content:    sb.String(),
			StopReason: fmt.Sprint(candidate.FinishReason),
			ToolCalls:  toolCalls,
			GenerationInfo: map[string]any{
				"input_tokens":     resp.UsageMetadata.PromptTokenCount,
				"output_tokens":    resp.UsageMetadata.CandidatesTokenCount,
				"total_tokens":     resp.UsageMetadata.TotalTokenCount,
				"PromptTokens":     resp.UsageMetadata.PromptTokenCount,
				"CompletionTokens": resp.UsageMetadata.CandidatesTokenCount,
				"TotalTokens":      resp.UsageMetadata.TotalTokenCount,
				"safety":           candidate.SafetyRatings,
				"modelVersion":     resp.ModelVersion,
				"responseId":       resp.ResponseID,
				"ThinkingContent":  "",
				"ThinkingTokens":   0,
			},
		})
	}
	return out
}

func (g *geminiModel) lookupThoughtSignature(toolCallID string) json.RawMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.thoughtSignatures[toolCallID]
}

func (g *geminiModel) storeThoughtSignature(toolCallID string, sig json.RawMessage) {
	if toolCallID == "" || len(sig) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.thoughtSignatures[toolCallID] = append(json.RawMessage(nil), sig...)
}

func randomToolCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func geminiRoleForMessage(role llms.ChatMessageType) string {
	switch role {
	case llms.ChatMessageTypeAI:
		return "model"
	case llms.ChatMessageTypeSystem:
		return "system"
	default:
		return "user"
	}
}

func convertGeminiTools(tools []llms.Tool) ([]geminiTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	declarations := make([]geminiFunctionDeclaration, 0, len(tools))
	for i, tool := range tools {
		if tool.Type != "function" {
			return nil, fmt.Errorf("tool [%d]: unsupported type %q, want 'function'", i, tool.Type)
		}
		params, ok := tool.Function.Parameters.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool [%d]: unsupported type %T of Parameters", i, tool.Function.Parameters)
		}
		schema, err := convertGeminiSchemaRecursive(params, i, "")
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, geminiFunctionDeclaration{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  schema,
		})
	}

	return []geminiTool{{FunctionDeclarations: declarations}}, nil
}

func convertGeminiSchemaRecursive(schemaMap map[string]any, toolIndex int, propertyPath string) (*geminiSchema, error) {
	schema := &geminiSchema{}

	if ty, ok := schemaMap["type"]; ok {
		tyString, ok := ty.(string)
		if !ok {
			return nil, fmt.Errorf("tool [%d], property [%s]: expected string for type", toolIndex, propertyPath)
		}
		schema.Type = convertGeminiToolSchemaType(tyString)
	}

	if desc, ok := schemaMap["description"]; ok {
		descString, ok := desc.(string)
		if !ok {
			return nil, fmt.Errorf("tool [%d], property [%s]: expected string for description", toolIndex, propertyPath)
		}
		schema.Description = descString
	}

	if properties, ok := schemaMap["properties"]; ok {
		propMap, ok := properties.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool [%d], property [%s]: expected map for properties", toolIndex, propertyPath)
		}

		schema.Properties = make(map[string]*geminiSchema)
		for propName, propValue := range propMap {
			valueMap, ok := propValue.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("tool [%d], property [%s.%s]: expect to find a value map", toolIndex, propertyPath, propName)
			}

			nestedPath := propName
			if propertyPath != "" {
				nestedPath = propertyPath + "." + propName
			}

			nestedSchema, err := convertGeminiSchemaRecursive(valueMap, toolIndex, nestedPath)
			if err != nil {
				return nil, err
			}
			schema.Properties[propName] = nestedSchema
		}
	} else if schema.Type == geminiTypeObject && propertyPath == "" {
		return nil, fmt.Errorf("tool [%d]: expected to find a map of properties", toolIndex)
	}

	if items, ok := schemaMap["items"]; ok && schema.Type == geminiTypeArray {
		itemMap, ok := items.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool [%d], property [%s]: expect to find a map for array items", toolIndex, propertyPath)
		}

		itemsPath := propertyPath + "[]"
		itemsSchema, err := convertGeminiSchemaRecursive(itemMap, toolIndex, itemsPath)
		if err != nil {
			return nil, err
		}
		schema.Items = itemsSchema
	}

	if required, ok := schemaMap["required"]; ok {
		if rs, ok := required.([]string); ok {
			schema.Required = rs
		} else if ri, ok := required.([]interface{}); ok {
			rs := make([]string, 0, len(ri))
			for _, r := range ri {
				rString, ok := r.(string)
				if !ok {
					return nil, fmt.Errorf("tool [%d], property [%s]: expected string for required", toolIndex, propertyPath)
				}
				rs = append(rs, rString)
			}
			schema.Required = rs
		} else {
			return nil, fmt.Errorf("tool [%d], property [%s]: expected array for required", toolIndex, propertyPath)
		}
	}

	return schema, nil
}

const (
	geminiTypeString  = 1
	geminiTypeNumber  = 2
	geminiTypeInteger = 3
	geminiTypeBoolean = 4
	geminiTypeArray   = 5
	geminiTypeObject  = 6
)

func convertGeminiToolSchemaType(ty string) int {
	switch ty {
	case "object":
		return geminiTypeObject
	case "string":
		return geminiTypeString
	case "number":
		return geminiTypeNumber
	case "integer":
		return geminiTypeInteger
	case "boolean":
		return geminiTypeBoolean
	case "array":
		return geminiTypeArray
	default:
		return 0
	}
}
