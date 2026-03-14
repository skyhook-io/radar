package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/skyhook-io/radar/internal/k8s"
)

// LocalTermSession tracks an active local terminal session
type LocalTermSession struct {
	ID    string `json:"id"`
	Shell string `json:"shell"`
	conn  *websocket.Conn
	cmd   *exec.Cmd
	ptmx  *os.File
}

type localTermSessionManager struct {
	sessions map[string]*LocalTermSession
	mu       sync.RWMutex
	nextID   int
}

var localTermMgr = &localTermSessionManager{
	sessions: make(map[string]*LocalTermSession),
}

// GetLocalTermSessionCount returns the number of active local terminal sessions
func GetLocalTermSessionCount() int {
	localTermMgr.mu.RLock()
	defer localTermMgr.mu.RUnlock()
	return len(localTermMgr.sessions)
}

// StopAllLocalTermSessions terminates all active local terminal sessions
func StopAllLocalTermSessions() {
	localTermMgr.mu.Lock()
	defer localTermMgr.mu.Unlock()

	for id, session := range localTermMgr.sessions {
		log.Printf("[localterm] Closing session %s", id)
		if session.cmd != nil && session.cmd.Process != nil {
			session.cmd.Process.Signal(syscall.SIGHUP)
		}
		if session.ptmx != nil {
			session.ptmx.Close()
		}
		if session.conn != nil {
			session.conn.Close()
		}
		delete(localTermMgr.sessions, id)
	}
}

// getDefaultShell returns the user's default shell
func getDefaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/bash"
}

// setEnv replaces or appends an environment variable in the env slice
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// handleLocalTerminal handles WebSocket connections for local terminal sessions
func (s *Server) handleLocalTerminal(w http.ResponseWriter, r *http.Request) {
	if k8s.IsInCluster() {
		s.writeError(w, http.StatusBadRequest, "local terminal not available in-cluster mode")
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[localterm] WebSocket upgrade error: %v", err)
		return
	}

	// Register session
	localTermMgr.mu.Lock()
	localTermMgr.nextID++
	sessionID := fmt.Sprintf("local-term-%d", localTermMgr.nextID)
	localTermMgr.mu.Unlock()

	shell := getDefaultShell()

	// Set up environment: inherit current process env, override KUBECONFIG
	env := os.Environ()
	if kubeconfigPath := k8s.GetKubeconfigPath(); kubeconfigPath != "" {
		env = setEnv(env, "KUBECONFIG", kubeconfigPath)
	}

	// Start shell with PTY
	cmd := exec.Command(shell, "-l")
	cmd.Env = env
	cmd.Dir = os.Getenv("HOME")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("[localterm] Failed to start PTY: %v", err)
		sendWSError(conn, fmt.Sprintf("Failed to start shell: %v", err))
		conn.Close()
		return
	}

	// Set initial terminal size
	pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	session := &LocalTermSession{
		ID:    sessionID,
		Shell: shell,
		conn:  conn,
		cmd:   cmd,
		ptmx:  ptmx,
	}
	localTermMgr.mu.Lock()
	localTermMgr.sessions[sessionID] = session
	localTermMgr.mu.Unlock()
	log.Printf("[localterm] Session %s started (shell=%s)", sessionID, shell)

	// Ensure cleanup on exit
	defer func() {
		localTermMgr.mu.Lock()
		delete(localTermMgr.sessions, sessionID)
		localTermMgr.mu.Unlock()

		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGHUP)
		}
		ptmx.Close()
		cmd.Wait()
		conn.Close()
		log.Printf("[localterm] Session %s ended", sessionID)
	}()

	// WebSocket write mutex (PTY reader and exit sender both write)
	var wsMu sync.Mutex

	// Read from PTY → write to WebSocket
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				msg := TerminalMessage{Type: "output", Data: string(buf[:n])}
				data, _ := json.Marshal(msg)
				wsMu.Lock()
				writeErr := conn.WriteMessage(websocket.TextMessage, data)
				wsMu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if err != nil {
				// PTY closed (shell exited)
				exitMsg, _ := json.Marshal(map[string]string{"type": "exit"})
				wsMu.Lock()
				conn.WriteMessage(websocket.TextMessage, exitMsg)
				wsMu.Unlock()
				time.Sleep(200 * time.Millisecond)
				conn.Close()
				return
			}
		}
	}()

	// Read from WebSocket → write to PTY (input) + handle resize
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) &&
				!websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[localterm] WebSocket read error: %v", err)
			}
			return
		}

		var msg TerminalMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			ptmx.Write([]byte(msg.Data))
		case "resize":
			pty.Setsize(ptmx, &pty.Winsize{
				Rows: msg.Rows,
				Cols: msg.Cols,
			})
		}
	}
}
