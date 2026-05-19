package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kyzrfranz/ag0/internal/llm"
	"github.com/kyzrfranz/ag0/internal/mcp"
	"github.com/kyzrfranz/ag0/internal/memory"
	"github.com/kyzrfranz/ag0/internal/orchestrator"
	"github.com/kyzrfranz/ag0/ui"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const (
	envPort              = "PORT"
	envAnthropicKey      = "ANTHROPIC_API_KEY"
	envGoogleKey         = "GOOGLE_API_KEY"
	envMCPURL            = "MCP_URL"
	envMongoURI          = "MONGODB_URI"
	envAgentsConfig      = "AGENTS_CONFIG"
	envCoordinatorConfig = "COORDINATOR_CONFIG"
	envCORSOrigin         = "CORS_ORIGIN"
	envDefaultSessionID   = "DEFAULT_SESSION_ID"
	envMaxToolResultChars = "MAX_TOOL_RESULT_CHARS"
	envMaxHistoryMessages = "MAX_HISTORY_MESSAGES"
	envLogLevel           = "LOG_LEVEL"
	envUserContext        = "USER_CONTEXT"

	defaultLogLevel = "info"

	defaultPort              = "9090"
	defaultAgentsConfig      = "agents.yaml"
	defaultCoordinatorConfig = "coordinator.yaml"
	defaultUserContext       = "context.md"
	defaultCORSOrigin        = "*"

	headerACAllowOrigin   = "Access-Control-Allow-Origin"
	headerACAllowMethods  = "Access-Control-Allow-Methods"
	headerACAllowHeaders  = "Access-Control-Allow-Headers"
	headerACExposeHeaders = "Access-Control-Expose-Headers"

	corsAllowMethods  = "GET, POST, OPTIONS"
	corsAllowHeaders  = "Content-Type, X-Session-Id"
	corsExposeHeaders = "X-Session-Id"

	headerSessionID = "X-Session-Id"

	shutdownTimeout = 15 * time.Second

	pathChat    = "/chat"
	pathHealth  = "/health"
	pathWSChat  = "/ws/chat"
	pathHistory = "/history"

	wsSessionQueryParam = "session_id"

	wsFrameTypeChunk    = "chunk"
	wsFrameTypeActivity = "activity"
	wsFrameTypeDone     = "done"
	wsFrameTypeError    = "error"
	wsFrameTypeReplay   = "replay"
	wsFrameTypeAck      = "ack"

	mimeJSON = "application/json"
)

type chatRequest struct {
	Message string `json:"message"`
}

type chatResponse struct {
	Response string `json:"response"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type historyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type historyResponse struct {
	Messages []historyMessage `json:"messages"`
}

// WebSocket frame types — all carry a Type discriminator so a single
// client switch can dispatch.
type wsChunkFrame struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	MsgID string `json:"msg_id"`
}

type wsActivityFrame struct {
	Type  string `json:"type"`
	Event string `json:"event"`
	Agent string `json:"agent,omitempty"`
	Tool  string `json:"tool,omitempty"`
}

type wsDoneFrame struct {
	Type  string `json:"type"`
	MsgID string `json:"msg_id"`
}

type wsErrorFrame struct {
	Type  string `json:"type"`
	Error string `json:"error"`
}

// wsReplayFrame is sent on connect when the previous turn finished but
// was never acknowledged — the client missed (some of) the chunks.
type wsReplayFrame struct {
	Type  string `json:"type"`
	MsgID string `json:"msg_id"`
	Text  string `json:"text"`
}

func main() {
	logLevel := parseLogLevel(EnvOrDefault(envLogLevel, defaultLogLevel))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	port := EnvOrDefault(envPort, defaultPort)
	mcpURL := os.Getenv(envMCPURL)
	mongoURI := os.Getenv(envMongoURI)
	anthropicKey := os.Getenv(envAnthropicKey)
	googleKey := os.Getenv(envGoogleKey)

	store, err := buildStore(mongoURI)
	if err != nil {
		slog.Error("init store", "err", err)
		os.Exit(1)
	}

	agentsPath := EnvOrDefault(envAgentsConfig, defaultAgentsConfig)
	agents, err := orchestrator.LoadAgents(agentsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			slog.Error("agents config not found",
				"path", agentsPath,
				"hint", "copy agents.example.yaml to agents.yaml and customize",
			)
		} else {
			slog.Error("load agents", "path", agentsPath, "err", err)
		}
		os.Exit(1)
	}

	maxToolResultChars := envIntOrDefault(envMaxToolResultChars, orchestrator.DefaultMaxToolResultChars)
	for i := range agents {
		agents[i].MaxToolResultChars = maxToolResultChars
	}

	maxHistoryMessages := envIntOrDefault(envMaxHistoryMessages, orchestrator.DefaultMaxHistoryMessages)

	coordPath := EnvOrDefault(envCoordinatorConfig, defaultCoordinatorConfig)
	coordCfg, err := orchestrator.LoadCoordinator(coordPath)
	if err != nil {
		slog.Error("load coordinator config", "path", coordPath, "err", err)
		os.Exit(1)
	}

	userContextPath := EnvOrDefault(envUserContext, defaultUserContext)
	switch data, err := os.ReadFile(userContextPath); {
	case err == nil:
		contextBlock := "\n\n## User Context\n" + string(data)
		coordCfg.SystemPrompt += contextBlock
		for i := range agents {
			agents[i].SystemPrompt += contextBlock
		}
		slog.Info("user context loaded",
			"path", userContextPath,
			"chars", len(data),
			"applied_to_agents", len(agents),
		)
	case errors.Is(err, os.ErrNotExist):
		slog.Info("no user context loaded", "path", userContextPath)
	default:
		slog.Warn("user context read failed", "path", userContextPath, "err", err)
	}

	var mcpClient *mcp.Client
	if mcpURL != "" {
		mcpClient, err = mcp.NewClient(mcpURL)
		if err != nil {
			slog.Error("init mcp", "url", mcpURL, "err", err)
			os.Exit(1)
		}
	}

	coord := &orchestrator.Coordinator{
		Agents:             agents,
		LLM:                llm.NewAnthropicClient(),
		SynthModel:         coordCfg.Model,
		RouterModel:        coordCfg.RouterModel,
		SystemPrompt:       coordCfg.SystemPrompt,
		MaxToolResultChars: maxToolResultChars,
		MaxHistoryMessages: maxHistoryMessages,
		MCP:                mcpClient,
		Store:              store,
	}

	corsOrigin := EnvOrDefault(envCORSOrigin, defaultCORSOrigin)

	defaultSessionID := os.Getenv(envDefaultSessionID)
	defaultSessionSource := "env"
	if defaultSessionID == "" {
		id, err := newUUID()
		if err != nil {
			slog.Error("default session id generation failed", "err", err)
			os.Exit(1)
		}
		defaultSessionID = id
		defaultSessionSource = "generated"
	}
	slog.Info("default session id", "id", defaultSessionID, "source", defaultSessionSource)

	slog.Info("starting ag0",
		"port", port,
		"mcp_url", mcpURL,
		"mongo", mongoURI != "",
		"anthropic_key", presence(anthropicKey),
		"google_key", presence(googleKey),
		"synth_model", coordCfg.Model,
		"router_model", coordCfg.RouterModel,
		"agents_config", agentsPath,
		"coordinator_config", coordPath,
		"user_context", userContextPath,
		"agents", len(coord.Agents),
		"cors_origin", corsOrigin,
		"default_session_id", defaultSessionID,
		"max_tool_result_chars", maxToolResultChars,
		"max_history_messages", maxHistoryMessages,
		"log_level", logLevel.String(),
	)

	sessions := NewSessionStore()

	mux := http.NewServeMux()
	mux.HandleFunc(pathChat, chatHandler(coord, sessions, defaultSessionID))
	mux.HandleFunc(pathHealth, healthHandler)
	mux.HandleFunc(pathHistory, historyHandler(sessions))
	mux.HandleFunc(pathWSChat, wsChatHandler(coord, sessions, defaultSessionID, corsOrigin))

	distFS, err := ui.DistFS()
	if err != nil {
		slog.Error("ui dist fs", "err", err)
		os.Exit(1)
	}
	mux.Handle("/", spaHandler(distFS))

	server := &http.Server{
		Addr:    ":" + port,
		Handler: cors(corsOrigin, mux),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-errCh:
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	slog.Info("stopped")
}

func buildStore(mongoURI string) (memory.Store, error) {
	if mongoURI == "" {
		slog.Info("using in-memory profile store")
		return memory.NewInMemoryStore(), nil
	}
	return memory.NewMongoStore()
}

func chatHandler(coord *orchestrator.Coordinator, sessions *SessionStore, defaultSessionID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
			return
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("invalid request: %v", err)})
			return
		}

		sessionID := r.Header.Get(headerSessionID)
		source := "request"
		if sessionID == "" {
			sessionID = defaultSessionID
			source = "default"
		}
		slog.Info("session", "id", sessionID, "source", source)
		w.Header().Set(headerSessionID, sessionID)

		sess := sessions.Get(sessionID)
		history := sess.Snapshot()

		resp, err := coord.Handle(r.Context(), orchestrator.ChatRequest{
			Message: req.Message,
			History: history,
		})
		if err != nil {
			slog.Error("chat handle failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}

		sess.Append(
			llm.Message{Role: llm.RoleUser, Content: req.Message},
			llm.Message{Role: llm.RoleAssistant, Content: resp.Response},
		)

		writeJSON(w, http.StatusOK, chatResponse{Response: resp.Response})
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// historyHandler returns the conversation history for a given session.
// Unknown sessions yield an empty messages array (200 OK) — the client
// can use the same endpoint to bootstrap a fresh chat without a
// pre-check. session_id is the only required field.
func historyHandler(sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
			return
		}
		sessionID := r.URL.Query().Get(wsSessionQueryParam)
		if sessionID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing session_id"})
			return
		}

		msgs := sessions.History(sessionID)
		out := historyResponse{Messages: make([]historyMessage, len(msgs))}
		for i, m := range msgs {
			out.Messages[i] = historyMessage{Role: m.Role, Content: m.Content}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// wsChatHandler upgrades to a WebSocket and runs a per-connection
// read/respond loop. Each inbound JSON message {"message": "..."} is
// handed to coord.HandleStream; emitted tokens (currently one per turn)
// are pushed back as {"response": "...", "session_id": "..."}.
//
// The session id is taken from the "session_id" query parameter
// (falling back to the X-Session-Id header for parity with /chat).
// When absent, the connection joins the global default session.
//
// Origin enforcement is driven by CORS_ORIGIN: "*" or "" allows any
// origin; anything else is matched as a host pattern (scheme stripped).
func wsChatHandler(coord *orchestrator.Coordinator, sessions *SessionStore, defaultSessionID, corsOrigin string) http.HandlerFunc {
	originPatterns := []string{"*"}
	if corsOrigin != "" && corsOrigin != "*" {
		pattern := corsOrigin
		if i := strings.Index(pattern, "://"); i >= 0 {
			pattern = pattern[i+3:]
		}
		originPatterns = []string{pattern}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: originPatterns,
		})
		if err != nil {
			slog.Warn("ws accept failed", "err", err)
			return
		}
		defer conn.CloseNow()

		sessionID := r.URL.Query().Get(wsSessionQueryParam)
		if sessionID == "" {
			sessionID = r.Header.Get(headerSessionID)
		}
		if sessionID == "" {
			sessionID = defaultSessionID
		}
		sess := sessions.Get(sessionID)

		slog.Info("ws session", "id", sessionID)

		ctx := r.Context()

		// Replay the previous turn if the client never acked it. The
		// next inbound frame — either an ack or a fresh message —
		// implicitly clears the pending buffer.
		if msgID, content, ok := sess.PendingTurn(); ok {
			slog.Info("ws replay pending turn", "session", sessionID, "msg_id", msgID, "chars", len(content))
			if writeErr := wsjson.Write(ctx, conn, wsReplayFrame{
				Type:  wsFrameTypeReplay,
				MsgID: msgID,
				Text:  content,
			}); writeErr != nil {
				slog.Warn("ws replay write failed", "err", writeErr)
			}
		}

		for {
			var in struct {
				Type    string `json:"type"`
				MsgID   string `json:"msg_id"`
				Message string `json:"message"`
			}
			if err := wsjson.Read(ctx, conn, &in); err != nil {
				slog.Info("ws read ended",
					"session", sessionID,
					"err", err,
					"close_status", int(websocket.CloseStatus(err)),
				)
				return
			}

			if in.Type == wsFrameTypeAck {
				slog.Info("ws ack received", "session", sessionID, "msg_id", in.MsgID)
				sess.AckLastTurn(in.MsgID)
				continue
			}

			// A new user message implicitly acks whatever the client
			// last received — moving on means it had enough of the
			// previous turn to keep the conversation going.
			if pendingID, _, ok := sess.PendingTurn(); ok {
				sess.AckLastTurn(pendingID)
			}
			slog.Info("ws message received", "session", sessionID, "len", len(in.Message))

			msgID, err := newUUID()
			if err != nil {
				slog.Error("ws msg id generation failed", "err", err)
				_ = wsjson.Write(ctx, conn, wsErrorFrame{
					Type:  wsFrameTypeError,
					Error: "internal error",
				})
				continue
			}

			history := sess.Snapshot()
			var accumulated strings.Builder
			chunks := 0

			// If the WS write fails mid-turn the connection is gone,
			// but the LLM pipeline keeps running so we capture the
			// full response into the accumulator. On reconnect the
			// buffered turn is replayed in full — far better than a
			// truncated partial.
			var writeFailed atomic.Bool

			err = coord.HandleStream(ctx, orchestrator.ChatRequest{
				Message: in.Message,
				History: history,
			},
				func(token string) {
					chunks++
					accumulated.WriteString(token)
					if writeFailed.Load() {
						return
					}
					if writeErr := wsjson.Write(ctx, conn, wsChunkFrame{
						Type:  wsFrameTypeChunk,
						Text:  token,
						MsgID: msgID,
					}); writeErr != nil {
						slog.Warn("ws write failed — detaching, pipeline continues", "err", writeErr)
						writeFailed.Store(true)
					}
				},
				func(ev orchestrator.ActivityEvent) {
					if writeFailed.Load() {
						return
					}
					if writeErr := wsjson.Write(ctx, conn, wsActivityFrame{
						Type:  wsFrameTypeActivity,
						Event: ev.Type,
						Agent: ev.Agent,
						Tool:  ev.Tool,
					}); writeErr != nil {
						slog.Warn("ws activity write failed — detaching, pipeline continues", "err", writeErr)
						writeFailed.Store(true)
					}
				},
			)
			detached := writeFailed.Load()
			slog.Info("ws turn done", "session", sessionID, "msg_id", msgID, "chunks", chunks, "total_chars", accumulated.Len(), "detached", detached, "err", err)
			if err != nil {
				slog.Error("ws chat handle failed", "err", err)
				if !detached {
					_ = wsjson.Write(ctx, conn, wsErrorFrame{
						Type:  wsFrameTypeError,
						Error: err.Error(),
					})
				}
				if detached {
					return
				}
				continue
			}

			// HandleStream returned ok but emitted no chunks → the
			// pipeline produced an empty reply (e.g. model went silent
			// after tool execution). Surface as an error frame and
			// skip history append so the dangling user message can be
			// retried without polluting the session.
			if chunks == 0 {
				slog.Warn("ws turn produced no chunks", "session", sessionID)
				if !detached {
					_ = wsjson.Write(ctx, conn, wsErrorFrame{
						Type:  wsFrameTypeError,
						Error: "No response generated — please try again",
					})
				}
				if detached {
					return
				}
				continue
			}

			// Buffer the full turn regardless of whether the client
			// is still connected — if it isn't, the next connection
			// will replay this in PendingTurn().
			sess.SetLastTurn(msgID, accumulated.String())

			// Done frame is best-effort: if the socket is already
			// broken (detached or otherwise) the write fails silently
			// and the buffered turn covers replay on reconnect.
			_ = wsjson.Write(ctx, conn, wsDoneFrame{
				Type:  wsFrameTypeDone,
				MsgID: msgID,
			})

			// Persist to conversation history so the next user turn
			// has full context, even if the client never saw the
			// done frame.
			sess.Append(
				llm.Message{Role: llm.RoleUser, Content: in.Message},
				llm.Message{Role: llm.RoleAssistant, Content: accumulated.String()},
			)

			// Socket is dead — stop the per-connection loop. The
			// client will reconnect and receive the buffered turn
			// via the replay frame.
			if detached {
				return
			}
		}
	}
}

// spaHandler serves the embedded UI dist as a fallback for any request that
// didn't match an explicit API route. Unknown paths resolve to index.html so
// client-side router routes still load the app instead of 404'ing.
func spaHandler(distFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(distFS, path); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// cors wraps next so every response carries the CORS allow-headers and
// short-circuits OPTIONS preflight requests with 204. Expose-Headers
// surfaces X-Session-Id so browser clients can read the session id the
// server assigned.
func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set(headerACAllowOrigin, origin)
		h.Set(headerACAllowMethods, corsAllowMethods)
		h.Set(headerACAllowHeaders, corsAllowHeaders)
		h.Set(headerACExposeHeaders, corsExposeHeaders)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Session holds the conversation history for one chat session.
type Session struct {
	mu      sync.Mutex
	history []llm.Message

	// Buffer of the most recent assistant turn so we can replay it if
	// the client disconnects mid-stream before we (or it) saw the done
	// frame. Cleared once the client acks or sends the next message.
	lastTurnMsgID       string
	lastTurnAccumulated string
	lastTurnAcked       bool
}

// Snapshot returns a defensive copy of the current history.
func (s *Session) Snapshot() []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Message, len(s.history))
	copy(out, s.history)
	return out
}

// Append adds messages to the end of the history.
func (s *Session) Append(msgs ...llm.Message) {
	s.mu.Lock()
	s.history = append(s.history, msgs...)
	s.mu.Unlock()
}

// SetLastTurn records the full content of the just-finished assistant
// turn so it can be replayed if the client never confirmed receipt.
func (s *Session) SetLastTurn(msgID, content string) {
	s.mu.Lock()
	s.lastTurnMsgID = msgID
	s.lastTurnAccumulated = content
	s.lastTurnAcked = false
	s.mu.Unlock()
}

// AckLastTurn marks the buffered turn delivered if msgID matches. A
// stale ack (different msgID) is ignored to avoid clearing a newer
// pending turn.
func (s *Session) AckLastTurn(msgID string) {
	s.mu.Lock()
	if s.lastTurnMsgID == msgID {
		s.lastTurnAcked = true
	}
	s.mu.Unlock()
}

// PendingTurn returns the buffered turn iff it exists and has not been
// acknowledged. Used on (re)connect to decide whether to replay.
func (s *Session) PendingTurn() (msgID, content string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastTurnMsgID == "" || s.lastTurnAcked {
		return "", "", false
	}
	return s.lastTurnMsgID, s.lastTurnAccumulated, true
}

// SessionStore is a process-local map of session id → Session.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewSessionStore constructs an empty SessionStore.
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*Session)}
}

// History returns a snapshot of the session's history, or nil if the
// session does not exist. Unlike Get, it never creates a session — so
// /history lookups don't pollute the store with empty entries.
func (s *SessionStore) History(id string) []llm.Message {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return sess.Snapshot()
}

// Get returns the Session for id, creating an empty one on first use.
func (s *SessionStore) Get(id string) *Session {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok {
		return sess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		return sess
	}
	sess = &Session{}
	s.sessions[id] = sess
	return sess
}

// newUUID returns an RFC 4122 v4 UUID built from crypto/rand.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[0:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16]), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// presence returns "set" if s is non-empty, otherwise "unset". Used to
// log API key availability without leaking the value.
func presence(s string) string {
	if s == "" {
		return "unset"
	}
	return "set"
}

// EnvOrDefault returns the value of key from the environment, or def if
// the variable is unset or empty.
func EnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseLogLevel maps the LOG_LEVEL env value to a slog.Level. Unknown
// values fall back to info (logged via fmt to stderr since slog isn't
// configured yet at the point this runs).
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "unknown LOG_LEVEL %q, using info\n", s)
		return slog.LevelInfo
	}
}

// envIntOrDefault parses the env var as a positive int, falling back
// to def on missing or invalid values (and logging the fallback).
func envIntOrDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		slog.Warn("invalid int env, using default", "key", key, "value", v, "default", def)
		return def
	}
	return n
}