package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local dev
	},
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
	defer conn.Close()

	// Get K8s client and config
	client := k8s.GetClient()
	config := k8s.GetConfig()
	if client == nil || config == nil {
		sendWSError(conn, "K8s client not initialized")
		return
	}

	// Build exec request
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{shell},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	// Create SPDY executor
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
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

	// Read messages from WebSocket
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("WebSocket read error: %v", err)
			}
			break
		}

		var msg TerminalMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			stdinWriter.Write([]byte(msg.Data))
		case "resize":
			select {
			case sizeQueue.resizeChan <- remotecommand.TerminalSize{
				Width:  msg.Cols,
				Height: msg.Rows,
			}:
			default:
				// Drop resize if channel full
			}
		}
	}

	// Clean up
	close(sizeQueue.resizeChan)
	stdinWriter.Close()

	// Wait for exec to finish
	if err := <-execDone; err != nil {
		log.Printf("Exec finished with error: %v", err)
	}
}

func sendWSError(conn *websocket.Conn, msg string) {
	errMsg := TerminalMessage{Type: "error", Data: msg}
	data, _ := json.Marshal(errMsg)
	conn.WriteMessage(websocket.TextMessage, data)
}
