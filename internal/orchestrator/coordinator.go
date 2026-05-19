package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kyzrfranz/ag0/internal/llm"
	"github.com/kyzrfranz/ag0/internal/mcp"
	"github.com/kyzrfranz/ag0/internal/memory"
)

const (
	// DefaultUserID is used until ChatRequest carries a per-user id.
	DefaultUserID = "default"

	// DefaultMaxHistoryMessages bounds conversation history sent to
	// agents and the synthesizer when Coordinator.MaxHistoryMessages
	// is zero. Older messages are dropped (most-recent kept).
	DefaultMaxHistoryMessages = 20

	// Activity event types emitted via the onActivity callback so
	// streaming callers (e.g. the WebSocket handler) can render
	// pipeline progress to the user.
	ActivityRouting      = "routing"
	ActivityAgentStart   = "agent_start"
	ActivityToolCall     = "tool_call"
	ActivityAgentDone    = "agent_done"
	ActivitySynthesizing = "synthesizing"
)

// ActivityEvent reports a step in the orchestration pipeline. Type is
// one of the Activity* constants; Agent and Tool are populated when
// relevant for the event type and otherwise empty.
type ActivityEvent struct {
	Type  string
	Agent string
	Tool  string
}

// emitActivity invokes fn safely when non-nil. Centralised so callers
// don't have to nil-check at every emit site.
func emitActivity(fn func(ActivityEvent), ev ActivityEvent) {
	if fn != nil {
		fn(ev)
	}
}

const (
	synthSystemPrompt = "You are a coordinator that replies to the user. When sub-agent responses are provided, merge them into a single coherent reply: preserve facts, drop redundancy, and do not invent details. When no agent responses are provided, answer the user message directly."

	routerSystemPrompt = "You are a routing layer. Given a user message and a list of specialist agents (name and description), pick the agents that should answer. Reply with ONLY a JSON array of agent names, e.g. [\"foo\",\"bar\"]. No prose, no markdown."

	synthHeaderUser   = "User asked: "
	synthHeaderAgents = "Agent responses:"
	synthErrorPrefix  = "[error: "
	synthErrorSuffix  = "]"

	routerHeaderAgents = "Available agents:"
	routerHeaderUser   = "User message: "
)

// Coordinator routes user messages to specialist sub-agents and
// synthesizes their responses into a single reply.
//
// LLM is the Claude Sonnet client used both for synthesis and (currently)
// for every agent invocation; each agent's Agent.Model is sent in the
// request so the same client can call different Claude models.
//
// SynthModel + SystemPrompt drive synthesize(); RouterModel drives
// selectAgents(). All three are populated from CoordinatorConfig.
type Coordinator struct {
	Agents             []Agent
	LLM                llm.Client
	SynthModel         string
	RouterModel        string
	SystemPrompt       string
	MaxToolResultChars int
	MaxHistoryMessages int
	MCP                *mcp.Client
	Store              memory.Store
}

// ChatRequest is a single user-initiated chat turn. History carries the
// prior conversation messages (alternating user / assistant) so agents
// and the synthesizer see full context.
type ChatRequest struct {
	Message string
	History []llm.Message
}

// ChatResponse is the synthesized reply returned to the user.
type ChatResponse struct {
	Response string
}

// Handle fans the message out to the relevant sub-agents in parallel
// and synthesizes their responses into a single reply.
func (c *Coordinator) Handle(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return c.handle(ctx, req, nil, nil)
}

// handle is the shared implementation for Handle (no callbacks) and
// HandleStream. onToken receives synthesizer/agent text deltas; the
// orchestrator emits onActivity events at each pipeline transition so
// streaming clients can render progress (routing, agent start/done,
// tool calls, synthesizing).
func (c *Coordinator) handle(ctx context.Context, req ChatRequest, onToken func(string), onActivity func(ActivityEvent)) (ChatResponse, error) {
	history := capHistory(req.History, c.MaxHistoryMessages)

	agentCtx := memory.NewBuilder(c.Store).
		WithProfile(ctx, DefaultUserID).
		WithHistory(history).
		Build()

	// Anchor every downstream LLM call to today's date so the model
	// can't drift onto stale dates carried by conversation history.
	// Agents and the router see the dated version; the synthesizer
	// merging agent replies still sees the original (the date already
	// lives inside whatever the agents produced).
	userMessageWithDate := fmt.Sprintf("[Today: %s]\n%s", time.Now().Format("2006-01-02"), req.Message)

	emitActivity(onActivity, ActivityEvent{Type: ActivityRouting})

	agents := c.selectAgents(ctx, userMessageWithDate)

	// Single-agent fast path: invoke directly, passing the streaming
	// callback down so the agent's final reply streams token-by-token
	// when the LLM client supports it. On success there's nothing to
	// merge so we skip synthesize entirely. On failure we fall through
	// to the multi-agent flow so synthesize can explain the error to
	// the user.
	if len(agents) == 1 {
		emitActivity(onActivity, ActivityEvent{Type: ActivityAgentStart, Agent: agents[0].Name})
		only := agents[0].Invoke(ctx, agentCtx, userMessageWithDate, c.MCP, c.LLM, onToken, onActivity)
		emitActivity(onActivity, ActivityEvent{Type: ActivityAgentDone, Agent: agents[0].Name})
		if only.Error == nil {
			slog.Info("single-agent direct reply", "agent", only.AgentName)
			return ChatResponse{Response: only.Content}, nil
		}
		return ChatResponse{}, fmt.Errorf("agent %s: %w", only.AgentName, only.Error)
	}

	responses := make([]AgentResponse, len(agents))
	var wg sync.WaitGroup
	for i, agent := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			emitActivity(onActivity, ActivityEvent{Type: ActivityAgentStart, Agent: agent.Name})
			// Multi-agent fan-out: agents do not stream tokens because
			// their outputs are aggregated by synthesize. Activity
			// events still flow so the client can see fan-out progress.
			responses[i] = agent.Invoke(ctx, agentCtx, userMessageWithDate, c.MCP, c.LLM, nil, onActivity)
			emitActivity(onActivity, ActivityEvent{Type: ActivityAgentDone, Agent: agent.Name})
		}()
	}
	wg.Wait()

	// Cost optimization: if exactly one agent answered without error, return
	// its content directly and skip the synthesis LLM call — there is
	// nothing to merge. (Only reachable when 2+ agents were selected and
	// all but one failed — the single-agent fast path above handles
	// the len==1 case.)
	if only := singleSuccessful(responses); only != nil {
		slog.Info("synthesize skipped (single successful agent)", "agent", only.AgentName)
		if onToken != nil && only.Content != "" {
			onToken(only.Content)
		}
		return ChatResponse{Response: only.Content}, nil
	}

	emitActivity(onActivity, ActivityEvent{Type: ActivitySynthesizing})
	merged, err := c.synthesize(ctx, history, req.Message, responses, onToken)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("synthesize: %w", err)
	}
	return ChatResponse{Response: merged}, nil
}

// HandleStream is the streaming counterpart to Handle. It invokes
// onToken once per text delta from the synthesizer (when the LLM
// client supports streaming), or once with the full reply when it
// doesn't / when the single-successful-agent short-circuit fires.
// onActivity, when non-nil, receives pipeline progress events
// (routing, agent_start, tool_call, agent_done, synthesizing) as
// they occur.
func (c *Coordinator) HandleStream(ctx context.Context, req ChatRequest, onToken func(string), onActivity func(ActivityEvent)) error {
	_, err := c.handle(ctx, req, onToken, onActivity)
	return err
}

// capHistory keeps only the most-recent max messages. A zero or negative
// max disables the cap. After trimming, leading non-user messages are
// dropped so the conversation still begins with a user turn (providers
// like Anthropic require strict role alternation starting from user).
func capHistory(history []llm.Message, max int) []llm.Message {
	if max <= 0 || len(history) <= max {
		return history
	}
	capped := history[len(history)-max:]
	for len(capped) > 0 && capped[0].Role != llm.RoleUser {
		capped = capped[1:]
	}
	return capped
}

// singleSuccessful returns the sole error-free AgentResponse if exactly
// one of the responses succeeded, otherwise nil.
func singleSuccessful(responses []AgentResponse) *AgentResponse {
	var found *AgentResponse
	for i := range responses {
		if responses[i].Error != nil {
			continue
		}
		if found != nil {
			return nil
		}
		found = &responses[i]
	}
	return found
}

// selectAgents asks the coordinator LLM which agents should respond to
// message. On any failure (LLM error, unparseable reply, no matches)
// it falls back to consulting every registered agent.
func (c *Coordinator) selectAgents(ctx context.Context, message string) []Agent {
	if len(c.Agents) <= 1 {
		return c.Agents
	}

	prompt := buildRouterPrompt(c.Agents, message)
	out, err := c.LLM.Complete(ctx, llm.Request{
		Model:        c.RouterModel,
		SystemPrompt: routerSystemPrompt,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: prompt},
		},
	})
	if err != nil {
		slog.Warn("agent selection failed, using all agents", "err", err)
		return c.Agents
	}

	names, err := parseAgentNames(out.Content)
	if err != nil || len(names) == 0 {
		slog.Warn("agent selection unparseable, using all agents", "err", err, "raw", out.Content)
		return c.Agents
	}

	selected := pickAgents(c.Agents, names)
	if len(selected) == 0 {
		slog.Warn("router named no known agents, using all", "requested", names)
		return c.Agents
	}

	slog.Info("selected agents", "agents", agentNames(selected))
	return selected
}

func buildRouterPrompt(agents []Agent, message string) string {
	var sb strings.Builder
	sb.WriteString(routerHeaderAgents)
	sb.WriteByte('\n')
	for _, a := range agents {
		sb.WriteString("- ")
		sb.WriteString(a.Name)
		if a.Description != "" {
			sb.WriteString(": ")
			sb.WriteString(a.Description)
		}
		sb.WriteByte('\n')
	}
	sb.WriteByte('\n')
	sb.WriteString(routerHeaderUser)
	sb.WriteString(message)
	return sb.String()
}

// parseAgentNames extracts a JSON string array from raw, tolerating
// Markdown code fences and surrounding prose.
func parseAgentNames(raw string) ([]string, error) {
	s := strings.TrimSpace(raw)
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array in response")
	}
	var names []string
	if err := json.Unmarshal([]byte(s[start:end+1]), &names); err != nil {
		return nil, err
	}
	return names, nil
}

// pickAgents returns the agents from all whose names appear in names,
// preserving the order of names and skipping unknowns and duplicates.
func pickAgents(all []Agent, names []string) []Agent {
	byName := make(map[string]Agent, len(all))
	for _, a := range all {
		byName[a.Name] = a
	}
	out := make([]Agent, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		if a, ok := byName[n]; ok {
			out = append(out, a)
			seen[n] = true
		}
	}
	return out
}

func agentNames(agents []Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}

// synthesize asks the coordinator LLM to produce the final reply. With
// agent responses it merges them; without, it answers the user message
// directly. Prior conversation history is prepended so the synthesizer
// has full context across turns.
//
// When onToken is non-nil and c.LLM implements llm.Streamer, response
// tokens are forwarded through onToken as they arrive. Otherwise the
// callback (if any) is invoked once with the assembled reply.
func (c *Coordinator) synthesize(ctx context.Context, history []llm.Message, userMessage string, parts []AgentResponse, onToken func(string)) (string, error) {
	content := userMessage
	if len(parts) > 0 {
		content = buildSynthPrompt(userMessage, parts)
	}

	messages := make([]llm.Message, 0, len(history)+1)
	messages = append(messages, history...)
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: content})

	out, err := llm.StreamOrComplete(ctx, c.LLM, llm.Request{
		Model:        c.SynthModel,
		SystemPrompt: c.SystemPrompt,
		Messages:     messages,
	}, onToken)
	if err != nil {
		return "", err
	}
	return out.Content, nil
}

func buildSynthPrompt(userMessage string, parts []AgentResponse) string {
	var sb strings.Builder
	sb.WriteString(synthHeaderUser)
	sb.WriteString(userMessage)
	sb.WriteString("\n\n")
	sb.WriteString(synthHeaderAgents)
	sb.WriteByte('\n')
	for _, p := range parts {
		sb.WriteString("- ")
		sb.WriteString(p.AgentName)
		sb.WriteString(": ")
		if p.Error != nil {
			sb.WriteString(synthErrorPrefix)
			sb.WriteString(p.Error.Error())
			sb.WriteString(synthErrorSuffix)
		} else {
			sb.WriteString(p.Content)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}