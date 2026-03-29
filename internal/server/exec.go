package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local dev
	},
}

// ExecSession tracks an active exec WebSocket connection
type ExecSession struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	conn      *websocket.Conn
}

// execSessionManager tracks active exec sessions
type execSessionManager struct {
	sessions map[string]*ExecSession
	mu       sync.RWMutex
	nextID   int
}

var execManager = &execSessionManager{
	sessions: make(map[string]*ExecSession),
}

// GetExecSessionCount returns the number of active exec sessions
func GetExecSessionCount() int {
	execManager.mu.RLock()
	defer execManager.mu.RUnlock()
	return len(execManager.sessions)
}

// StopAllExecSessions closes all active exec WebSocket connections
func StopAllExecSessions() {
	execManager.mu.Lock()
	defer execManager.mu.Unlock()

	for id, session := range execManager.sessions {
		log.Printf("Closing exec session %s (%s/%s)", id, session.Namespace, session.Pod)
		session.conn.Close()
		delete(execManager.sessions, id)
	}
}

// TerminalMessage represents a message between client and server
type TerminalMessage struct {
	Type string `json:"type"` // "input", "resize", "output", "error"
	Data string `json:"data,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

// wsWriter wraps a websocket connection to satisfy io.Writer
type wsWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	msg := TerminalMessage{Type: "output", Data: string(p)}
	data, err := json.Marshal(msg)
	if err != nil {
		return 0, err
	}
	if err := w.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return 0, err
	}
	return len(p), nil
}

// terminalSizeQueue implements remotecommand.TerminalSizeQueue
type terminalSizeQueue struct {
	resizeChan chan remotecommand.TerminalSize
}

func (t *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-t.resizeChan
	if !ok {
		return nil
	}
	return &size
}

// handlePodExec handles WebSocket connections for pod exec
func (s *Server) handlePodExec(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")

	// Get shell - prefer bash, fall back to sh
	shell := r.URL.Query().Get("shell")
	if shell == "" {
		shell = "/bin/sh"
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	// Register the session
	execManager.mu.Lock()
	execManager.nextID++
	sessionID := fmt.Sprintf("exec-%d", execManager.nextID)
	session := &ExecSession{
		ID:        sessionID,
		Namespace: namespace,
		Pod:       podName,
		Container: container,
		conn:      conn,
	}
	execManager.sessions[sessionID] = session
	execManager.mu.Unlock()
	log.Printf("Exec session %s started (%s/%s)", sessionID, namespace, podName)

	// Ensure cleanup on exit
	defer func() {
		execManager.mu.Lock()
		delete(execManager.sessions, sessionID)
		execManager.mu.Unlock()
		conn.Close()
		log.Printf("Exec session %s ended (%s/%s)", sessionID, namespace, podName)
	}()

	// Get K8s client and config (impersonated when auth is enabled)
	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		sendWSError(conn, "K8s client not initialized")
		return
	}
	auth.AuditLog(r, namespace, podName)

	// Create SPDY executor
	exec, err := k8score.NewPodExecExecutor(client, config, namespace, podName, container, []string{shell}, true)
	if err != nil {
		sendWSError(conn, fmt.Sprintf("Failed to create executor: %v", err))
		return
	}

	// Set up pipes for stdin
	stdinReader, stdinWriter := io.Pipe()
	defer stdinWriter.Close()

	// Set up terminal size queue
	sizeQueue := &terminalSizeQueue{
		resizeChan: make(chan remotecommand.TerminalSize, 1),
	}

	// Send initial size
	sizeQueue.resizeChan <- remotecommand.TerminalSize{Width: 80, Height: 24}

	// Set up stdout/stderr writer
	wsOut := &wsWriter{conn: conn}

	// Run exec in goroutine
	execDone := make(chan error, 1)
	go func() {
		err := exec.StreamWithContext(r.Context(), remotecommand.StreamOptions{
			Stdin:             stdinReader,
			Stdout:            wsOut,
			Stderr:            wsOut,
			Tty:               true,
			TerminalSizeQueue: sizeQueue,
		})
		execDone <- err
	}()

	// Channel to receive WebSocket messages from reader goroutine
	msgChan := make(chan []byte, 1)
	readErrChan := make(chan error, 1)

	// Read WebSocket messages in a separate goroutine
	// Block on reads - use conn.Close() from watcher to unblock
	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				// Any error (including from Close()) means we're done
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) &&
					!websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					select {
					case readErrChan <- err:
					default:
					}
				}
				return
			}
			select {
			case msgChan <- message:
			default:
				// Drop message if main loop isn't reading
			}
		}
	}()

	// Watch for exec completion - close connection to unblock reader
	go func() {
		select {
		case err := <-execDone:
			if err != nil {
				errMsg := err.Error()
				errorType := "exec_error"
				if isShellNotFoundError(errMsg) {
					errorType = "shell_not_found"
				}
				log.Printf("Exec failed (%s): %v", errorType, err)
				sendWSErrorWithType(conn, errorType, errMsg)
			}
			// Give browser time to process the error message before closing.
			// Without this delay, conn.Close() tears down TCP before the
			// browser's onmessage fires, so the error never reaches the UI.
			time.Sleep(200 * time.Millisecond)
			conn.Close()
		case <-r.Context().Done():
			conn.Close()
		}
	}()

	// Main loop - process messages until read error (watcher handles exec completion)
	for {
		select {
		case err := <-readErrChan:
			// WebSocket read error
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("WebSocket read error: %v", err)
			}
			goto cleanup
		case message := <-msgChan:
			var msg TerminalMessage
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Printf("WebSocket: invalid terminal message: %v", err)
				continue
			}

			switch msg.Type {
			case "input":
				stdinWriter.Write([]byte(msg.Data))
			case "resize":
				// Drain any stale pending size so the new one isn't dropped.
				// The initial 80x24 may still be buffered if StreamWithContext
				// hasn't consumed it yet (K8s API connect is slower than local WS).
				select {
				case <-sizeQueue.resizeChan:
				default:
				}
				sizeQueue.resizeChan <- remotecommand.TerminalSize{
					Width:  msg.Cols,
					Height: msg.Rows,
				}
			}
		case <-r.Context().Done():
			goto cleanup
		}
	}

cleanup:
	close(sizeQueue.resizeChan)
	stdinWriter.Close()
}

func sendWSError(conn *websocket.Conn, msg string) {
	sendWSErrorWithType(conn, "exec_error", msg)
}

// sendWSErrorWithType sends an error with a specific error type to help frontend distinguish error causes
func sendWSErrorWithType(conn *websocket.Conn, errorType, msg string) {
	errMsg := struct {
		Type      string `json:"type"`
		ErrorType string `json:"errorType,omitempty"`
		Data      string `json:"data"`
	}{
		Type:      "error",
		ErrorType: errorType,
		Data:      msg,
	}
	data, err := json.Marshal(errMsg)
	if err != nil {
		log.Printf("[exec] Failed to marshal error message: %v", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("[exec] Failed to send error to client (%s: %s): %v", errorType, msg, err)
	}
}

// isShellNotFoundError detects errors indicating the shell binary is missing
func isShellNotFoundError(errMsg string) bool {
	patterns := []string{
		"executable file not found",
		"no such file or directory",
		"command not found",
		"oci runtime exec failed",
		"executable not found",
		"not found in $path",
	}
	errLower := strings.ToLower(errMsg)
	for _, pattern := range patterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}
	return false
}

// NodeDebugRequest is the request body for creating a node debug pod
type NodeDebugRequest struct {
	Image string `json:"image,omitempty"`
}

// NodeDebugResponse extends the pod result with a status field.
type NodeDebugResponse struct {
	k8score.NodeDebugPodResult
	Status string `json:"status"`
}

// handleNodeDebug creates a privileged debug pod on a node
func (s *Server) handleNodeDebug(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	nodeName := chi.URLParam(r, "name")
	if nodeName == "" {
		s.writeError(w, http.StatusBadRequest, "node name is required")
		return
	}

	var req NodeDebugRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	client := k8s.GetClient()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "K8s client not initialized")
		return
	}

	// Create the debug pod
	result, err := k8score.CreateNodeDebugPod(r.Context(), client, nodeName, req.Image)
	if err != nil {
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[exec] Failed to create node debug pod for %s: %v", nodeName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Wait for the pod to be running
	status := "running"
	err = k8score.WaitForPodRunning(r.Context(), client, result.Namespace, result.PodName, 60*time.Second)
	if err != nil {
		status = "pending"
		log.Printf("[exec] Node debug pod %s created but not running: %v", result.PodName, err)
	}

	s.writeJSON(w, NodeDebugResponse{
		NodeDebugPodResult: *result,
		Status:             status,
	})
}

// handleNodeDebugCleanup deletes debug pods for a node
func (s *Server) handleNodeDebugCleanup(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	nodeName := chi.URLParam(r, "name")
	if nodeName == "" {
		s.writeError(w, http.StatusBadRequest, "node name is required")
		return
	}

	client := k8s.GetClient()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "K8s client not initialized")
		return
	}

	if err := k8score.DeleteNodeDebugPods(r.Context(), client, nodeName); err != nil {
		log.Printf("[exec] Failed to cleanup node debug pods for %s: %v", nodeName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"status": "ok"})
}

// DebugContainerRequest is the request body for creating a debug container
type DebugContainerRequest struct {
	TargetContainer string `json:"targetContainer,omitempty"`
	Image           string `json:"image,omitempty"`
}

// DebugContainerResponse is the response after creating a debug container
type DebugContainerResponse struct {
	ContainerName string `json:"containerName"`
	Image         string `json:"image"`
	Status        string `json:"status"`
}

// handleCreateDebugContainer creates an ephemeral debug container in a pod
func (s *Server) handleCreateDebugContainer(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")

	var req DebugContainerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Create ephemeral container (impersonated when auth is enabled)
	auth.AuditLog(r, namespace, podName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	ec, err := k8s.CreateEphemeralContainerWithClient(r.Context(), k8s.EphemeralContainerOptions{
		Namespace:       namespace,
		PodName:         podName,
		TargetContainer: req.TargetContainer,
		Image:           req.Image,
	}, client)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") {
			s.writeError(w, http.StatusNotFound, errMsg)
			return
		}
		if strings.Contains(errMsg, "ephemeral containers are disabled") ||
			strings.Contains(errMsg, "ephemeralcontainers") {
			s.writeError(w, http.StatusBadRequest, "ephemeral containers are not enabled on this cluster")
			return
		}
		log.Printf("[exec] Failed to create debug container for %s/%s: %v", namespace, podName, err)
		s.writeError(w, http.StatusInternalServerError, errMsg)
		return
	}

	// Wait for container to be running (with timeout)
	err = k8s.WaitForEphemeralContainer(r.Context(), namespace, podName, ec.Name, 30*time.Second)
	status := "running"
	if err != nil {
		status = "pending"
		log.Printf("[exec] Debug container %s created but not yet running: %v", ec.Name, err)
	}

	s.writeJSON(w, DebugContainerResponse{
		ContainerName: ec.Name,
		Image:         ec.Image,
		Status:        status,
	})
}
