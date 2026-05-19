package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultTimeout = 30 * time.Second
	envTimeoutSecs = "MCP_TIMEOUT_SECS"

	clientName    = "ag0"
	clientVersion = "0.1"

	contentKindText    = "text"
	contentKindJSON    = "json"
	contentKindUnknown = "unknown"
)

// Tool describes an MCP tool advertised by the server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// Content is a single content block returned from a tool call.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolResult is the output of a tool call.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError"`
}

// Client wraps the official MCP SDK's ClientSession so callers can stay
// on the local Tool / ToolResult shapes. Session handshake, JSON-RPC
// framing, Mcp-Session-Id header management and reconnect/retry are all
// handled by the SDK's StreamableClientTransport.
type Client struct {
	URL     string
	session *sdk.ClientSession

	toolsOnce  sync.Once
	toolsCache []Tool
	toolsErr   error
}

// NewClient builds a Streamable HTTP transport, performs the SDK
// initialize handshake against context.Background, and returns a
// ready-to-use client.
func NewClient(url string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("mcp: empty server URL")
	}
	sdkClient := sdk.NewClient(&sdk.Implementation{
		Name:    clientName,
		Version: clientVersion,
	}, nil)
	transport := &sdk.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Timeout: timeoutFromEnv()},
		// We only need request/response semantics. The standalone SSE
		// stream is the SDK's mechanism for server-initiated messages
		// (notifications etc.), but many MCP servers — including the
		// TrainingPeaks one — don't handle GET-for-SSE cleanly and the
		// SDK's reconnect retries eventually close the whole session,
		// killing in-flight tools/call requests. Disabling it keeps the
		// session healthy at the cost of not receiving server-pushed
		// notifications (which we don't use anyway).
		DisableStandaloneSSE: true,
	}
	session, err := sdkClient.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	return &Client{URL: url, session: session}, nil
}

// Close shuts down the underlying MCP session. Safe to call multiple
// times.
func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

func timeoutFromEnv() time.Duration {
	v := os.Getenv(envTimeoutSecs)
	if v == "" {
		return defaultTimeout
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultTimeout
	}
	return time.Duration(n) * time.Second
}

// Call invokes a single MCP tool by name with the given arguments.
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (ToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.session.CallTool(ctx, &sdk.CallToolParams{
		Name:      tool,
		Arguments: args,
	})
	if err != nil {
		return ToolResult{}, err
	}
	return convertResult(res), nil
}

// ListTools returns the tools advertised by the server. The first call
// hits the server; subsequent calls return the cached slice (errors
// are sticky on the first failure).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	c.toolsOnce.Do(func() {
		c.toolsCache, c.toolsErr = c.fetchTools(ctx)
	})
	return c.toolsCache, c.toolsErr
}

// GetToolSchemas returns each tool's input schema keyed by tool name.
// Backed by the same cache as ListTools, so it's a single network
// round-trip across both methods. The returned map is unordered by
// nature; callers that need a deterministic tool array (e.g. for
// prompt-cache stability) must sort the keys themselves.
func (c *Client) GetToolSchemas(ctx context.Context) (map[string]json.RawMessage, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(tools))
	for _, t := range tools {
		if len(t.InputSchema) == 0 {
			continue
		}
		raw, err := json.Marshal(t.InputSchema)
		if err != nil {
			continue
		}
		out[t.Name] = raw
	}
	return out, nil
}

func (c *Client) fetchTools(ctx context.Context) ([]Tool, error) {
	res, err := c.session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			continue
		}
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: convertSchema(t.InputSchema),
		})
	}
	return out, nil
}

// convertSchema normalises whatever the SDK delivered for InputSchema
// (typically map[string]any, but the SDK accepts json.RawMessage and
// other shapes) into the map[string]any our local Tool type exposes.
func convertSchema(s any) map[string]any {
	if s == nil {
		return nil
	}
	if m, ok := s.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func convertResult(res *sdk.CallToolResult) ToolResult {
	if res == nil {
		return ToolResult{}
	}
	out := ToolResult{IsError: res.IsError}
	for _, block := range res.Content {
		text, kind := extractText(block)
		out.Content = append(out.Content, Content{Type: kind, Text: text})
	}
	return out
}

// extractText pulls displayable text + a kind label from an SDK content
// block. Non-text blocks (image/audio/resource refs/etc.) fall back to
// their JSON form so the agent still sees something useful instead of
// silently dropping the block.
func extractText(c sdk.Content) (string, string) {
	if tc, ok := c.(*sdk.TextContent); ok {
		return tc.Text, contentKindText
	}
	if raw, err := json.Marshal(c); err == nil {
		return string(raw), contentKindJSON
	}
	return "", contentKindUnknown
}