package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	headerContentType = "Content-Type"
	headerAccept      = "Accept"
	mimeJSON          = "application/json"

	defaultMaxTokens = 4096

	StopReasonStop      = "stop"
	StopReasonToolUse   = "tool_use"
	StopReasonMaxTokens = "max_tokens"
)

// Message is a single chat message. An assistant message may carry
// both text Content and a list of ToolCalls (mirroring Anthropic
// content blocks and Gemini parts, both of which allow mixed
// text+tool-call payloads in a single turn). A user message may
// carry ToolResults, which serialise as tool_result blocks
// (Anthropic) or functionResponse parts (Gemini) — required to
// follow every preceding assistant tool_use turn.
type Message struct {
	Role        string
	Content     string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// ToolDefinition is one tool advertised to the LLM. InputSchema is the
// JSON Schema for the tool's arguments, passed through verbatim.
type ToolDefinition struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ToolCall is a tool invocation the model wants the host to execute.
// ID is the provider-issued correlation id (empty for Gemini, which
// matches results back by Name instead).
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolResult is the outcome of executing a tool call, fed back to the
// model in a follow-up Request via Request.ToolResults. ID is used for
// providers that correlate by id (Anthropic); Name is used by Gemini.
type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Request is a model-agnostic completion request.
//
//   - Tools, when non-empty, advertises tools the model may call.
//   - ToolResults, when non-empty, is appended as a synthetic user turn
//     carrying the results of tool calls from the previous response;
//     the assistant turn that produced those calls must already be in
//     Messages with ToolCalls populated.
type Request struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	MaxTokens    int
	Tools        []ToolDefinition
	ToolResults  []ToolResult
}

// Response is a model-agnostic completion response. ToolCalls is set
// when the model wants the host to invoke tools; StopReason is one of
// StopReasonStop / StopReasonToolUse / StopReasonMaxTokens (or the
// provider value verbatim if unmapped).
//
// CacheInputTokens is the number of input tokens the provider wrote
// into its prompt cache on this call (cache misses that populate the
// cache). CacheReadTokens is the number served from the cache (cache
// hits). Either may be zero if the provider does not report it for
// this call or model.
type Response struct {
	Content          string
	ToolCalls        []ToolCall
	StopReason       string
	CacheInputTokens int
	CacheReadTokens  int
}

// Client is the interface every LLM backend implements. Models are
// swappable behind this interface.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Streamer is implemented by LLM clients that can emit response tokens
// incrementally. onChunk is invoked once per text delta as it arrives.
// The returned Response carries the full accumulated content (same as
// Complete would have produced) plus token / stop-reason metadata so
// callers can choose either path interchangeably.
//
// Callers should feature-detect with a type assertion:
//
//	if s, ok := client.(llm.Streamer); ok { ... }
type Streamer interface {
	Stream(ctx context.Context, req Request, onChunk func(string)) (Response, error)
}

// StreamOrComplete dispatches to Streamer.Stream when onChunk is non-nil
// and the client supports streaming, otherwise falls back to Complete.
// In the fallback path, if onChunk is non-nil, it is called once with
// the final Content so callers observing tokens see at least one
// chunk per turn regardless of provider capability.
func StreamOrComplete(ctx context.Context, client Client, req Request, onChunk func(string)) (Response, error) {
	if onChunk != nil {
		if s, ok := client.(Streamer); ok {
			return s.Stream(ctx, req, onChunk)
		}
	}
	resp, err := client.Complete(ctx, req)
	if err == nil && onChunk != nil && resp.Content != "" {
		onChunk(resp.Content)
	}
	return resp, err
}

// postJSON marshals body, POSTs it to url with the given headers, and
// returns the response body. Non-2xx responses become errors that include
// the response body. Content-Type and Accept are set automatically.
func postJSON(ctx context.Context, client *http.Client, label, url string, headers map[string]string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", label, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("new %s request: %w", label, err)
	}
	req.Header.Set(headerContentType, mimeJSON)
	req.Header.Set(headerAccept, mimeJSON)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", label, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s http %d: %s", label, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}