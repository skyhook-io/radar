package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
)

// WorkerSession represents a supervised background Radar process for a specific cluster context.
type WorkerSession struct {
	ContextName string    `json:"context"`
	Port        int       `json:"port"`
	PID         int       `json:"pid"`
	Status      string    `json:"status"` // "active", "starting", "stopped"
	LastAccess  time.Time `json:"lastAccess"`
	CreatedAt   time.Time `json:"createdAt"`
	cmd         *exec.Cmd
	proxy       *httputil.ReverseProxy
	mu          sync.Mutex
}

// SessionInfo provides cluster session details for the Control Center.
type SessionInfo struct {
	ContextName string    `json:"context"`
	IsCurrent   bool      `json:"isCurrent"`
	IsPrimary   bool      `json:"isPrimary"`
	Status      string    `json:"status"` // "active", "sleeping"
	Port        int       `json:"port,omitempty"`
	PID         int       `json:"pid,omitempty"`
	LastAccess  time.Time `json:"lastAccess,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
}

// ClusterSupervisor dynamically spawns, reverse-proxies, and manages the lifecycle
// of per-cluster Radar worker instances, enabling simultaneous multi-cluster sessions.
type ClusterSupervisor struct {
	mu             sync.RWMutex
	workers        map[string]*WorkerSession
	primaryCtx     string
	kubeconfig     string
	kubeconfigDirs []string
	exePath        string
	stopCh         chan struct{}
}

// NewClusterSupervisor initializes a new ClusterSupervisor.
func NewClusterSupervisor(primaryCtx, kubeconfig string, kubeconfigDirs []string) *ClusterSupervisor {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[supervisor] warning: failed to resolve executable path: %v", err)
	}

	cs := &ClusterSupervisor{
		workers:        make(map[string]*WorkerSession),
		primaryCtx:     primaryCtx,
		kubeconfig:     kubeconfig,
		kubeconfigDirs: kubeconfigDirs,
		exePath:        exe,
		stopCh:         make(chan struct{}),
	}

	// Auto-idle background workers after 15 minutes of inactivity
	cs.startAutoIdle(15 * time.Minute)
	return cs
}

// GetOrCreateWorker retrieves an existing worker for contextName or launches a new one.
func (cs *ClusterSupervisor) GetOrCreateWorker(ctx context.Context, contextName string) (*WorkerSession, error) {
	cs.mu.Lock()
	if worker, exists := cs.workers[contextName]; exists && worker.Status == "active" {
		worker.mu.Lock()
		worker.LastAccess = time.Now()
		worker.mu.Unlock()
		cs.mu.Unlock()
		return worker, nil
	}
	cs.mu.Unlock()

	// Verify that the context exists in kubeconfig
	contexts, err := k8s.GetAvailableContexts()
	if err == nil {
		found := false
		for _, c := range contexts {
			if c.Name == contextName {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("context %q not found in available contexts", contextName)
		}
	}

	port, err := findFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate free port: %w", err)
	}

	worker := &WorkerSession{
		ContextName: contextName,
		Port:        port,
		Status:      "starting",
		CreatedAt:   time.Now(),
		LastAccess:  time.Now(),
	}

	cs.mu.Lock()
	cs.workers[contextName] = worker
	cs.mu.Unlock()

	args := []string{
		fmt.Sprintf("--port=%d", port),
		fmt.Sprintf("--context=%s", contextName),
		fmt.Sprintf("--base-path=/c/%s", contextName),
		"--no-browser",
		"--no-version-check",
		"--worker",
	}
	if cs.kubeconfig != "" {
		args = append(args, fmt.Sprintf("--kubeconfig=%s", cs.kubeconfig))
	}
	if len(cs.kubeconfigDirs) > 0 {
		args = append(args, fmt.Sprintf("--kubeconfig-dir=%s", strings.Join(cs.kubeconfigDirs, ",")))
	}

	cmd := exec.Command(cs.exePath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cs.mu.Lock()
		delete(cs.workers, contextName)
		cs.mu.Unlock()
		return nil, fmt.Errorf("failed to start worker process for %s: %w", contextName, err)
	}

	worker.PID = cmd.Process.Pid
	worker.cmd = cmd

	// Wait for worker readiness (up to 6s)
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/c/%s/api/connection", port, contextName)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ready := false
	for i := 0; i < 60; i++ {
		time.Sleep(100 * time.Millisecond)
		resp, err := client.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
	}

	if !ready {
		_ = cmd.Process.Kill()
		cs.mu.Lock()
		delete(cs.workers, contextName)
		cs.mu.Unlock()
		return nil, fmt.Errorf("worker for %s timed out waiting for readiness", contextName)
	}

	workerURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(workerURL)
	proxy.FlushInterval = -1 // Flush immediately for SSE streams

	worker.mu.Lock()
	worker.proxy = proxy
	worker.Status = "active"
	worker.mu.Unlock()

	// Monitor child process termination
	go func() {
		_ = cmd.Wait()
		cs.mu.Lock()
		if cs.workers[contextName] == worker {
			delete(cs.workers, contextName)
		}
		cs.mu.Unlock()
		log.Printf("[supervisor] worker for context %q (PID %d) stopped", contextName, worker.PID)
	}()

	log.Printf("[supervisor] started worker for context %q on port %d (PID %d)", contextName, port, worker.PID)
	return worker, nil
}

// ServeHTTP proxies incoming requests to the appropriate worker.
func (cs *ClusterSupervisor) ServeHTTP(w http.ResponseWriter, r *http.Request, contextName string) {
	worker, err := cs.GetOrCreateWorker(r.Context(), contextName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Cluster session %q unavailable: %v", contextName, err), http.StatusServiceUnavailable)
		return
	}

	worker.mu.Lock()
	worker.LastAccess = time.Now()
	proxy := worker.proxy
	worker.mu.Unlock()

	proxy.ServeHTTP(w, r)
}

// ReleaseWorker terminates a background worker process, instantly releasing all its resources.
func (cs *ClusterSupervisor) ReleaseWorker(contextName string) bool {
	cs.mu.Lock()
	worker, exists := cs.workers[contextName]
	if !exists {
		cs.mu.Unlock()
		return false
	}
	delete(cs.workers, contextName)
	cs.mu.Unlock()

	if worker.cmd != nil && worker.cmd.Process != nil {
		log.Printf("[supervisor] releasing worker for context %q (PID %d)", contextName, worker.PID)
		_ = worker.cmd.Process.Kill()
	}
	return true
}

// ReleaseAll terminates all active background workers.
func (cs *ClusterSupervisor) ReleaseAll() int {
	cs.mu.Lock()
	list := make([]*WorkerSession, 0, len(cs.workers))
	for _, w := range cs.workers {
		list = append(list, w)
	}
	cs.workers = make(map[string]*WorkerSession)
	cs.mu.Unlock()

	count := 0
	for _, w := range list {
		if w.cmd != nil && w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
			count++
		}
	}
	return count
}

// GetSessions returns the current list of sessions for the Control Center.
func (cs *ClusterSupervisor) GetSessions() []SessionInfo {
	available, _ := k8s.GetAvailableContexts()
	currentCtx := k8s.GetContextName()

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var sessions []SessionInfo
	seen := make(map[string]bool)

	// Primary session (the parent process itself)
	sessions = append(sessions, SessionInfo{
		ContextName: cs.primaryCtx,
		IsCurrent:   cs.primaryCtx == currentCtx,
		IsPrimary:   true,
		Status:      "active",
		PID:         os.Getpid(),
	})
	seen[cs.primaryCtx] = true

	// Active workers
	for name, w := range cs.workers {
		if seen[name] {
			continue
		}
		sessions = append(sessions, SessionInfo{
			ContextName: name,
			IsCurrent:   name == currentCtx,
			IsPrimary:   false,
			Status:      w.Status,
			Port:        w.Port,
			PID:         w.PID,
			LastAccess:  w.LastAccess,
			CreatedAt:   w.CreatedAt,
		})
		seen[name] = true
	}

	// Sleeping/available contexts
	for _, ctx := range available {
		if seen[ctx.Name] {
			continue
		}
		sessions = append(sessions, SessionInfo{
			ContextName: ctx.Name,
			IsCurrent:   ctx.Name == currentCtx,
			IsPrimary:   false,
			Status:      "sleeping",
		})
		seen[ctx.Name] = true
	}

	return sessions
}

func (cs *ClusterSupervisor) startAutoIdle(idleTimeout time.Duration) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-cs.stopCh:
				ticker.Stop()
				return
			case <-ticker.C:
				cs.mu.Lock()
				now := time.Now()
				for name, w := range cs.workers {
					if w.Status == "active" && now.Sub(w.LastAccess) > idleTimeout {
						log.Printf("[supervisor] auto-idling worker for context %q (inactive for %v)", name, now.Sub(w.LastAccess).Round(time.Second))
						if w.cmd != nil && w.cmd.Process != nil {
							_ = w.cmd.Process.Kill()
						}
						delete(cs.workers, name)
					}
				}
				cs.mu.Unlock()
			}
		}
	}()
}

// Shutdown terminates all managed child worker processes cleanly.
func (cs *ClusterSupervisor) Shutdown() {
	select {
	case <-cs.stopCh:
	default:
		close(cs.stopCh)
	}
	cs.ReleaseAll()
}
