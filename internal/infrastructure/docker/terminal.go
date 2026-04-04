package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// dockerTerminalSession wraps Docker's HijackedResponse to implement ports.TerminalSession.
type dockerTerminalSession struct {
	hijack types.HijackedResponse
	execID string
	client *client.Client
}

func (s *dockerTerminalSession) Read(p []byte) (int, error) {
	return s.hijack.Reader.Read(p)
}

func (s *dockerTerminalSession) Write(p []byte) (int, error) {
	return s.hijack.Conn.Write(p)
}

func (s *dockerTerminalSession) Close() error {
	s.hijack.Close()
	return nil
}

func (s *dockerTerminalSession) Resize(rows, cols uint) error {
	return s.client.ContainerExecResize(context.Background(), s.execID, container.ResizeOptions{
		Height: rows,
		Width:  cols,
	})
}
