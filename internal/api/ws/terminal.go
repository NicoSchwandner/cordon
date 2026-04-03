package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/api/middleware"
	dockerprovider "github.com/nicobistolfi/cordon/internal/infrastructure/docker"
	"nhooyr.io/websocket"
)

// TerminalHandler relays terminal I/O between a WebSocket client and a workspace container.
type TerminalHandler struct {
	provider *dockerprovider.Provider
	docker   *client.Client
}

func NewTerminalHandler(provider *dockerprovider.Provider) (*TerminalHandler, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &TerminalHandler{provider: provider, docker: cli}, nil
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

	// Verify auth
	tenantID := middleware.TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Get container info
	info, err := h.provider.ContainerInfo(wsID)
	if err != nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	containerID := info.ID

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

	// Build tmux command: create-or-attach a persistent session.
	// If tmux isn't available, fall back to a plain shell.
	shell := "if command -v bash >/dev/null 2>&1; then exec bash -li; else exec sh -i; fi"
	startDir := info.WorkspaceFolder
	if startDir == "" {
		startDir = "/"
	}
	tmuxCmd := fmt.Sprintf(
		`if command -v tmux >/dev/null 2>&1; then tmux new-session -As cordon -c %s; else cd %s && %s; fi`,
		startDir, startDir, shell,
	)

	// Create exec with PTY
	execConfig := container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", tmuxCmd},
		Env:          []string{"TERM=xterm-256color"},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	}

	execResp, err := h.docker.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		log.Printf("exec create error: %v", err)
		conn.Close(websocket.StatusInternalError, "failed to create exec")
		return
	}

	hijack, err := h.docker.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		log.Printf("exec attach error: %v", err)
		conn.Close(websocket.StatusInternalError, "failed to attach")
		return
	}
	defer hijack.Close()

	// Start the exec
	if err := h.docker.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{Tty: true}); err != nil {
		log.Printf("exec start error: %v", err)
		return
	}

	done := make(chan struct{})

	// Container stdout → WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := hijack.Reader.Read(buf)
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
				// Check for resize messages
				var msg resizeMsg
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					h.docker.ContainerExecResize(ctx, execResp.ID, container.ResizeOptions{
						Height: uint(msg.Rows),
						Width:  uint(msg.Cols),
					})
					continue
				}
			}

			// Regular input
			hijack.Conn.Write(data)
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
