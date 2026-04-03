package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"nhooyr.io/websocket"
)

func connectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect <workspace-id>",
		Short: "Pipe a remote workspace terminal to your local terminal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wsID := args[0]
			return runConnect(wsID)
		},
	}
}

func runConnect(workspaceID string) error {
	url := strings.Replace(serverURL, "http", "ws", 1) + "/ws/terminal/" + workspaceID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("connecting to workspace: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Put terminal in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("setting raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Handle terminal resize
	go handleResize(ctx, conn)

	done := make(chan struct{})

	// Remote → local stdout
	go func() {
		defer close(done)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			os.Stdout.Write(data)
		}
	}()

	// Local stdin → remote
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err != io.EOF {
					return
				}
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
				return
			}
		}
	}()

	<-done
	return nil
}

func handleResize(ctx context.Context, conn *websocket.Conn) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)

	// Send initial size
	sendSize(ctx, conn)

	for {
		select {
		case <-sigCh:
			sendSize(ctx, conn)
		case <-ctx.Done():
			return
		}
	}
}

func sendSize(ctx context.Context, conn *websocket.Conn) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	msg := fmt.Sprintf(`{"type":"resize","cols":%d,"rows":%d}`, w, h)
	conn.Write(ctx, websocket.MessageText, []byte(msg))
}
