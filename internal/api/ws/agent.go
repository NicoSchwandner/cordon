package ws

import (
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/api/middleware"
	"nhooyr.io/websocket"
)

// AgentRegistry tracks connected sidecar agents (service container daemons).
// Each agent connects via WebSocket and registers with its workspace ID + container name.
type AgentRegistry struct {
	mu    sync.RWMutex
	conns map[string]*websocket.Conn // key: "wsID:containerName"
}

func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		conns: make(map[string]*websocket.Conn),
	}
}

func agentKey(wsID uuid.UUID, containerName string) string {
	return wsID.String() + ":" + containerName
}

// Register adds a connection for a workspace's service container.
func (r *AgentRegistry) Register(wsID uuid.UUID, containerName string, conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns[agentKey(wsID, containerName)] = conn
}

// Unregister removes a connection.
func (r *AgentRegistry) Unregister(wsID uuid.UUID, containerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, agentKey(wsID, containerName))
}

// Get returns the connection for a specific service container, if connected.
func (r *AgentRegistry) Get(wsID uuid.UUID, containerName string) (*websocket.Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	conn, ok := r.conns[agentKey(wsID, containerName)]
	return conn, ok
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
		log.Printf("[agent] websocket accept error: %v", err)
		return
	}

	log.Printf("[agent] service container connected: ws=%s container=%s", wsID.String()[:8], containerName)
	h.registry.Register(wsID, containerName, conn)

	defer func() {
		h.registry.Unregister(wsID, containerName)
		conn.Close(websocket.StatusNormalClosure, "")
		log.Printf("[agent] service container disconnected: ws=%s container=%s", wsID.String()[:8], containerName)
	}()

	// Keep connection alive until client disconnects.
	// Exec requests will be sent via conn.Write() by the exec handler.
	ctx := r.Context()
	<-ctx.Done()
}
