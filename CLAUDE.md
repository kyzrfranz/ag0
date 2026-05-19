# ag0

A generic multi-agent AI orchestration service in Go.
A coordinator agent routes user messages to specialist sub-agents, each with their own model, system prompt and MCP tools. Responses are synthesized into a single reply.

> **License:** ag0 is distributed under the [Business Source License 1.1](LICENSE).
> Personal and non-commercial use is permitted; commercial use requires a
> license (contact thomas.hieber@gmail.com). The license converts to
> Apache 2.0 on 2030-05-19. See [`LICENSE`](LICENSE) for the authoritative terms.

## Project Structure

```
cmd/ag0/
  main.go                — HTTP server, wiring, graceful shutdown, session store, CORS, SPA fallback

internal/
  orchestrator/
    coordinator.go       — routes message to sub-agents, synthesizes response, history capping
    coordinator_config.go — CoordinatorConfig, LoadCoordinator(path)
    agents.go            — Agent, AgentResponse, Invoke, native tool calling, model routing
    config.go            — AgentConfig, LoadAgents(path)

  memory/
    profile.go           — Store interface, InMemoryStore, MongoStore stub
    context.go           — AgentContext, Builder

  llm/
    llm.go               — shared types: Message, Request, Response, ToolDefinition, ToolCall, Client interface
    anthropic.go         — AnthropicClient, native tool calling, prompt caching
    gemini.go            — GeminiClient, native tool calling, implicit caching

  mcp/
    client.go            — MCP client using github.com/modelcontextprotocol/go-sdk, Call/ListTools/GetToolSchemas

ui/
  embed.go               — //go:embed all:dist; DistFS() for the SPA handler
  src/                   — Vue 3 + Pinia chat UI (formerly the standalone ag0-ui repo)
  package.json, vite.config.ts, tsconfig*.json, index.html
  dist/                  — Vite build output; created by `npm run build`, embedded at Go compile time (gitignored)
```

## Configuration

ag0 ships with `*.example.yaml` / `*.example.md` templates committed to the
repo. The real configs (`agents.yaml`, `coordinator.yaml`, `context.md`) are
**gitignored** — they hold operator-specific prompts, model choices, tool
whitelists and personal context that shouldn't go upstream.

| File | Required | Behavior when missing |
|------|----------|----------------------|
| `agents.yaml` | yes | Process exits at startup with a hint to `cp agents.example.yaml agents.yaml` |
| `coordinator.yaml` | no | Falls back to built-in defaults (Sonnet synthesis, Haiku routing, generic synth prompt) |
| `context.md` | no | Logs `no user context loaded` and continues — agent prompts are unchanged |

Skills directory follows the same pattern: `skills/*.md` is ignored,
`skills/*.example.md` is committed.

Bootstrapping a fresh checkout:

```bash
cp agents.example.yaml      agents.yaml
cp coordinator.example.yaml coordinator.yaml   # optional
cp context.example.md       context.md         # optional
$EDITOR agents.yaml                             # add API keys to .env.local separately
```

In Docker the binary follows the same rule: only the `.example.*` files
are baked into the image, so a bare `docker run` exits with the hint. Real
configs must be bind-mounted:

```bash
docker run --rm -p 9090:9090 \
  -v $(pwd)/agents.yaml:/app/agents.yaml \
  -v $(pwd)/coordinator.yaml:/app/coordinator.yaml \
  -v $(pwd)/context.md:/app/context.md \
  -e ANTHROPIC_API_KEY=... ag0
```

## Build

The UI is built first, then Go embeds the dist/ output:

```bash
# First-time setup:
cp agents.example.yaml agents.yaml   # required, then customize

# Local dev (Go binary serves both UI and API on PORT):
cd ui && npm install && npm run build
cd .. && go build ./cmd/ag0
./ag0

# Docker: single multi-stage build does both:
docker build -t ag0 .
```

For UI-only iteration without rebuilding the binary, run `cd ui && npm run dev`
(port 5173). The dev server reads `VITE_AG0_URL` from `ui/.env.local` to point
at a separately-running ag0 (e.g. `http://localhost:9090`). In the embedded
build, `VITE_AG0_URL` is left empty so the UI resolves to
`window.location.origin` and talks back to the same host that served it.

## Core Loop

```
User message + session history
  → Coordinator selects sub-agents via router LLM (haiku)
  → Each sub-agent runs concurrently:
      - Gets tool schemas from MCP SDK
      - Calls LLM with native tool calling (Claude or Gemini)
      - LLM decides which tools to call + args
      - Agent executes tools via MCP
      - LLM synthesizes tool results into agent response
  → If single agent: return directly (no synthesis call)
  → If multiple agents: coordinator synthesizes via Claude Sonnet
  → Session history updated
  → Response returned
```

## Agent Definition (agents.yaml)

```yaml
- name: agent_name
  description: used by router to select this agent
  model: claude-sonnet-4-6 | gemini-2.5-flash
  system_prompt: |
    ...
  mcp_tools:           # whitelist — empty means use all tools (dynamic discovery)
    - tool_name
```

## Coordinator Config (coordinator.yaml)

```yaml
model: claude-sonnet-4-6        # synthesis model
router_model: claude-haiku-4-5-20251001  # routing model (cheap)
system_prompt: |
  ...
```

## API

### POST /chat
```json
// Request
{ "message": "..." }
// Request headers
X-Session-Id: <uuid>   // optional, generated if missing

// Response
{ "response": "..." }
// Response headers
X-Session-Id: <uuid>   // always returned, same session
```

### GET /health
Returns 200 OK.

## Session Management

- Sessions stored in-memory (map[string]*Session)
- DEFAULT_SESSION_ID env var — all clients without X-Session-Id share this session
- History capped at MAX_HISTORY_MESSAGES (default 20)
- Sessions lost on restart (MongoDB persistence is backlog item)

## MCP Servers

- TrainingPeaks MCP: `http://192.168.3.215:8765/mcp`
- Session handshake handled by Go MCP SDK (github.com/modelcontextprotocol/go-sdk)
- Tools sorted alphabetically for stable prompt caching

## Environment Variables

```bash
ANTHROPIC_API_KEY=
GOOGLE_API_KEY=
MCP_URL=http://192.168.3.215:8765/mcp
MONGODB_URI=              # optional, uses in-memory store if unset
PORT=9090
DEFAULT_SESSION_ID=coaching-session-1
MAX_HISTORY_MESSAGES=20
MAX_TOOL_RESULT_CHARS=8000
AGENTS_CONFIG=agents.yaml
COORDINATOR_CONFIG=coordinator.yaml
USER_CONTEXT=context.md
LOG_LEVEL=info
CORS_ORIGIN=*
```

## Key Design Decisions

- **Native tool calling** — LLM decides which tools to call and fills args directly from MCP schemas. No resolveToolArgs LLM call.
- **Model routing** — agents with `model: gemini-*` use GeminiClient, others use AnthropicClient
- **Single agent shortcut** — if only one agent responds, skip synthesis call (saves one Sonnet call)
- **Prompt caching** — cache_control on system prompt and last tool for Anthropic; implicit for Gemini 2.5 Flash
- **Tool result truncation** — truncated to MAX_TOOL_RESULT_CHARS with suffix noting chars removed
- **Context cancellation** — all LLM and MCP calls respect request context; client disconnect aborts pipeline

## Backlog (priority order)

### P1 — MongoDB persistence
- Implement MongoStore TODOs in internal/memory/profile.go
- Persist sessions to MongoDB so history survives restarts

### P2 — UX
- PWA install prompt

### P3 — Scale
- Tool result truncation per-agent override in agents.yaml
- Multi-MCP support (second MCP server)

## Conventions

- Go 1.26+
- No frameworks — stdlib `net/http` only
- Interfaces for LLM clients (swappable backends)
- Config via environment variables + YAML files
- Structured logging with `log/slog`
- Errors wrapped with `fmt.Errorf("context: %w", err)`