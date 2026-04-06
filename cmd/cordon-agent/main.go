// cordon-agent is a lightweight sidecar binary installed in workspace containers.
// It provides two modes:
//
//	daemon  - runs in service containers, receives exec requests via WebSocket
//	exec    - runs in the primary container, routes commands to the right container
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"nhooyr.io/websocket"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: cordon-agent <daemon|exec> [args...]\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "daemon":
		runDaemon()
	case "exec":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: cordon-agent exec <command> [args...]\n")
			os.Exit(1)
		}
		runExec(os.Args[2], os.Args[3:]...)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

// repoMap describes the mapping from repo directory to service container.
type repoMapEntry struct {
	Container string `json:"container"`
	Path      string `json:"path"`
}

// loadRepoMap reads /workspace/.cordon/repos.json.
func loadRepoMap() map[string]repoMapEntry {
	data, err := os.ReadFile("/workspace/.cordon/repos.json")
	if err != nil {
		return nil
	}
	var m map[string]repoMapEntry
	json.Unmarshal(data, &m)
	return m
}

// findRepoForDir returns the repo name and entry if pwd is under a service container repo's path.
func findRepoForDir(pwd string, repoMap map[string]repoMapEntry) (string, *repoMapEntry) {
	for name, entry := range repoMap {
		if strings.HasPrefix(pwd, entry.Path) {
			return name, &entry
		}
	}
	return "", nil
}

// runExec is called in the primary container by shell wrappers.
// It checks if the current directory maps to a service container and routes accordingly.
func runExec(command string, args ...string) {
	pwd, _ := os.Getwd()
	repoMap := loadRepoMap()

	repoName, _ := findRepoForDir(pwd, repoMap)

	if repoName == "" {
		// Not under a service container repo — run locally
		execLocal(command, args...)
		return
	}

	// Route to service container via Cordon server exec API
	server := os.Getenv("CORDON_SERVER")
	wsID := os.Getenv("CORDON_WORKSPACE_ID")
	if server == "" || wsID == "" {
		// No routing config — run locally
		execLocal(command, args...)
		return
	}

	cmd := append([]string{command}, args...)

	// Build exec request
	reqBody := map[string]any{
		"repo":   repoName,
		"cmd":    []string{"sh", "-c", fmt.Sprintf("cd %s && %s", pwd, strings.Join(cmd, " "))},
		"stream": true,
	}
	body, _ := json.Marshal(reqBody)

	resp, err := http.Post(server+"/api/workspaces/"+wsID+"/exec", "application/json", strings.NewReader(string(body)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cordon-agent: routing failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		io.Copy(os.Stderr, resp.Body)
		os.Exit(1)
	}

	// Stream response
	exitCode := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		line := scanner.Text()
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Code int    `json:"code"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			fmt.Println(line)
			continue
		}
		switch msg.Type {
		case "stdout":
			fmt.Print(msg.Data)
		case "stderr":
			fmt.Fprint(os.Stderr, msg.Data)
		case "exit":
			exitCode = msg.Code
		}
	}

	os.Exit(exitCode)
}

// execLocal replaces the current process with the command.
func execLocal(command string, args ...string) {
	path, err := exec.LookPath(command)
	if err != nil {
		// Try finding the real binary (not our wrapper)
		// Remove our wrapper dir from PATH and try again
		origPath := os.Getenv("PATH")
		wrapperDir := "/workspace/.cordon/bin"
		cleanPath := removeFromPath(origPath, wrapperDir)
		os.Setenv("PATH", cleanPath)
		path, err = exec.LookPath(command)
		os.Setenv("PATH", origPath) // restore
		if err != nil {
			fmt.Fprintf(os.Stderr, "cordon-agent: %s: command not found\n", command)
			os.Exit(127)
		}
	}

	argv := append([]string{command}, args...)
	env := os.Environ()
	err = syscall.Exec(path, argv, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cordon-agent: exec failed: %v\n", err)
		os.Exit(1)
	}
}

// removeFromPath removes a directory from the PATH-style string.
func removeFromPath(pathStr, dir string) string {
	dir = filepath.Clean(dir)
	parts := strings.Split(pathStr, ":")
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if filepath.Clean(p) != dir {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, ":")
}

// runDaemon runs in service containers, accepting exec requests via WebSocket
// from the Cordon server and streaming output back.
func runDaemon() {
	server := os.Getenv("CORDON_SERVER")
	wsID := os.Getenv("CORDON_WORKSPACE_ID")
	containerName := os.Getenv("CORDON_CONTAINER_NAME")

	if server == "" || wsID == "" || containerName == "" {
		fmt.Fprintf(os.Stderr, "cordon-agent daemon: CORDON_SERVER, CORDON_WORKSPACE_ID, and CORDON_CONTAINER_NAME required\n")
		os.Exit(1)
	}

	wsURL := strings.Replace(server, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = fmt.Sprintf("%s/ws/agent/%s/%s", wsURL, wsID, containerName)

	fmt.Fprintf(os.Stderr, "cordon-agent: connecting to %s\n", wsURL)

	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		err := connectAndServe(wsURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cordon-agent: connection error: %v, reconnecting in %s...\n", err, backoff)
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}

// execRequestMsg matches the server's exec request protocol.
type execRequestMsg struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Cmd     []string `json:"cmd"`
	WorkDir string   `json:"workdir,omitempty"`
}

// execResponseMsg is sent back to the server for each output chunk or exit.
type execResponseMsg struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "stdout", "stderr", "exit"
	Data string `json:"data,omitempty"`
	Code int    `json:"code,omitempty"`
}

// connectAndServe establishes a WebSocket connection and processes exec requests.
func connectAndServe(wsURL string) error {
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	// Disable read limit — exec output can be large
	conn.SetReadLimit(-1)

	fmt.Fprintf(os.Stderr, "cordon-agent: connected, waiting for exec requests\n")

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var req execRequestMsg
		if err := json.Unmarshal(data, &req); err != nil {
			fmt.Fprintf(os.Stderr, "cordon-agent: invalid message: %v\n", err)
			continue
		}

		if req.Type != "exec" {
			fmt.Fprintf(os.Stderr, "cordon-agent: unknown message type: %s\n", req.Type)
			continue
		}

		go handleExec(ctx, conn, req)
	}
}

// handleExec runs a command and streams stdout/stderr back over the WebSocket.
func handleExec(ctx context.Context, conn *websocket.Conn, req execRequestMsg) {
	var mu sync.Mutex // serialize writes to the shared connection
	send := func(msg execResponseMsg) {
		data, err := json.Marshal(msg)
		if err != nil {
			return
		}
		mu.Lock()
		conn.Write(ctx, websocket.MessageText, data)
		mu.Unlock()
	}

	if len(req.Cmd) == 0 {
		send(execResponseMsg{ID: req.ID, Type: "stderr", Data: "empty command\n"})
		send(execResponseMsg{ID: req.ID, Type: "exit", Code: 1})
		return
	}

	cmd := exec.CommandContext(ctx, req.Cmd[0], req.Cmd[1:]...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		send(execResponseMsg{ID: req.ID, Type: "stderr", Data: fmt.Sprintf("stdout pipe: %v\n", err)})
		send(execResponseMsg{ID: req.ID, Type: "exit", Code: 1})
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		send(execResponseMsg{ID: req.ID, Type: "stderr", Data: fmt.Sprintf("stderr pipe: %v\n", err)})
		send(execResponseMsg{ID: req.ID, Type: "exit", Code: 1})
		return
	}

	if err := cmd.Start(); err != nil {
		send(execResponseMsg{ID: req.ID, Type: "stderr", Data: fmt.Sprintf("start: %v\n", err)})
		send(execResponseMsg{ID: req.ID, Type: "exit", Code: 1})
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	stream := func(r io.Reader, msgType string) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				send(execResponseMsg{ID: req.ID, Type: msgType, Data: string(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}

	go stream(stdout, "stdout")
	go stream(stderr, "stderr")

	wg.Wait()

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	send(execResponseMsg{ID: req.ID, Type: "exit", Code: exitCode})
}
