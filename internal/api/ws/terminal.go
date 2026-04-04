package ws

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"nhooyr.io/websocket"
)

// TerminalProvider opens interactive terminal sessions for workspaces.
type TerminalProvider interface {
	OpenTerminal(ctx context.Context, tenantID, workspaceID uuid.UUID, opts ports.TerminalOpts) (ports.TerminalSession, error)
}

// TerminalHandler relays terminal I/O between a WebSocket client and a workspace container.
type TerminalHandler struct {
	terminals TerminalProvider
}

func NewTerminalHandler(tp TerminalProvider) *TerminalHandler {
	return &TerminalHandler{terminals: tp}
}

type resizeMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (h *TerminalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract workspace ID from path: /ws/terminal/{workspace_id}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	wsIDStr := parts[len(parts)-1]
	wsID, err := uuid.Parse(wsIDStr)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Open terminal session via backend-agnostic interface
	session, err := h.terminals.OpenTerminal(r.Context(), tenantID, wsID, ports.TerminalOpts{})
	if err != nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	defer session.Close()

	// Accept WebSocket
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Allow connections from any origin in dev
	})
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	done := make(chan struct{})

	// Container stdout → WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := session.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("container read error: %v", err)
				}
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
				return
			}
		}
	}()

	// WebSocket → Container stdin
	go func() {
		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				return
			}

			if msgType == websocket.MessageText {
				var msg resizeMsg
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					session.Resize(uint(msg.Rows), uint(msg.Cols))
					continue
				}
			}

			session.Write(data)
		}
	}()

	<-done
}

// ApprovalWSHandler pushes approval requests to connected WebSocket clients.
type ApprovalWSHandler struct {
	// Will be wired to the approval store's broadcast mechanism
}

func NewApprovalWSHandler() *ApprovalWSHandler {
	return &ApprovalWSHandler{}
}

func (h *ApprovalWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()

	// Keep connection alive, send approval requests as they come
	// For now, just keep the connection open until client disconnects
	<-ctx.Done()
}

// AuditWSHandler streams audit entries in real-time.
type AuditWSHandler struct{}

func NewAuditWSHandler() *AuditWSHandler {
	return &AuditWSHandler{}
}

func (h *AuditWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	<-ctx.Done()
}
