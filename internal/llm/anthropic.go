package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
)

const (
	anthropicLabel   = "anthropic"
	anthropicURL     = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"

	envAnthropicKey = "ANTHROPIC_API_KEY"
	hdrAnthropicKey = "x-api-key"
	hdrAnthropicVer = "anthropic-version"

	anthropicTypeText       = "text"
	anthropicTypeToolUse    = "tool_use"
	anthropicTypeToolResult = "tool_result"

	anthropicStopEndTurn   = "end_turn"
	anthropicStopToolUse   = "tool_use"
	anthropicStopMaxTokens = "max_tokens"

	anthropicCacheEphemeral = "ephemeral"

	mimeEventStream = "text/event-stream"

	sseDataPrefix = "data: "

	anthropicEventContentBlockStart = "content_block_start"
	anthropicEventContentBlockDelta = "content_block_delta"
	anthropicEventMessageDelta      = "message_delta"
	anthropicEventMessageStart      = "message_start"
	anthropicDeltaTextDelta         = "text_delta"
	anthropicDeltaInputJSON         = "input_json_delta"
)

// AnthropicClient calls the Claude API.
type AnthropicClient struct {
	APIKey string
	HTTP   *http.Client
}

// NewAnthropicClient constructs an AnthropicClient using ANTHROPIC_API_KEY.
func NewAnthropicClient() *AnthropicClient {
	return &AnthropicClient{
		APIKey: os.Getenv(envAnthropicKey),
		HTTP:   http.DefaultClient,
	}
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicSystemBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []any of typed blocks
}

type anthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    any                `json:"system,omitempty"` // []anthropicSystemBlock when set
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// anthropicResponseBlock is the loose shape of a content block in the
// response — text and tool_use share the array, distinguished by Type.
type anthropicResponseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicResponse struct {
	Content    []anthropicResponseBlock `json:"content"`
	StopReason string                   `json:"stop_reason"`
	Usage      anthropicUsage           `json:"usage"`
	Error      *anthropicError          `json:"error,omitempty"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Complete sends a request to Claude and returns the response.
func (c *AnthropicClient) Complete(ctx context.Context, req Request) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	headers := map[string]string{
		hdrAnthropicKey: c.APIKey,
		hdrAnthropicVer: anthropicVersion,
	}

	slog.Debug("cache key debug",
		"system_prompt_len", len(req.SystemPrompt),
		"system_hash", hashString(req.SystemPrompt),
		"tools_count", len(req.Tools),
		"first_tool_name", firstToolName(req.Tools),
		"tools_hash", hashTools(req.Tools),
	)

	apiReq := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    buildAnthropicSystem(req.SystemPrompt),
		Messages:  buildAnthropicMessages(req.Messages, req.ToolResults),
		Tools:     buildAnthropicTools(req.Tools),
	}

	hasCacheControl := req.SystemPrompt != "" || len(req.Tools) > 0
	debugBody, _ := json.Marshal(apiReq)
	slog.Debug("anthropic request debug",
		"system_len", len(req.SystemPrompt),
		"tools_count", len(req.Tools),
		"has_cache_control", hasCacheControl,
		"body", string(debugBody),
	)

	body, err := postJSON(ctx, c.HTTP, anthropicLabel, anthropicURL, headers, apiReq)
	if err != nil {
		return Response{}, err
	}

	var out anthropicResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Response{}, fmt.Errorf("decode %s response: %w", anthropicLabel, err)
	}
	if out.Error != nil {
		return Response{}, fmt.Errorf("%s error: %s: %s", anthropicLabel, out.Error.Type, out.Error.Message)
	}

	var sb strings.Builder
	var toolCalls []ToolCall
	for _, block := range out.Content {
		switch block.Type {
		case anthropicTypeText:
			sb.WriteString(block.Text)
		case anthropicTypeToolUse:
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: block.Input,
			})
		}
	}
	if out.Usage.CacheCreationInputTokens > 0 || out.Usage.CacheReadInputTokens > 0 {
		slog.Debug("anthropic cache stats",
			"cache_creation_tokens", out.Usage.CacheCreationInputTokens,
			"cache_read_tokens", out.Usage.CacheReadInputTokens,
		)
	}

	return Response{
		Content:          sb.String(),
		ToolCalls:        toolCalls,
		StopReason:       mapAnthropicStopReason(out.StopReason),
		CacheInputTokens: out.Usage.CacheCreationInputTokens,
		CacheReadTokens:  out.Usage.CacheReadInputTokens,
	}, nil
}

// Stream sends a streaming request to Claude and emits text deltas via
// onChunk as they arrive. The returned Response is fully populated
// (Content equals the concatenation of every emitted chunk).
//
// Currently only text deltas stream; tool_use input deltas are ignored
// because the synthesis path never advertises tools. If that changes,
// add input_json_delta accumulation here.
func (c *AnthropicClient) Stream(ctx context.Context, req Request, onChunk func(string)) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	apiReq := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    buildAnthropicSystem(req.SystemPrompt),
		Messages:  buildAnthropicMessages(req.Messages, req.ToolResults),
		Tools:     buildAnthropicTools(req.Tools),
		Stream:    true,
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return Response{}, fmt.Errorf("marshal anthropic stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("new anthropic stream request: %w", err)
	}
	httpReq.Header.Set(headerContentType, mimeJSON)
	httpReq.Header.Set(headerAccept, mimeEventStream)
	httpReq.Header.Set(hdrAnthropicKey, c.APIKey)
	httpReq.Header.Set(hdrAnthropicVer, anthropicVersion)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("post anthropic stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return Response{}, fmt.Errorf("%s stream http %d: %s", anthropicLabel, resp.StatusCode, string(errBody))
	}

	return parseAnthropicStream(resp.Body, onChunk)
}

// streamingToolBlock accumulates a tool_use content block while the
// model streams it: the id and name arrive in content_block_start, and
// the input JSON is delivered in input_json_delta chunks that we
// concatenate until the block closes.
type streamingToolBlock struct {
	id    string
	name  string
	input strings.Builder
}

// parseAnthropicStream walks an SSE response body, calling onChunk for
// every text delta and accumulating tool_use blocks. Returns the
// assembled Response (text + tool calls + cache / stop / token metadata)
// once the stream ends.
func parseAnthropicStream(body io.Reader, onChunk func(string)) (Response, error) {
	scanner := bufio.NewScanner(body)
	// SSE lines can be large (a single delta may contain a long string);
	// default 64 KiB max isn't enough for some responses.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var content strings.Builder
	var stopReason string
	var usage anthropicUsage
	toolBlocks := map[int]*streamingToolBlock{}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, sseDataPrefix) {
			continue
		}
		data := strings.TrimPrefix(line, sseDataPrefix)
		if data == "" {
			continue
		}

		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			continue
		}

		switch env.Type {
		case anthropicEventContentBlockStart:
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			if ev.ContentBlock.Type == anthropicTypeToolUse {
				toolBlocks[ev.Index] = &streamingToolBlock{
					id:   ev.ContentBlock.ID,
					name: ev.ContentBlock.Name,
				}
			}

		case anthropicEventContentBlockDelta:
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			switch ev.Delta.Type {
			case anthropicDeltaTextDelta:
				if ev.Delta.Text != "" {
					content.WriteString(ev.Delta.Text)
					if onChunk != nil {
						onChunk(ev.Delta.Text)
					}
				}
			case anthropicDeltaInputJSON:
				if b, ok := toolBlocks[ev.Index]; ok {
					b.input.WriteString(ev.Delta.PartialJSON)
				}
			}

		case anthropicEventMessageStart:
			var ev struct {
				Message struct {
					Usage anthropicUsage `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				usage = ev.Message.Usage
			}

		case anthropicEventMessageDelta:
			var ev struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				if ev.Delta.StopReason != "" {
					stopReason = ev.Delta.StopReason
				}
				if ev.Usage.OutputTokens > 0 {
					usage.OutputTokens = ev.Usage.OutputTokens
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("scan %s stream: %w", anthropicLabel, err)
	}

	if usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
		slog.Info("anthropic cache stats",
			"cache_creation_tokens", usage.CacheCreationInputTokens,
			"cache_read_tokens", usage.CacheReadInputTokens,
		)
	}

	var toolCalls []ToolCall
	if len(toolBlocks) > 0 {
		indices := make([]int, 0, len(toolBlocks))
		for i := range toolBlocks {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		toolCalls = make([]ToolCall, 0, len(indices))
		for _, i := range indices {
			b := toolBlocks[i]
			input := b.input.String()
			if input == "" {
				input = "{}"
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   b.id,
				Name: b.name,
				Args: json.RawMessage(input),
			})
		}
	}

	return Response{
		Content:          content.String(),
		ToolCalls:        toolCalls,
		StopReason:       mapAnthropicStopReason(stopReason),
		CacheInputTokens: usage.CacheCreationInputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
	}, nil
}

// hashString returns the first 8 bytes of sha256(s) as hex — short
// enough to log, long enough to spot drift between calls.
func hashString(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// hashTools hashes the tool list in the order given. If the same agent
// produces a different hash across calls, the tools slice is unstable
// (e.g. orchestrator iterating a map without sorting), which guarantees
// cache misses on the tools breakpoint.
func hashTools(tools []ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	h := sha256.New()
	for _, t := range tools {
		h.Write([]byte(t.Name))
		h.Write([]byte{0})
		h.Write([]byte(t.Description))
		h.Write([]byte{0})
		h.Write(t.InputSchema)
		h.Write([]byte{0xff})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

func firstToolName(tools []ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	return tools[0].Name
}

// buildAnthropicSystem wraps the system prompt as a single text block
// with an ephemeral cache breakpoint, so the system prompt is cached
// across calls. Nil when the prompt is empty (so omitempty drops it).
func buildAnthropicSystem(systemPrompt string) any {
	if systemPrompt == "" {
		return nil
	}
	return []anthropicSystemBlock{
		{
			Type:         anthropicTypeText,
			Text:         systemPrompt,
			CacheControl: &anthropicCacheControl{Type: anthropicCacheEphemeral},
		},
	}
}

func buildAnthropicTools(defs []ToolDefinition) []anthropicTool {
	if len(defs) == 0 {
		return nil
	}
	out := make([]anthropicTool, len(defs))
	for i, d := range defs {
		schema := d.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out[i] = anthropicTool{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: schema,
		}
	}
	// Cache breakpoint on the last tool covers system + tools, so any
	// follow-up call with the same system + tool roster pays only for
	// the new user/tool_result turns.
	out[len(out)-1].CacheControl = &anthropicCacheControl{Type: anthropicCacheEphemeral}
	return out
}

func buildAnthropicMessages(in []Message, toolResults []ToolResult) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(in)+1)
	for _, m := range in {
		out = append(out, buildAnthropicMessage(m))
	}
	if len(toolResults) > 0 {
		blocks := make([]any, 0, len(toolResults))
		for _, r := range toolResults {
			blocks = append(blocks, anthropicToolResultBlock{
				Type:      anthropicTypeToolResult,
				ToolUseID: r.ID,
				Content:   r.Content,
			})
		}
		out = append(out, anthropicMessage{Role: RoleUser, Content: blocks})
	}
	return out
}

func buildAnthropicMessage(m Message) anthropicMessage {
	// User turn carrying tool_results: each block references the
	// tool_use_id from the matching assistant turn.
	if len(m.ToolResults) > 0 {
		blocks := make([]any, 0, len(m.ToolResults))
		for _, r := range m.ToolResults {
			blocks = append(blocks, anthropicToolResultBlock{
				Type:      anthropicTypeToolResult,
				ToolUseID: r.ID,
				Content:   r.Content,
			})
		}
		return anthropicMessage{Role: m.Role, Content: blocks}
	}
	// Assistant turn with tool_use blocks (and optional preceding text).
	if len(m.ToolCalls) > 0 {
		blocks := make([]any, 0, len(m.ToolCalls)+1)
		if m.Content != "" {
			blocks = append(blocks, anthropicTextBlock{Type: anthropicTypeText, Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			input := tc.Args
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			blocks = append(blocks, anthropicToolUseBlock{
				Type:  anthropicTypeToolUse,
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		return anthropicMessage{Role: m.Role, Content: blocks}
	}
	// Plain text message.
	return anthropicMessage{Role: m.Role, Content: m.Content}
}

func mapAnthropicStopReason(r string) string {
	switch r {
	case anthropicStopToolUse:
		return StopReasonToolUse
	case anthropicStopEndTurn:
		return StopReasonStop
	case anthropicStopMaxTokens:
		return StopReasonMaxTokens
	default:
		return r
	}
}
