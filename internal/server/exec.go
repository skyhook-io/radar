package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func (s *Server) upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return (&websocket.Upgrader{CheckOrigin: s.browserOriginAllowed}).Upgrade(w, r, nil)
}

const podExecHeartbeatInterval = 30 * time.Second
const terminalCloseTimeout = time.Second

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

func defaultExecContainer(pod *corev1.Pod) string {
	if container := k8score.DefaultExecContainer(pod); container != "" {
		return container
	}
	// An interactive terminal still needs a default for proxy-only gateway pods.
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name
	}
	return ""
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
	cancel    context.CancelFunc
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
		session.cancel()
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

func runPodExecHeartbeat(conn *websocket.Conn, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			// Active output already keeps intermediaries alive, so let the Ping
			// wait behind it instead of timing out a healthy busy terminal.
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Time{}); err != nil {
				return
			}
		}
	}
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

// errExecDisabled reads as a Kubernetes "forbidden" so callers that classify
// exec failures treat it as a permission denial.
var errExecDisabled = errors.New("forbidden: exec is disabled on this Radar instance (--disable-exec)")

// requireExecEnabled rejects routes that run commands in Pods or start debug
// shells when the operator started Radar with --disable-exec.
func (s *Server) requireExecEnabled(w http.ResponseWriter) bool {
	if k8s.ForceDisableExec {
		s.writeError(w, http.StatusForbidden, "exec is disabled on this Radar instance (--disable-exec)")
		return false
	}
	return true
}

// handlePodExec handles WebSocket connections for pod exec
func (s *Server) handlePodExec(w http.ResponseWriter, r *http.Request) {
	if !s.requireExecEnabled(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	overrideShell := r.URL.Query().Get("shell")

	// Upgrade to WebSocket
	conn, err := s.upgradeWebSocket(w, r)
	if err != nil {
		log.Printf("WebSocket upgrade error (origin=%q host=%q): %v", r.Header.Get("Origin"), r.Host, err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
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
		cancel:    cancel,
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
	go func() {
		runPodExecHeartbeat(conn, podExecHeartbeatInterval, ctx.Done())
		cancel()
	}()

	// Get K8s client and config (impersonated when auth is enabled)
	client := s.getClientForRequest(r)
	config := s.getConfigForRequest(r)
	if client == nil || config == nil {
		sendWSError(conn, "K8s client not initialized")
		return
	}
	auth.AuditLog(r, namespace, podName)

	// Fetch the pod when we need it for OS detection (?shell= not explicit) or
	// to default an unspecified container. Defaulting to containers[0] without
	// consulting kubectl.kubernetes.io/default-container lands mesh-injected
	// pods in their distroless sidecar (no shell); honor the annotation like
	// kubectl does. Pod-fetch failure is non-fatal: log, assume Linux, and let
	// the apiserver apply its own containers[0] default.
	var podOS string
	var podUID types.UID
	expectedUID := types.UID(r.URL.Query().Get("uid"))
	if overrideShell == "" || container == "" || expectedUID != "" {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if expectedUID != "" {
			if err != nil {
				sendWSError(conn, fmt.Sprintf("Could not verify the original Pod: %v", err))
				return
			}
			if pod.UID != expectedUID {
				sendWSError(conn, "The original Pod was replaced. Reopen its details to start a terminal.")
				return
			}
		}
		if err != nil {
			log.Printf("[exec] pod fetch failed for %s/%s (OS detection + container defaulting skipped): %v", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(podName), err)
		} else {
			podUID = pod.UID
			if overrideShell == "" {
				podOS = detectPodOS(ctx, pod, nodeLabelsLookupFor(client))
			}
			if container == "" {
				container = defaultExecContainer(pod)
				execManager.mu.Lock()
				session.Container = container
				execManager.mu.Unlock()
				log.Printf("[exec] session %s defaulted to container %q for %s/%s", sessionID, container, k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(podName))
			}
		}
	}
	command := defaultExecCommand(overrideShell, DefaultPodShellCommand, podOS)

	// Create remote command executor
	exec, err := k8score.NewPodExecExecutor(client, config, namespace, podName, container, command, true)
	if err != nil {
		sendWSError(conn, fmt.Sprintf("Failed to create executor: %v", err))
		return
	}

	streamPodTerminal(ctx, conn, podUID, command[0], exec.StreamWithContext)
}

func streamPodTerminal(parent context.Context, conn *websocket.Conn, uid types.UID, executable string, stream func(context.Context, remotecommand.StreamOptions) error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stdinReader, stdinWriter := io.Pipe()
	sizes := &terminalSizeQueue{resizeChan: make(chan remotecommand.TerminalSize, 1)}
	sizes.resizeChan <- remotecommand.TerminalSize{Width: 80, Height: 24}
	output := &wsWriter{conn: conn}

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer close(sizes.resizeChan)
		defer stdinWriter.Close()
		var reportedMalformed bool
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					log.Printf("[exec] terminal WebSocket read failed: %v", err)
				}
				return
			}
			var msg TerminalMessage
			if err := json.Unmarshal(frame, &msg); err != nil {
				if !reportedMalformed {
					log.Printf("[exec] malformed terminal WebSocket message")
					reportedMalformed = true
				}
				continue
			}
			switch msg.Type {
			case "input":
				// A blocked Kubernetes stdin must backpressure the browser, not discard keystrokes.
				if _, err := io.WriteString(stdinWriter, msg.Data); err != nil {
					if errors.Is(err, io.ErrClosedPipe) {
						drainTerminalPeer(conn)
					}
					return
				}
			case "resize":
				select {
				case <-sizes.resizeChan:
				default:
				}
				sizes.resizeChan <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}
			}
		}
	}()

	streamDone := make(chan struct{})
	var streamErr error
	go func() {
		defer close(streamDone)
		streamErr = stream(ctx, remotecommand.StreamOptions{Stdin: stdinReader, Stdout: output, Stderr: output, Tty: true, TerminalSizeQueue: sizes})
	}()

	select {
	case <-streamDone:
		if ctx.Err() == nil {
			deadline := time.Now().Add(terminalCloseTimeout)
			if streamErr != nil {
				conn.SetWriteDeadline(deadline)
				sendPodExecError(conn, streamErr, uid, executable)
			}
			if writeTerminalClose(conn, deadline) == nil {
				// Release any blocked input so the sole reader can consume the peer's close reply.
				stdinReader.Close()
				select {
				case <-inputDone:
				case <-ctx.Done():
				case <-time.After(time.Until(deadline)):
				}
			}
		}
	case <-inputDone:
	case <-ctx.Done():
	}
	cancel()
	conn.Close()
	stdinReader.Close()
	<-inputDone
	<-streamDone
}

func sendPodExecError(conn *websocket.Conn, err error, podUID types.UID, executable string) {
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
	sendWSErrorWithType(conn, errorType, errMsg, podUID, executable)
}

func sendWSError(conn *websocket.Conn, msg string) {
	deadline := time.Now().Add(terminalCloseTimeout)
	conn.SetWriteDeadline(deadline)
	sendWSErrorWithType(conn, "exec_error", msg, "", "")
	if writeTerminalClose(conn, deadline) == nil {
		conn.SetReadDeadline(deadline)
		drainTerminalPeer(conn)
	}
}

func writeTerminalClose(conn *websocket.Conn, deadline time.Time) error {
	return conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline)
}

func drainTerminalPeer(conn *websocket.Conn) {
	for {
		if _, _, err := conn.NextReader(); err != nil {
			return
		}
	}
}

// sendWSErrorWithType sends an error with a specific error type to help frontend distinguish error causes
func sendWSErrorWithType(conn *websocket.Conn, errorType, msg string, podUID types.UID, executable string) {
	errMsg := struct {
		Type       string    `json:"type"`
		ErrorType  string    `json:"errorType,omitempty"`
		Executable string    `json:"executable,omitempty"`
		PodUID     types.UID `json:"podUID,omitempty"`
		Data       string    `json:"data"`
	}{
		Type:       "error",
		ErrorType:  errorType,
		Executable: executable,
		PodUID:     podUID,
		Data:       msg,
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
		"executable not found",
		"not found in $path",
		// POSIX exit 127 = "command not found". Some runtime/kubelet
		// combinations surface a missing shell as "command terminated with
		// exit code 127" via the exec stream rather than as a structured
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
	if !s.requireExecEnabled(w) {
		return
	}
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

func (s *Server) handleNodeDebugCleanup(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	nodeName := chi.URLParam(r, "name")
	if nodeName == "" {
		s.writeError(w, http.StatusBadRequest, "node name is required")
		return
	}

	namespace := r.URL.Query().Get("namespace")
	podName := r.URL.Query().Get("podName")
	uid := r.URL.Query().Get("uid")
	if err := k8score.ValidateNodeDebugPodIdentity(namespace, podName, types.UID(uid)); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	auth.AuditLog(r, namespace, podName)
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	if err := k8score.DeleteNodeDebugPod(r.Context(), client, namespace, podName, types.UID(uid)); err != nil {
		if apierrors.IsConflict(err) {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		}
		log.Printf("[exec] Failed to cleanup node debug pod %s/%s: %v", namespace, podName, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, map[string]string{"status": "ok"})
}

// DebugContainerRequest is the request body for creating a debug container
type DebugContainerRequest struct {
	PodUID            types.UID `json:"uid,omitempty"`
	TargetContainer   string    `json:"targetContainer,omitempty"`
	Image             string    `json:"image,omitempty"`
	ReturnImmediately bool      `json:"returnImmediately,omitempty"`
}

// DebugContainerResponse is the response after creating a debug container
type DebugContainerResponse struct {
	ContainerName string `json:"containerName"`
	Image         string `json:"image"`
	Status        string `json:"status"`
}

// handleCreateDebugContainer creates an ephemeral debug container in a pod
func (s *Server) handleCreateDebugContainer(w http.ResponseWriter, r *http.Request) {
	if !s.requireExecEnabled(w) {
		return
	}
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
		PodUID:          req.PodUID,
		TargetContainer: req.TargetContainer,
		Image:           s.resolveDebugImage(req.Image),
	}, client)
	if err != nil {
		errMsg := err.Error()
		if apierrors.IsForbidden(err) {
			s.writeError(w, http.StatusForbidden, errMsg)
			return
		}
		if apierrors.IsConflict(err) {
			s.writeError(w, http.StatusConflict, errMsg)
			return
		}
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, errMsg)
			return
		}
		if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
			s.writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		if strings.Contains(errMsg, "ephemeral containers are disabled") {
			s.writeError(w, http.StatusBadRequest, "ephemeral containers are not enabled on this cluster")
			return
		}
		log.Printf("[exec] Failed to create debug container for %s/%s: %v", namespace, podName, err)
		s.writeError(w, http.StatusInternalServerError, errMsg)
		return
	}

	status := "pending"
	if !req.ReturnImmediately {
		if err = k8score.WaitForEphemeralContainer(r.Context(), client, namespace, podName, ec.Name, 30*time.Second); err == nil {
			status = "running"
		} else {
			log.Printf("[exec] Debug container %s created but not yet running: %v", ec.Name, err)
		}
	}

	s.writeJSON(w, DebugContainerResponse{
		ContainerName: ec.Name,
		Image:         ec.Image,
		Status:        status,
	})
}
