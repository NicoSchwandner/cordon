package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"nhooyr.io/websocket"
)

// ExecMessage represents a streaming message from an agent exec request.
type ExecMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "stdout", "stderr", "exit"
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
}

// execRequest is sent from the server to an agent to request command execution.
type execRequest struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"` // "exec"
	Cmd     []string `json:"cmd"`
	WorkDir string   `json:"workdir,omitempty"`
}

// agentConn wraps a WebSocket connection with response routing for pending exec requests.
type agentConn struct {
	conn    *websocket.Conn
	pending map[string]chan ExecMessage // request ID -> response channel
	mu      sync.Mutex
}

func newAgentConn(conn *websocket.Conn) *agentConn {
	return &agentConn{
		conn:    conn,
		pending: make(map[string]chan ExecMessage),
	}
}

// readLoop reads messages from the agent and routes them to pending request channels.
func (ac *agentConn) readLoop(ctx context.Context) {
	for {
		_, data, err := ac.conn.Read(ctx)
		if err != nil {
			ac.closeAllPending()
			return
		}

		var msg ExecMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("invalid message from agent", "component", "agent", "error", err)
			continue
		}

		ac.mu.Lock()
		ch, ok := ac.pending[msg.ID]
		ac.mu.Unlock()

		if !ok {
			slog.Warn("response for unknown request", "component", "agent", "request_id", msg.ID)
			continue
		}

		ch <- msg

		if msg.Type == "exit" {
			ac.mu.Lock()
			delete(ac.pending, msg.ID)
			close(ch)
			ac.mu.Unlock()
		}
	}
}

// closeAllPending closes all pending response channels (called on disconnect).
func (ac *agentConn) closeAllPending() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	for id, ch := range ac.pending {
		close(ch)
		delete(ac.pending, id)
	}
}

// AgentRegistry tracks connected sidecar agents (service container daemons).
// Each agent connects via WebSocket and registers with its workspace ID + container name.
type AgentRegistry struct {
	mu    sync.RWMutex
	conns map[string]*agentConn // key: "wsID:containerName"
}

func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		conns: make(map[string]*agentConn),
	}
}

func agentKey(wsID uuid.UUID, containerName string) string {
	return wsID.String() + ":" + containerName
}

// Register adds a connection for a workspace's service container and starts its read loop.
func (r *AgentRegistry) Register(ctx context.Context, wsID uuid.UUID, containerName string, conn *websocket.Conn) {
	ac := newAgentConn(conn)
	r.mu.Lock()
	r.conns[agentKey(wsID, containerName)] = ac
	r.mu.Unlock()
	go ac.readLoop(ctx)
}

// Unregister removes a connection.
func (r *AgentRegistry) Unregister(wsID uuid.UUID, containerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ac, ok := r.conns[agentKey(wsID, containerName)]; ok {
		ac.closeAllPending()
	}
	delete(r.conns, agentKey(wsID, containerName))
}

// IsConnected returns whether an agent is connected for a workspace container.
func (r *AgentRegistry) IsConnected(wsID uuid.UUID, containerName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.conns[agentKey(wsID, containerName)]
	return ok
}

// Exec sends an exec request to a connected agent and returns a channel of streaming responses.
// The channel is closed when the exec completes (after the "exit" message) or on disconnect.
func (r *AgentRegistry) Exec(ctx context.Context, wsID uuid.UUID, containerName string, cmd []string, workdir string) (<-chan ExecMessage, error) {
	r.mu.RLock()
	ac, ok := r.conns[agentKey(wsID, containerName)]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no agent connected for container %s", containerName)
	}

	reqID := uuid.New().String()
	ch := make(chan ExecMessage, 64)

	ac.mu.Lock()
	ac.pending[reqID] = ch
	ac.mu.Unlock()

	req := execRequest{
		ID:      reqID,
		Type:    "exec",
		Cmd:     cmd,
		WorkDir: workdir,
	}
	data, err := json.Marshal(req)
	if err != nil {
		ac.mu.Lock()
		delete(ac.pending, reqID)
		close(ch)
		ac.mu.Unlock()
		return nil, fmt.Errorf("marshal exec request: %w", err)
	}

	if err := ac.conn.Write(ctx, websocket.MessageText, data); err != nil {
		ac.mu.Lock()
		delete(ac.pending, reqID)
		close(ch)
		ac.mu.Unlock()
		return nil, fmt.Errorf("send exec request: %w", err)
	}

	return ch, nil
}

// AgentWSHandler handles WebSocket connections from sidecar agents in service containers.
// Path: /ws/agent/{workspace_id}/{container_name}
type AgentWSHandler struct {
	registry *AgentRegistry
}

func NewAgentWSHandler(registry *AgentRegistry) *AgentWSHandler {
	return &AgentWSHandler{registry: registry}
}

func (h *AgentWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse: /ws/agent/{workspace_id}/{container_name}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ws/agent/"), "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path: expected /ws/agent/{workspace_id}/{container_name}", http.StatusBadRequest)
		return
	}

	wsID, err := uuid.Parse(parts[0])
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}
	containerName := parts[1]

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Warn("websocket accept error", "component", "agent", "error", err)
		return
	}

	slog.Info("service container connected", "component", "agent", "workspace_id", wsID.String()[:8], "container", containerName)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	h.registry.Register(ctx, wsID, containerName, conn)

	defer func() {
		h.registry.Unregister(wsID, containerName)
		conn.CloseNow()
		slog.Info("service container disconnected", "component", "agent", "workspace_id", wsID.String()[:8], "container", containerName)
	}()

	// Block until context is done (client disconnects or server shuts down).
	// The read loop runs in the background via Register.
	<-ctx.Done()
}
