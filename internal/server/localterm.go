package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/skyhook-io/radar/internal/k8s"
)

// ptyHandle abstracts platform-specific PTY implementations
type ptyHandle interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// shellProcess holds the started shell process and its PTY
type shellProcess struct {
	pty     ptyHandle
	process *os.Process
}

// LocalTermSession tracks an active local terminal session
type LocalTermSession struct {
	ID    string `json:"id"`
	Shell string `json:"shell"`
	conn  *websocket.Conn
	proc  *shellProcess
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
		if session.proc != nil {
			if session.proc.process != nil {
				signalProcess(session.proc.process)
			}
			session.proc.pty.Close()
		}
		if session.conn != nil {
			session.conn.Close()
		}
		delete(localTermMgr.sessions, id)
	}
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
	// with a temp copy that has current-context set to Radar's active context.
	env := os.Environ()
	tmpKubeconfig, err := k8s.WriteKubeconfigForCurrentContext()
	if err != nil {
		log.Printf("[localterm] Failed to write temp kubeconfig, falling back to default: %v", err)
		if kubeconfigPath := k8s.GetKubeconfigPath(); kubeconfigPath != "" {
			env = setEnv(env, "KUBECONFIG", kubeconfigPath)
		}
	} else {
		env = setEnv(env, "KUBECONFIG", tmpKubeconfig)
	}

	// Ensure TERM is set so the shell's terminfo binds the escape sequences
	// xterm.js emits (e.g. Backspace → 0x7f, Delete → CSI 3~). When Radar is
	// launched from a GUI (desktop app, browser), the inherited env often has
	// no TERM, leaving backspace/delete keys broken.
	env = setEnv(env, "TERM", "xterm-256color")

	// Get home directory (cross-platform)
	homeDir, _ := os.UserHomeDir()

	// Start shell with PTY
	proc, err := startShell(shell, env, homeDir)
	if err != nil {
		log.Printf("[localterm] Failed to start PTY: %v", err)
		sendWSError(conn, fmt.Sprintf("Failed to start shell: %v", err))
		conn.Close()
		return
	}

	// Set initial terminal size
	proc.pty.Resize(80, 24)

	session := &LocalTermSession{
		ID:    sessionID,
		Shell: shell,
		conn:  conn,
		proc:  proc,
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

		if proc.process != nil {
			signalProcess(proc.process)
		}
		proc.pty.Close()
		waitProcess(proc)
		conn.Close()
		if tmpKubeconfig != "" {
			os.Remove(tmpKubeconfig)
		}
		log.Printf("[localterm] Session %s ended", sessionID)
	}()

	// WebSocket write mutex (PTY reader and exit sender both write)
	var wsMu sync.Mutex

	// Read from PTY → write to WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := proc.pty.Read(buf)
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
			proc.pty.Write([]byte(msg.Data))
		case "resize":
			proc.pty.Resize(msg.Cols, msg.Rows)
		}
	}
}
