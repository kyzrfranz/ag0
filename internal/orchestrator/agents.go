package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kyzrfranz/ag0/internal/llm"
	"github.com/kyzrfranz/ag0/internal/mcp"
	"github.com/kyzrfranz/ag0/internal/memory"
)

const (
	sectionProfile      = "Profile:"
	sectionUserQuestion = "USER QUESTION: "
	toolErrorTag        = "[error]"

	geminiModelPrefix = "gemini"

	// DefaultMaxToolResultChars is the fallback byte cap applied to each
	// MCP tool's content when Agent.MaxToolResultChars is zero.
	DefaultMaxToolResultChars = 8000

	truncationSuffixFmt = " [truncated: %d chars removed]"
	truncationPrefixFmt = "[truncated: %d chars removed from start]\n"

	// maxToolRounds caps the number of round-trips an agent will make
	// when the model keeps requesting more tool calls after each batch
	// of tool results. Without it a misbehaving model could loop
	// forever. 10 is generous for legitimate multi-step workflows.
	maxToolRounds = 10

	// emptyResponseRetryPrompt is appended as a final user message
	// when the model exits a tool-call sequence with no content. Some
	// providers occasionally settle on stop="stop" with empty text
	// after a tool round; a single nudge usually unblocks them.
	emptyResponseRetryPrompt = "Please provide a complete response based on the tool results above."

	// maxRoundsFallbackText is returned when the model is still asking
	// for more tools after maxToolRounds and never produced final text.
	maxRoundsFallbackText = "Tool calls completed but no final response generated."
)

// Agent is a stateless specialist: a description, system prompt, model
// id, and the MCP tools it is allowed to call. MaxToolResultChars is
// set by the Coordinator when wiring the roster; zero falls back to
// DefaultMaxToolResultChars.
//
// TruncateFromEnd controls which side of an oversized tool result is
// dropped: the default (false) keeps the END of the content — best for
// time-series MCP responses where the newest data sits last — and
// setting it true preserves the START instead.
type Agent struct {
	Name               string
	Description        string
	SystemPrompt       string
	Model              string
	MCPTools           []string
	MaxToolResultChars int
	TruncateFromEnd    bool
}

// AgentResponse is the output of a single sub-agent invocation.
type AgentResponse struct {
	AgentName string
	Content   string
	Error     error
}

// Invoke runs the agent against userMessage. When an MCP client is
// available, the agent uses the provider's native tool-calling protocol
// (Anthropic tool_use / Gemini functionCall); otherwise it just asks
// the model directly.
//
// onToken, when non-nil, receives the agent's final reply as it streams
// (text deltas in real time when the LLM client supports streaming,
// otherwise one chunk with the whole reply at the end). It is only
// safe to pass a non-nil onToken when this agent is the sole consumer
// of the callback — i.e. the coordinator's single-agent fast-path.
func (a Agent) Invoke(
	ctx context.Context,
	agentCtx memory.AgentContext,
	userMessage string,
	mcpClient *mcp.Client,
	llmClient llm.Client,
	onToken func(string),
	onActivity func(ActivityEvent),
) AgentResponse {
	if mcpClient != nil {
		return a.invokeWithNativeTools(ctx, agentCtx, userMessage, mcpClient, llmClient, onToken, onActivity)
	}
	return a.invokeWithoutTools(ctx, agentCtx, userMessage, llmClient, onToken)
}

func (a Agent) invokeWithoutTools(ctx context.Context, agentCtx memory.AgentContext, userMessage string, llmClient llm.Client, onToken func(string)) AgentResponse {
	resp := AgentResponse{AgentName: a.Name}
	routed := clientFor(a.Model, llmClient)

	messages := a.buildInitialMessages(agentCtx, userMessage)
	out, err := llm.StreamOrComplete(ctx, routed, llm.Request{
		Model:        a.Model,
		SystemPrompt: a.SystemPrompt,
		Messages:     messages,
	}, onToken)
	if err != nil {
		resp.Error = fmt.Errorf("agent %s: %w", a.Name, err)
		return resp
	}
	resp.Content = out.Content
	return resp
}

// invokeWithNativeTools runs the native tool-calling loop:
//  1. Advertise tools (whitelisted from MCP server schemas).
//  2. Stream a Complete — model may emit ToolCalls (with stop_reason
//     "tool_use") or settle on a final answer (any other stop_reason
//     or zero ToolCalls).
//  3. If tools were requested: execute them, append the assistant
//     tool_use turn + the ToolResults, and loop back to step 2.
//  4. Otherwise the response is the final answer.
//
// Each loop iteration streams text deltas via onToken as they arrive,
// so the user sees both the model's pre-tool narration and the final
// reply live. maxToolRounds caps the loop to defend against runaway
// model behaviour.
func (a Agent) invokeWithNativeTools(ctx context.Context, agentCtx memory.AgentContext, userMessage string, mcpClient *mcp.Client, llmClient llm.Client, onToken func(string), onActivity func(ActivityEvent)) AgentResponse {
	resp := AgentResponse{AgentName: a.Name}
	routed := clientFor(a.Model, llmClient)

	tools, err := a.buildToolDefs(ctx, mcpClient)
	if err != nil {
		resp.Error = fmt.Errorf("agent %s: %w", a.Name, err)
		return resp
	}

	messages := a.buildInitialMessages(agentCtx, userMessage)
	var out llm.Response

	for round := 1; round <= maxToolRounds; round++ {
		out, err = llm.StreamOrComplete(ctx, routed, llm.Request{
			Model:        a.Model,
			SystemPrompt: a.SystemPrompt,
			Messages:     messages,
			Tools:        tools,
		}, onToken)
		if err != nil {
			resp.Error = fmt.Errorf("agent %s: %w", a.Name, err)
			return resp
		}

		slog.Info("llm call result",
			"agent", a.Name,
			"round", round,
			"content_len", len(out.Content),
			"tool_calls", len(out.ToolCalls),
			"stop_reason", out.StopReason,
		)

		// Continue ONLY when the model both signalled tool_use AND
		// emitted at least one tool call. Either condition alone is
		// a terminal turn — defends against a model that flags
		// tool_use with no actual calls (would loop forever).
		if out.StopReason != llm.StopReasonToolUse || len(out.ToolCalls) == 0 {
			break
		}

		toolNames := make([]string, len(out.ToolCalls))
		for i, tc := range out.ToolCalls {
			toolNames[i] = tc.Name
		}
		slog.Info("tool call round", "agent", a.Name, "round", round, "tools", toolNames)

		toolResults := a.executeToolCalls(ctx, mcpClient, out.ToolCalls, onActivity)

		// Append the round as a strict pair: assistant turn with the
		// tool_use blocks immediately followed by a user turn with the
		// matching tool_result blocks. Anthropic requires every
		// tool_use to be answered by a tool_result with the same id
		// in the very next message before more model turns can follow.
		messages = append(messages,
			llm.Message{
				Role:      llm.RoleAssistant,
				Content:   out.Content,
				ToolCalls: out.ToolCalls,
			},
			llm.Message{
				Role:        llm.RoleUser,
				ToolResults: toolResults,
			},
		)
	}

	// Loop fell out with the model still requesting tools — we hit
	// maxToolRounds without ever getting a terminal text turn. Plant
	// a fallback so the caller sees *something* instead of silence.
	if out.StopReason == llm.StopReasonToolUse && len(out.ToolCalls) > 0 {
		slog.Error("max tool rounds without text response",
			"agent", a.Name,
			"max", maxToolRounds,
		)
		if out.Content == "" {
			out.Content = maxRoundsFallbackText
		}
	}

	// Known failure mode: model stops cleanly (stop_reason != tool_use)
	// but emits no content after a tool round. One nudge with a fresh
	// user turn usually gets the summary out.
	if out.Content == "" && len(out.ToolCalls) == 0 && out.StopReason != llm.StopReasonToolUse {
		slog.Warn("empty response, retrying", "agent", a.Name)
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: emptyResponseRetryPrompt,
		})
		retryOut, retryErr := llm.StreamOrComplete(ctx, routed, llm.Request{
			Model:        a.Model,
			SystemPrompt: a.SystemPrompt,
			Messages:     messages,
			Tools:        tools,
		}, onToken)
		if retryErr != nil {
			resp.Error = fmt.Errorf("agent %s retry: %w", a.Name, retryErr)
			return resp
		}
		slog.Info("llm call result",
			"agent", a.Name,
			"phase", "retry",
			"content_len", len(retryOut.Content),
			"tool_calls", len(retryOut.ToolCalls),
			"stop_reason", retryOut.StopReason,
		)
		out = retryOut
	}

	resp.Content = out.Content
	return resp
}

// buildToolDefs assembles the ToolDefinition slice sent to the LLM:
// descriptions come from ListTools, raw schemas from GetToolSchemas
// (both backed by the same cache on the MCP client). When the agent
// declares MCPTools, only those names are advertised; otherwise the
// full advertised set is used.
func (a Agent) buildToolDefs(ctx context.Context, mcpClient *mcp.Client) ([]llm.ToolDefinition, error) {
	schemas, err := mcpClient.GetToolSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("get tool schemas: %w", err)
	}
	tools, err := mcpClient.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	descriptions := make(map[string]string, len(tools))
	for _, t := range tools {
		descriptions[t.Name] = t.Description
	}

	var allow map[string]bool
	if len(a.MCPTools) > 0 {
		allow = make(map[string]bool, len(a.MCPTools))
		for _, n := range a.MCPTools {
			allow[n] = true
		}
	}

	// Collect names, then sort. Map iteration is randomised, so without
	// this sort the tools array order would vary call-to-call, which
	// invalidates Anthropic's cache_control breakpoint on the last tool.
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		if allow != nil && !allow[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]llm.ToolDefinition, 0, len(names))
	for _, name := range names {
		out = append(out, llm.ToolDefinition{
			Name:        name,
			Description: descriptions[name],
			InputSchema: schemas[name],
		})
	}
	return out, nil
}

// executeToolCalls runs each model-requested tool via MCP and returns
// llm.ToolResult entries in the same order. Per-call logging matches
// the previous manual mode so dashboards keep working. An ActivityToolCall
// event is emitted before each MCP call so streaming clients can show
// progress in real time.
func (a Agent) executeToolCalls(ctx context.Context, mcpClient *mcp.Client, calls []llm.ToolCall, onActivity func(ActivityEvent)) []llm.ToolResult {
	results := make([]llm.ToolResult, 0, len(calls))
	for _, tc := range calls {
		emitActivity(onActivity, ActivityEvent{Type: ActivityToolCall, Agent: a.Name, Tool: tc.Name})
		args := decodeToolArgs(tc.Args)
		out, err := mcpClient.Call(ctx, tc.Name, args)
		if err != nil {
			slog.Info("mcp tool call", "agent", a.Name, "tool", tc.Name, "status", "error", "err", err)
			results = append(results, llm.ToolResult{
				ID:      tc.ID,
				Name:    tc.Name,
				Content: fmt.Sprintf("%s %v", toolErrorTag, err),
			})
			continue
		}
		if out.IsError {
			slog.Info("mcp tool call", "agent", a.Name, "tool", tc.Name, "status", "ok", "tool_is_error", true, "error_content", out.Content)
		} else {
			slog.Info("mcp tool call", "agent", a.Name, "tool", tc.Name, "status", "ok", "tool_is_error", false)
		}
		formatted := formatToolResult(out)
		slog.Debug("tool result content", "agent", a.Name, "tool", tc.Name, "content", formatted)
		results = append(results, llm.ToolResult{
			ID:      tc.ID,
			Name:    tc.Name,
			Content: a.truncateToolContent(tc.Name, formatted),
		})
	}
	return results
}

func decodeToolArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return map[string]any{}
	}
	return args
}

func (a Agent) buildInitialMessages(agentCtx memory.AgentContext, userMessage string) []llm.Message {
	messages := make([]llm.Message, 0, len(agentCtx.History)+1)
	messages = append(messages, agentCtx.History...)
	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: buildAgentPrompt(agentCtx.Profile, userMessage),
	})
	return messages
}

func buildAgentPrompt(profile memory.Profile, userMessage string) string {
	var sb strings.Builder
	if len(profile) > 0 {
		sb.WriteString(sectionProfile)
		sb.WriteByte('\n')
		if b, err := json.MarshalIndent(profile, "", "  "); err == nil {
			sb.Write(b)
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString(sectionUserQuestion)
	sb.WriteString(userMessage)
	return sb.String()
}

func formatToolResult(r mcp.ToolResult) string {
	var sb strings.Builder
	for _, block := range r.Content {
		sb.WriteString(block.Text)
	}
	if r.IsError {
		return toolErrorTag + " " + sb.String()
	}
	return sb.String()
}

// truncateToolContent caps content at the agent's MaxToolResultChars
// (or DefaultMaxToolResultChars when unset). By default it keeps the
// END of the content and drops from the start — preserving the most
// recent entries in date-sorted MCP responses. Set Agent.TruncateFromEnd
// to flip the strategy (preserve the start, drop the end).
//
// Both modes back off to a valid UTF-8 rune boundary near the cut and
// include a marker indicating how many bytes were dropped.
func (a Agent) truncateToolContent(tool, content string) string {
	limit := a.MaxToolResultChars
	if limit <= 0 {
		limit = DefaultMaxToolResultChars
	}
	if len(content) <= limit {
		return content
	}

	if a.TruncateFromEnd {
		// Preserve the start; drop the tail. Walk back from limit to
		// a rune boundary so we never split a multi-byte character.
		cut := limit
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		removed := len(content) - cut
		slog.Warn("tool result truncated",
			"agent", a.Name,
			"tool", tool,
			"original_chars", len(content),
			"limit", limit,
			"strategy", "cut_end",
		)
		return content[:cut] + fmt.Sprintf(truncationSuffixFmt, removed)
	}

	// Default: preserve the tail (most-recent in date-ordered output);
	// drop the head. Walk forward from the candidate start until we
	// hit a rune boundary.
	start := len(content) - limit
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	removed := start
	slog.Warn("tool result truncated",
		"agent", a.Name,
		"tool", tool,
		"original_chars", len(content),
		"limit", limit,
		"strategy", "cut_start",
	)
	return fmt.Sprintf(truncationPrefixFmt, removed) + content[start:]
}

// clientFor picks the LLM client for a model. Gemini-prefixed models
// route to a fresh GeminiClient; everything else uses the provided
// (typically Anthropic) client.
func clientFor(model string, anthropic llm.Client) llm.Client {
	if strings.HasPrefix(model, geminiModelPrefix) {
		return llm.NewGeminiClient()
	}
	return anthropic
}

// DefaultAgents returns the built-in agent roster. Empty by default —
// callers register their own agents on the Coordinator.
func DefaultAgents() []Agent {
	return []Agent{}
}