package server

import (
	"context"
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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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

// defaultShellScript is the built-in fallback command used when no shell is
// requested explicitly via ?shell= and no override is set via --pod-shell-default.
// It exports TERM so colours and cursor movement work in container shells that
// don't set it, then detects the best available shell with `command -v` and
// execs into it.
//
// We detect with `command -v` *before* execing, rather than relying on
// `exec bash || exec ash || exec sh`, because POSIX requires a non-interactive
// shell to exit immediately when `exec` fails to find the requested command.
// That behaviour breaks the naive `||` cascade: once `exec bash` fails in an
// image without bash, the outer `sh -c` exits 127 and the `|| exec ash`
// branch never runs. The `command -v` check confirms existence first, so
// `exec` can only fail for exotic reasons (e.g. permission denied), which
// POSIX does treat as fatal — and in that case the session surfaces the
// error to the frontend, which is the correct behaviour.
//
// bash is run as `bash -il` (interactive login) so it picks up the image's
// startup files. Per the bash manual, a login shell reads /etc/profile and
// then the first existing of ~/.bash_profile, ~/.bash_login, or ~/.profile.
// Debian-family images typically also chain in /etc/bash.bashrc (via a
// distro hook in /etc/profile) and then ~/.bashrc (via the canonical
// `[ -f ~/.bashrc ] && . ~/.bashrc` line in ~/.bash_profile). One of those
// files sets PS1 — almost always containing \w — which fixes the missing
// working-directory-in-prompt symptom from skyhook-io/radar#452.
const defaultShellScript = "export TERM=xterm-256color; if command -v bash >/dev/null 2>&1; then exec bash -il; elif command -v ash >/dev/null 2>&1; then exec ash; else exec sh; fi"

// windowsDefaultShellScript prefers PowerShell, falling back to cmd.exe.
// The `|| cmd` branch is load-bearing for Nano Server images, which ship
// without PowerShell.
const windowsDefaultShellScript = `where powershell >nul 2>&1 && powershell || cmd`

// DefaultPodShellCommand, when non-empty, overrides defaultShellScript as the
// script passed to `sh -c`. Set by the bootstrap layer from the
// --pod-shell-default CLI flag. Empty means "use the built-in default".
//
// POSIX-only by design — Windows pods always use windowsDefaultShellScript
// regardless of this value. A single command string can't safely target both
// shells.
var DefaultPodShellCommand string

// defaultExecCommand builds the command argv for a pod exec session.
//
// Precedence:
//  1. ?shell= override — verbatim as a single argv element.
//  2. podOS == "windows" — cmd.exe + windowsDefaultShellScript.
//  3. --pod-shell-default fallback (POSIX-only, see DefaultPodShellCommand).
//  4. Built-in defaultShellScript.
//
// Empty podOS defaults to Linux so detection failures don't break the
// common case.
func defaultExecCommand(override, fallback, podOS string) []string {
	if override != "" {
		return []string{override}
	}
	if podOS == "windows" {
		return []string{"cmd.exe", "/c", windowsDefaultShellScript}
	}
	if fallback != "" {
		return []string{"sh", "-c", fallback}
	}
	return []string{"sh", "-c", defaultShellScript}
}

// osNodeLabelsLookup is injected so detectPodOS is unit-testable without a
// fake client.
type osNodeLabelsLookup func(ctx context.Context, nodeName string) (map[string]string, error)

// detectPodOS returns "windows" or "linux" (lowercased), or "" when unknown.
// Three tiers, in order of authority:
//
//  1. pod.Spec.OS.Name — GA in K8s 1.25, designed for exactly this.
//  2. pod.Spec.NodeSelector kubernetes.io/os (beta. variant as fallback).
//  3. The scheduled node's labels — covers pods placed by default
//     node-affinity rather than an explicit selector, common when Windows
//     nodes are tainted and admission webhooks add the toleration without
//     also injecting the selector.
//
// On tier-3 lookup failure (typically RBAC denying `get nodes`), returns ""
// so the caller defaults to Linux — matches pre-Windows-support behavior.
func detectPodOS(ctx context.Context, pod *corev1.Pod, lookupNode osNodeLabelsLookup) string {
	if pod.Spec.OS != nil && pod.Spec.OS.Name != "" {
		return strings.ToLower(string(pod.Spec.OS.Name))
	}
	if osName, ok := osFromLabels(pod.Spec.NodeSelector); ok {
		return strings.ToLower(osName)
	}
	if pod.Spec.NodeName == "" {
		return ""
	}
	labels, err := lookupNode(ctx, pod.Spec.NodeName)
	if err != nil {
		log.Printf("[exec] node label lookup for OS detection failed (node=%s, assuming Linux): %v", pod.Spec.NodeName, err)
		return ""
	}
	if osName, ok := osFromLabels(labels); ok {
		return strings.ToLower(osName)
	}
	return ""
}

// osFromLabels prefers kubernetes.io/os over the deprecated beta. variant.
func osFromLabels(m map[string]string) (string, bool) {
	if v, ok := m["kubernetes.io/os"]; ok && v != "" {
		return v, true
	}
	if v, ok := m["beta.kubernetes.io/os"]; ok && v != "" {
		return v, true
	}
	return "", false
}

func nodeLabelsLookupFor(client kubernetes.Interface) osNodeLabelsLookup {
	return func(ctx context.Context, nodeName string) (map[string]string, error) {
		node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return node.Labels, nil
	}
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
	overrideShell := r.URL.Query().Get("shell")

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

	// OS detection runs whenever ?shell= isn't explicit. --pod-shell-default
	// must NOT short-circuit — it's POSIX-only, and a Windows pod still has
	// to be routed to the Windows script ahead of the fallback. Pod-fetch
	// failure is non-fatal: log and assume Linux.
	var podOS string
	if overrideShell == "" {
		pod, err := client.CoreV1().Pods(namespace).Get(r.Context(), podName, metav1.GetOptions{})
		if err != nil {
			log.Printf("[exec] OS detection skipped for %s/%s (assuming Linux): %v", namespace, podName, err)
		} else {
			podOS = detectPodOS(r.Context(), pod, nodeLabelsLookupFor(client))
		}
	}
	command := defaultExecCommand(overrideShell, DefaultPodShellCommand, podOS)

	// Create SPDY executor
	exec, err := k8score.NewPodExecExecutor(client, config, namespace, podName, container, command, true)
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
				} else if looksLikeShellNotFound(errMsg) {
					// Drift canary: the error has shell-not-found hallmarks but
					// isShellNotFoundError's substring matcher didn't recognize
					// it. Most likely kubelet/runc/containerd reworded the
					// upstream error text. Log a breadcrumb so we notice before
					// users start filing "Failed to connect" bugs. See the
					// looksLikeShellNotFound doc comment for rationale.
					log.Printf("[exec] WARNING: error looks shell-related but isShellNotFoundError did not match — kubelet error text may have drifted; update isShellNotFoundError patterns if this recurs. errMsg=%q", errMsg)
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
		// POSIX exit 127 = "command not found". Some runtime/kubelet
		// combinations surface a missing shell as "command terminated with
		// exit code 127" via the SPDY stream rather than as a structured
		// runtime error. Our default exec wraps in `sh -c <script>`, so an
		// exit-127 from that wrapper means `sh` itself couldn't run — i.e.
		// shell missing. The drift canary picked this up against distroless
		// coredns; see skyhook-io/radar#456 (comment thread).
		"exit code 127",
		// Windows-container equivalents. hcsshim (the Host Compute Service
		// runtime backing containerd on Windows) surfaces missing executables
		// as "hcs::System::CreateProcess: ... The system cannot find the file
		// specified." The localized phrasing is what English Windows uses;
		// non-English locales emit the same hcs::System::CreateProcess prefix
		// with a translated tail, so we match the prefix as the durable
		// signal and keep the English tail for backstop coverage.
		"hcs::system::createprocess",
		"the system cannot find the file",
	}
	errLower := strings.ToLower(errMsg)
	for _, pattern := range patterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}
	return false
}

// looksLikeShellNotFound is a drift canary for isShellNotFoundError. It
// returns true when an error message has shell-not-found hallmarks using
// broader heuristics than the substring patterns above: (a) the message
// contains both "exec" and "not found", which catches most variants of
// missing-executable errors across container runtimes, or (b) the message
// contains "exit code 127", which is POSIX's reserved exit status for
// "command not found" and is what kubelet surfaces when our `sh -c` script
// can't exec the detected shell.
//
// The caller uses this only for logging, never for classification — if
// this matches but isShellNotFoundError doesn't, we log a WARNING so a
// maintainer can update the patterns. False positives here are harmless
// (noisier logs on unrelated errors) but silent false negatives there
// would demote shell-missing errors to the generic "exec_error" branch,
// losing the frontend's "Start debug container" CTA.
func looksLikeShellNotFound(errMsg string) bool {
	errLower := strings.ToLower(errMsg)
	if strings.Contains(errLower, "exec") && strings.Contains(errLower, "not found") {
		return true
	}
	if strings.Contains(errLower, "exit code 127") {
		return true
	}
	return false
}

// resolveDebugImage returns the image to use for a debug container/pod: an
// explicit per-request override, else the operator-configured --debug-image,
// else the built-in busybox default.
func (s *Server) resolveDebugImage(requested string) string {
	if requested != "" {
		return requested
	}
	if s.effectiveConfig != nil && s.effectiveConfig.DebugImage != "" {
		return s.effectiveConfig.DebugImage
	}
	return k8score.DefaultDebugImage
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

	auth.AuditLog(r, "", nodeName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	// Create the debug pod
	result, err := k8score.CreateNodeDebugPod(r.Context(), client, nodeName, s.resolveDebugImage(req.Image))
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

	auth.AuditLog(r, "", nodeName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
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
		Image:           s.resolveDebugImage(req.Image),
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
