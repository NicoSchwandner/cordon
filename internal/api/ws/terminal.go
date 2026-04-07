package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/application/audit"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
	"nhooyr.io/websocket"
)

// TerminalProvider opens interactive terminal sessions for workspaces.
type TerminalProvider interface {
	OpenTerminal(ctx context.Context, tenantID, workspaceID uuid.UUID, opts ports.TerminalOpts) (ports.TerminalSession, error)
}

// ActivityRecorder records workspace activity for idle timeout tracking.
type ActivityRecorder interface {
	RecordActivity(wsID uuid.UUID)
}

// TerminalHandler relays terminal I/O between a WebSocket client and a workspace container.
type TerminalHandler struct {
	terminals TerminalProvider
	activity  ActivityRecorder
}

func NewTerminalHandler(tp TerminalProvider, ar ActivityRecorder) *TerminalHandler {
	return &TerminalHandler{terminals: tp, activity: ar}
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

	if h.activity != nil {
		h.activity.RecordActivity(wsID)
	}

	// Accept WebSocket
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Allow connections from any origin in dev
	})
	if err != nil {
		slog.Warn("websocket accept error", "component", "terminal", "error", err)
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
					slog.Warn("container read error", "component", "terminal", "error", err)
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

			if h.activity != nil {
				h.activity.RecordActivity(wsID)
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
type AuditWSHandler struct {
	broadcast *audit.Broadcaster
}

func NewAuditWSHandler(broadcast *audit.Broadcaster) *AuditWSHandler {
	return &AuditWSHandler{broadcast: broadcast}
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

	// Drain reads so the connection stays alive (WebSocket requires it)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	ch := h.broadcast.Subscribe()
	defer h.broadcast.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-ch:
			// Only send entries for this tenant
			if entry.TenantID != tenantID {
				continue
			}
			data, err := json.Marshal(auditEntryJSON(entry))
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

// auditEntryJSON converts a domain audit entry to the same JSON shape the
// REST API returns, so the frontend can use one type for both.
type auditEntryWire struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	WorkspaceID string `json:"workspace_id"`
	Timestamp   string `json:"timestamp"`
	Tier        int    `json:"tier"`
	TierName    string `json:"tier_name"`
	Operation   string `json:"operation"`
	Target      string `json:"target"`
	Caller      string `json:"caller"`
	Decision    string `json:"decision"`
	DurationMs  int64  `json:"duration_ms"`
	Detail      string `json:"detail"`
}

func auditEntryJSON(e domain.AuditEntry) auditEntryWire {
	return auditEntryWire{
		ID:          e.ID.String(),
		TenantID:    e.TenantID.String(),
		WorkspaceID: e.WorkspaceID.String(),
		Timestamp:   e.Timestamp.Format(time.RFC3339Nano),
		Tier:        int(e.Tier),
		TierName:    e.Tier.String(),
		Operation:   e.Operation,
		Target:      e.Target,
		Caller:      e.Caller,
		Decision:    string(e.Decision),
		DurationMs:  e.DurationMs,
		Detail:      e.Detail,
	}
}
