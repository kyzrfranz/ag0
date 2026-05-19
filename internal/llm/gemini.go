package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Prompt caching note: Gemini 2.5 Flash (and other 2.5-family models)
// performs implicit caching automatically when a request exceeds the
// minimum cache-eligible prompt size (~1024 tokens at time of writing).
// No request-side opt-in is needed. Cache hits show up in the response
// as usageMetadata.cachedContentTokenCount, which we expose via
// Response.CacheReadTokens and log at debug level below.

const (
	geminiLabel      = "gemini"
	geminiBaseURL    = "https://generativelanguage.googleapis.com/v1beta/models"
	geminiMethodPath = ":generateContent"

	envGeminiKey    = "GOOGLE_API_KEY"
	geminiKeyParam  = "key"
	geminiRoleModel = "model"
	geminiRoleUser  = "user"

	geminiFinishStop      = "STOP"
	geminiFinishMaxTokens = "MAX_TOKENS"
)

// GeminiClient calls the Google Gemini API.
type GeminiClient struct {
	APIKey string
	HTTP   *http.Client
}

// NewGeminiClient constructs a GeminiClient using GOOGLE_API_KEY.
func NewGeminiClient() *GeminiClient {
	return &GeminiClient{
		APIKey: os.Getenv(envGeminiKey),
		HTTP:   http.DefaultClient,
	}
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"function_declarations"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent   `json:"system_instruction,omitempty"`
	Contents          []geminiContent  `json:"contents"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool     `json:"tools,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
	Error         *geminiError         `json:"error,omitempty"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// Complete sends a request to Gemini and returns the response.
func (c *GeminiClient) Complete(ctx context.Context, req Request) (Response, error) {
	gReq := geminiRequest{
		Contents: buildGeminiContents(req.Messages, req.ToolResults),
		Tools:    buildGeminiTools(req.Tools),
	}
	if req.SystemPrompt != "" {
		gReq.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: req.SystemPrompt}},
		}
	}
	if req.MaxTokens > 0 {
		gReq.GenerationConfig = &geminiGenConfig{MaxOutputTokens: req.MaxTokens}
	}

	q := url.Values{}
	q.Set(geminiKeyParam, c.APIKey)
	endpoint := fmt.Sprintf("%s/%s%s?%s", geminiBaseURL, url.PathEscape(req.Model), geminiMethodPath, q.Encode())

	body, err := postJSON(ctx, c.HTTP, geminiLabel, endpoint, nil, gReq)
	if err != nil {
		return Response{}, err
	}

	var out geminiResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Response{}, fmt.Errorf("decode %s response: %w", geminiLabel, err)
	}
	if out.Error != nil {
		return Response{}, fmt.Errorf("%s error: %s: %s", geminiLabel, out.Error.Status, out.Error.Message)
	}
	if len(out.Candidates) == 0 {
		return Response{}, nil
	}

	cand := out.Candidates[0]
	var sb strings.Builder
	var toolCalls []ToolCall
	for _, p := range cand.Content.Parts {
		if p.Text != "" {
			sb.WriteString(p.Text)
		}
		if p.FunctionCall != nil {
			args := p.FunctionCall.Args
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			toolCalls = append(toolCalls, ToolCall{
				Name: p.FunctionCall.Name,
				Args: args,
			})
		}
	}
	var cacheRead int
	if out.UsageMetadata != nil {
		cacheRead = out.UsageMetadata.CachedContentTokenCount
		slog.Debug("gemini usage",
			"prompt_tokens", out.UsageMetadata.PromptTokenCount,
			"candidates_tokens", out.UsageMetadata.CandidatesTokenCount,
			"total_tokens", out.UsageMetadata.TotalTokenCount,
			"cached_tokens", cacheRead,
		)
	}

	return Response{
		Content:         sb.String(),
		ToolCalls:       toolCalls,
		StopReason:      mapGeminiFinishReason(cand.FinishReason),
		CacheReadTokens: cacheRead,
	}, nil
}

func buildGeminiTools(defs []ToolDefinition) []geminiTool {
	if len(defs) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDeclaration, len(defs))
	for i, d := range defs {
		decls[i] = geminiFunctionDeclaration{
			Name:        d.Name,
			Description: d.Description,
			Parameters:  geminiToolParameters(d.Name, d.InputSchema),
		}
	}
	return []geminiTool{{FunctionDeclarations: decls}}
}

// geminiToolParameters unmarshals the MCP-supplied JSON Schema, rewrites
// it through sanitizeSchemaForGemini, and re-marshals for the request.
// On any unmarshal/marshal error the raw schema is passed through so the
// call still has a chance to succeed (Gemini will surface a clearer
// schema error than we could).
func geminiToolParameters(name string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		slog.Warn("gemini schema unmarshal failed — passing raw", "tool", name, "err", err)
		return raw
	}
	sanitized := sanitizeSchemaForGemini(name, schema)
	out, err := json.Marshal(sanitized)
	if err != nil {
		slog.Warn("gemini schema remarshal failed — passing raw", "tool", name, "err", err)
		return raw
	}
	return out
}

// geminiSupportedFormats are the JSON Schema "format" values Gemini's
// function-declaration subset accepts on string-typed properties.
// Others are stripped to avoid schema-validation errors.
var geminiSupportedFormats = map[string]bool{
	"date-time": true,
	"enum":      true,
}

// geminiUnsupportedKeys are JSON Schema keywords Gemini rejects or
// ignores. They're dropped from every node during sanitization.
var geminiUnsupportedKeys = map[string]bool{
	"$schema":              true,
	"$ref":                 true,
	"additionalProperties": true,
}

// sanitizeSchemaForGemini rewrites a JSON-Schema-draft-2020 tool input
// schema (as produced by MCP servers) into the OpenAPI-3.0-flavoured
// subset Gemini's function_declarations accept.
//
// Transformations applied recursively:
//   - type: ["string", "null"]  → type: "string" + nullable: true
//   - drop $schema, $ref, additionalProperties (unsupported by Gemini)
//   - drop unrecognised "format" values (only date-time / enum kept)
//   - default missing "type" on property schemas to "string"
//
// The input map is not mutated; a fresh tree is returned. Each
// transformation is logged at debug level keyed by the tool name and
// the dotted path inside the schema.
func sanitizeSchemaForGemini(toolName string, schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	return sanitizeSchemaNode(toolName, "", schema)
}

func sanitizeSchemaNode(toolName, path string, node map[string]any) map[string]any {
	out := make(map[string]any, len(node))
	for k, v := range node {
		if geminiUnsupportedKeys[k] {
			slog.Debug("sanitized tool schema for gemini", "tool", toolName, "property", path, "removed", k)
			continue
		}
		switch k {
		case "type":
			t, nullable, changed := normalizeSchemaType(v)
			out["type"] = t
			if nullable {
				out["nullable"] = true
			}
			if changed {
				slog.Debug("sanitized tool schema for gemini", "tool", toolName, "property", path, "field", "type")
			}
		case "format":
			if s, ok := v.(string); ok && geminiSupportedFormats[s] {
				out["format"] = v
			} else {
				slog.Debug("sanitized tool schema for gemini", "tool", toolName, "property", path, "removed", "format")
			}
		case "properties":
			out["properties"] = sanitizeProperties(toolName, path, v)
		case "items":
			if itemMap, ok := v.(map[string]any); ok {
				out["items"] = sanitizeSchemaNode(toolName, joinSchemaPath(path, "items"), itemMap)
			} else {
				out["items"] = v
			}
		default:
			out[k] = sanitizeSchemaValue(toolName, joinSchemaPath(path, k), v)
		}
	}
	return out
}

// sanitizeProperties walks each property's sub-schema and ensures every
// property carries a "type" — Gemini rejects untyped property schemas,
// so missing types default to "string".
func sanitizeProperties(toolName, path string, v any) any {
	props, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(props))
	for name, prop := range props {
		propPath := joinSchemaPath(path, name)
		propMap, ok := prop.(map[string]any)
		if !ok {
			out[name] = prop
			continue
		}
		sanitized := sanitizeSchemaNode(toolName, propPath, propMap)
		if _, hasType := sanitized["type"]; !hasType {
			sanitized["type"] = "string"
			slog.Debug("sanitized tool schema for gemini", "tool", toolName, "property", propPath, "added", "type=string")
		}
		out[name] = sanitized
	}
	return out
}

// sanitizeSchemaValue recurses into nested maps and arrays so transforms
// reach inside allOf / anyOf / oneOf / $defs and similar containers.
func sanitizeSchemaValue(toolName, path string, v any) any {
	switch nested := v.(type) {
	case map[string]any:
		return sanitizeSchemaNode(toolName, path, nested)
	case []any:
		out := make([]any, len(nested))
		for i, item := range nested {
			out[i] = sanitizeSchemaValue(toolName, fmt.Sprintf("%s[%d]", path, i), item)
		}
		return out
	default:
		return v
	}
}

// normalizeSchemaType converts a JSON Schema "type" value into Gemini's
// expected form. JSON Schema allows an array of types (commonly
// ["T", "null"] to mark nullable fields); Gemini expects a single
// scalar plus a sibling "nullable" boolean.
//
// Returns the scalar type, whether nullable should be set, and whether
// the value was changed from its input form (for logging).
func normalizeSchemaType(v any) (any, bool, bool) {
	collect := func(items []string) (string, bool) {
		var first string
		nullable := false
		for _, s := range items {
			if s == "null" {
				nullable = true
				continue
			}
			if first == "" {
				first = s
			}
		}
		if first == "" {
			first = "string"
		}
		return first, nullable
	}
	switch t := v.(type) {
	case string:
		return t, false, false
	case []any:
		items := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				items = append(items, s)
			}
		}
		first, nullable := collect(items)
		return first, nullable, true
	case []string:
		first, nullable := collect(t)
		return first, nullable, true
	default:
		return v, false, false
	}
}

// joinSchemaPath builds a dotted breadcrumb path through a schema tree
// (e.g. "user.address.city") for log messages. The "properties" /
// "items" keywords are folded into the path implicitly by the caller
// so the result reads like a user-visible field path.
func joinSchemaPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func buildGeminiContents(in []Message, toolResults []ToolResult) []geminiContent {
	out := make([]geminiContent, 0, len(in)+1)
	for _, m := range in {
		out = append(out, buildGeminiContent(m))
	}
	if len(toolResults) > 0 {
		parts := make([]geminiPart, 0, len(toolResults))
		for _, r := range toolResults {
			parts = append(parts, geminiPart{
				FunctionResponse: &geminiFunctionResponse{
					Name:     r.Name,
					Response: map[string]any{"result": r.Content},
				},
			})
		}
		out = append(out, geminiContent{Role: geminiRoleUser, Parts: parts})
	}
	return out
}

func buildGeminiContent(m Message) geminiContent {
	role := m.Role
	if role == RoleAssistant {
		role = geminiRoleModel
	}
	parts := make([]geminiPart, 0, 1+len(m.ToolCalls)+len(m.ToolResults))
	if m.Content != "" {
		parts = append(parts, geminiPart{Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		args := tc.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		parts = append(parts, geminiPart{
			FunctionCall: &geminiFunctionCall{Name: tc.Name, Args: args},
		})
	}
	for _, r := range m.ToolResults {
		parts = append(parts, geminiPart{
			FunctionResponse: &geminiFunctionResponse{
				Name:     r.Name,
				Response: map[string]any{"result": r.Content},
			},
		})
	}
	return geminiContent{Role: role, Parts: parts}
}

func mapGeminiFinishReason(r string) string {
	switch r {
	case geminiFinishStop:
		return StopReasonStop
	case geminiFinishMaxTokens:
		return StopReasonMaxTokens
	default:
		return r
	}
}