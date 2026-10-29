package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/skyhook-io/radar/internal/k8s"
)

// WorkerLaunchConfig defines operator restrictions and configuration inherited by child workers.
type WorkerLaunchConfig struct {
	KubeconfigPath       string
	KubeconfigDirs       []string
	DisableExec          bool
	DisableLocalTerminal bool
	DisableHelmWrite     bool
	PodShellDefault      string
	Namespace            string
	Namespaces           []string
	NamespaceScope       bool
	TimelineStorage      string
	TimelineDBPath       string
	TimelineRetention    time.Duration
	TimelineMaxSizeBytes int64
	NoVersionCheck       bool
	DevMode              bool
}

// WorkerSession represents a supervised background Radar process for a specific cluster context.
type WorkerSession struct {
	ContextName string    `json:"context"`
	Slug        string    `json:"slug"`
	Port        int       `json:"port"`
	PID         int       `json:"pid"`
	Status      string    `json:"status"` // "active", "starting", "stopped"
	LastAccess  time.Time `json:"lastAccess"`
	CreatedAt   time.Time `json:"createdAt"`

	inFlight    atomic.Int64
	ready       chan struct{}
	startErr    error
	exited      chan struct{}
	cancelStart context.CancelFunc

	cmd   *exec.Cmd
	proxy *httputil.ReverseProxy
	mu    sync.Mutex
}

func (w *WorkerSession) touch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.LastAccess = time.Now()
}

func (w *WorkerSession) getLastAccess() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.LastAccess
}

func (w *WorkerSession) getStatus() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Status
}

func (w *WorkerSession) setStatus(status string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Status = status
}

// SessionInfo provides cluster session details for the Control Center.
type SessionInfo struct {
	ContextName string     `json:"context"`
	Slug        string     `json:"slug"`
	IsCurrent   bool       `json:"isCurrent"`
	IsPrimary   bool       `json:"isPrimary"`
	Status      string     `json:"status"` // "active", "sleeping"
	Port        int        `json:"port,omitempty"`
	PID         int        `json:"pid,omitempty"`
	LastAccess  *time.Time `json:"lastAccess,omitempty"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
}

// ClusterSupervisor dynamically spawns, reverse-proxies, and manages the lifecycle
// of per-cluster Radar worker instances, enabling simultaneous multi-cluster sessions.
type ClusterSupervisor struct {
	mu            sync.RWMutex
	workers       map[string]*WorkerSession // keyed by slug
	slugToContext map[string]string
	contextToSlug map[string]string
	primaryCtx    string
	launchConfig  *WorkerLaunchConfig
	exePath       string
	stopCh        chan struct{}
}

// ContextSlug computes a stable, URL-safe identifier for a Kubernetes context name.
func ContextSlug(contextName string) string {
	hash := sha256.Sum256([]byte(contextName))
	hexHash := hex.EncodeToString(hash[:])[:8]

	var sb strings.Builder
	lastHyphen := false
	for _, r := range contextName {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			lastHyphen = false
		} else if r >= 'A' && r <= 'Z' {
			sb.WriteRune(unicode.ToLower(r))
			lastHyphen = false
		} else {
			if !lastHyphen {
				sb.WriteRune('-')
				lastHyphen = true
			}
		}
	}
	clean := strings.Trim(sb.String(), "-")
	if len(clean) > 20 {
		clean = clean[:20]
	}
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "cluster"
	}
	return clean + "-" + hexHash
}

// NewClusterSupervisor initializes a new ClusterSupervisor.
func NewClusterSupervisor(primaryCtx string, launchConfig *WorkerLaunchConfig) *ClusterSupervisor {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[supervisor] warning: failed to resolve executable path: %v", err)
	}

	cs := &ClusterSupervisor{
		workers:       make(map[string]*WorkerSession),
		slugToContext: make(map[string]string),
		contextToSlug: make(map[string]string),
		primaryCtx:    primaryCtx,
		launchConfig:  launchConfig,
		exePath:       exe,
		stopCh:        make(chan struct{}),
	}

	cs.registerSlug(primaryCtx)
	// Auto-idle background workers after 15 minutes of inactivity
	cs.startAutoIdle(15 * time.Minute)
	return cs
}

func (cs *ClusterSupervisor) registerSlug(contextName string) string {
	if slug, ok := cs.contextToSlug[contextName]; ok {
		return slug
	}
	slug := ContextSlug(contextName)
	cs.contextToSlug[contextName] = slug
	cs.slugToContext[slug] = contextName
	return slug
}

// SlugForContext returns the URL slug for a given context name.
func (cs *ClusterSupervisor) SlugForContext(contextName string) string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.registerSlug(contextName)
}

// ContextForSlug resolves a slug back to its original context name.
func (cs *ClusterSupervisor) ContextForSlug(slug string) (string, bool) {
	cs.mu.RLock()
	name, ok := cs.slugToContext[slug]
	cs.mu.RUnlock()
	if ok {
		return name, true
	}

	// Try resolving from available contexts
	contexts, err := k8s.GetAvailableContexts()
	if err == nil {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		for _, c := range contexts {
			computed := cs.registerSlug(c.Name)
			if computed == slug {
				return c.Name, true
			}
		}
	}
	return "", false
}

// SetPrimaryContext updates the recorded primary context and terminates any redundant worker.
func (cs *ClusterSupervisor) SetPrimaryContext(newPrimary string) {
	cs.mu.Lock()
	cs.primaryCtx = newPrimary
	slug := cs.registerSlug(newPrimary)
	worker, hasWorker := cs.workers[slug]
	if hasWorker {
		delete(cs.workers, slug)
	}
	cs.mu.Unlock()

	if hasWorker && worker != nil {
		log.Printf("[supervisor] terminating redundant worker for newly primary context %q", newPrimary)
		if worker.cancelStart != nil {
			worker.cancelStart()
		}
		if worker.cmd != nil && worker.cmd.Process != nil {
			_ = worker.cmd.Process.Kill()
		}
	}
}

// IsPrimary returns true if contextName or slug corresponds to the primary session.
func (cs *ClusterSupervisor) IsPrimary(identifier string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	currentPrimary := cs.primaryCtx
	if identifier == currentPrimary {
		return true
	}
	if ctxName, ok := cs.slugToContext[identifier]; ok && ctxName == currentPrimary {
		return true
	}
	return false
}

// GetOrCreateWorker retrieves an existing worker for contextName/slug or launches a new one.
func (cs *ClusterSupervisor) GetOrCreateWorker(ctx context.Context, identifier string) (*WorkerSession, error) {
	// First resolve context name and slug
	var contextName, slug string
	if realName, ok := cs.ContextForSlug(identifier); ok {
		contextName = realName
		slug = identifier
	} else {
		contextName = identifier
		slug = cs.SlugForContext(contextName)
	}

	if cs.IsPrimary(contextName) {
		return nil, fmt.Errorf("context %q is already served by the primary session", contextName)
	}

	cs.mu.Lock()
	if worker, exists := cs.workers[slug]; exists {
		status := worker.getStatus()
		if status == "active" {
			worker.touch()
			cs.mu.Unlock()
			return worker, nil
		}
		if status == "starting" {
			readyCh := worker.ready
			cs.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-readyCh:
				if worker.startErr != nil {
					return nil, worker.startErr
				}
				if worker.getStatus() == "active" {
					worker.touch()
					return worker, nil
				}
				return nil, fmt.Errorf("worker for %s stopped during startup", contextName)
			}
		}
		// If previously stopped, clean it up
		delete(cs.workers, slug)
	}

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
			cs.mu.Unlock()
			return nil, fmt.Errorf("context %q not found in available contexts", contextName)
		}
	}

	port, err := findFreePort()
	if err != nil {
		cs.mu.Unlock()
		return nil, fmt.Errorf("failed to allocate free port: %w", err)
	}

	startCtx, cancelStart := context.WithCancel(context.Background())
	worker := &WorkerSession{
		ContextName: contextName,
		Slug:        slug,
		Port:        port,
		Status:      "starting",
		CreatedAt:   time.Now(),
		LastAccess:  time.Now(),
		ready:       make(chan struct{}),
		exited:      make(chan struct{}),
		cancelStart: cancelStart,
	}

	cs.workers[slug] = worker
	cs.mu.Unlock()

	args := []string{
		fmt.Sprintf("--port=%d", port),
		fmt.Sprintf("--context=%s", contextName),
		fmt.Sprintf("--base-path=/c/%s", slug),
		"--no-browser",
		"--worker",
	}

	if cs.launchConfig != nil {
		cfg := cs.launchConfig
		if cfg.KubeconfigPath != "" {
			args = append(args, fmt.Sprintf("--kubeconfig=%s", cfg.KubeconfigPath))
		}
		if len(cfg.KubeconfigDirs) > 0 {
			args = append(args, fmt.Sprintf("--kubeconfig-dir=%s", strings.Join(cfg.KubeconfigDirs, ",")))
		}
		if cfg.DisableExec {
			args = append(args, "--disable-exec")
		}
		if cfg.DisableLocalTerminal {
			args = append(args, "--disable-local-terminal")
		}
		if cfg.DisableHelmWrite {
			args = append(args, "--disable-helm-write")
		}
		if cfg.PodShellDefault != "" {
			args = append(args, fmt.Sprintf("--pod-shell-default=%s", cfg.PodShellDefault))
		}
		if cfg.Namespace != "" {
			args = append(args, fmt.Sprintf("--namespace=%s", cfg.Namespace))
		}
		if len(cfg.Namespaces) > 0 {
			args = append(args, fmt.Sprintf("--namespaces=%s", strings.Join(cfg.Namespaces, ",")))
		}
		if cfg.NamespaceScope {
			args = append(args, "--namespace-scope")
		}
		if cfg.TimelineStorage != "" {
			args = append(args, fmt.Sprintf("--timeline-storage=%s", cfg.TimelineStorage))
		}
		if cfg.TimelineDBPath != "" {
			args = append(args, fmt.Sprintf("--timeline-db=%s", cfg.TimelineDBPath))
		}
		if cfg.TimelineRetention > 0 {
			args = append(args, fmt.Sprintf("--timeline-retention=%s", cfg.TimelineRetention.String()))
		}
		if cfg.TimelineMaxSizeBytes > 0 {
			args = append(args, fmt.Sprintf("--timeline-max-size=%d", cfg.TimelineMaxSizeBytes))
		}
		if cfg.NoVersionCheck {
			args = append(args, "--no-version-check")
		}
		if cfg.DevMode {
			args = append(args, "--dev")
		}
	}

	cmd := exec.Command(cs.exePath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cs.mu.Lock()
		delete(cs.workers, slug)
		cs.mu.Unlock()
		worker.setStatus("stopped")
		worker.startErr = err
		close(worker.ready)
		close(worker.exited)
		return nil, fmt.Errorf("failed to start worker process for %s: %w", contextName, err)
	}

	worker.PID = cmd.Process.Pid
	worker.cmd = cmd

	// Immediately launch process monitor goroutine to ensure child is always reaped
	go func() {
		_ = cmd.Wait()
		close(worker.exited)
		worker.setStatus("stopped")
		cs.mu.Lock()
		if cs.workers[slug] == worker {
			delete(cs.workers, slug)
		}
		cs.mu.Unlock()
		log.Printf("[supervisor] worker for context %q (PID %d) stopped", contextName, worker.PID)
	}()

	// Wait for worker readiness (up to 6s)
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/c/%s/api/connection", port, slug)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	readyDeadline := time.After(6 * time.Second)

	for {
		select {
		case <-startCtx.Done():
			_ = cmd.Process.Kill()
			<-worker.exited
			cs.mu.Lock()
			delete(cs.workers, slug)
			cs.mu.Unlock()
			worker.setStatus("stopped")
			worker.startErr = errors.New("worker startup cancelled")
			close(worker.ready)
			return nil, worker.startErr

		case <-worker.exited:
			cs.mu.Lock()
			delete(cs.workers, slug)
			cs.mu.Unlock()
			worker.setStatus("stopped")
			worker.startErr = fmt.Errorf("worker for %s exited unexpectedly during startup", contextName)
			close(worker.ready)
			return nil, worker.startErr

		case <-readyDeadline:
			_ = cmd.Process.Kill()
			<-worker.exited
			cs.mu.Lock()
			delete(cs.workers, slug)
			cs.mu.Unlock()
			worker.setStatus("stopped")
			worker.startErr = fmt.Errorf("worker for %s timed out waiting for readiness", contextName)
			close(worker.ready)
			return nil, worker.startErr

		case <-ticker.C:
			resp, err := client.Get(healthURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					workerURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
					proxy := httputil.NewSingleHostReverseProxy(workerURL)
					proxy.FlushInterval = -1 // Flush immediately for SSE streams

					worker.mu.Lock()
					worker.proxy = proxy
					worker.Status = "active"
					worker.mu.Unlock()

					close(worker.ready)
					log.Printf("[supervisor] started worker for context %q on port %d (PID %d)", contextName, port, worker.PID)
					return worker, nil
				}
			}
		}
	}
}

// ServeHTTP proxies incoming requests to the appropriate worker.
func (cs *ClusterSupervisor) ServeHTTP(w http.ResponseWriter, r *http.Request, identifier string) {
	worker, err := cs.GetOrCreateWorker(r.Context(), identifier)
	if err != nil {
		http.Error(w, fmt.Sprintf("Cluster session %q unavailable: %v", identifier, err), http.StatusServiceUnavailable)
		return
	}

	worker.inFlight.Add(1)
	defer func() {
		worker.inFlight.Add(-1)
		worker.touch()
	}()

	worker.mu.Lock()
	proxy := worker.proxy
	worker.mu.Unlock()

	if proxy == nil {
		http.Error(w, fmt.Sprintf("Cluster session %q proxy not ready", identifier), http.StatusServiceUnavailable)
		return
	}

	proxy.ServeHTTP(w, r)
}

// ReleaseWorker terminates a background worker process, instantly releasing all its resources.
func (cs *ClusterSupervisor) ReleaseWorker(identifier string) bool {
	var slug string
	if s, ok := cs.ContextForSlug(identifier); ok {
		_ = s
		slug = identifier
	} else {
		slug = cs.SlugForContext(identifier)
	}

	cs.mu.Lock()
	worker, exists := cs.workers[slug]
	if !exists {
		cs.mu.Unlock()
		return false
	}
	delete(cs.workers, slug)
	cs.mu.Unlock()

	cs.terminateWorker(worker)
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

	for _, w := range list {
		cs.terminateWorker(w)
	}
	return len(list)
}

func (cs *ClusterSupervisor) terminateWorker(w *WorkerSession) {
	if w.cancelStart != nil {
		w.cancelStart()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		log.Printf("[supervisor] releasing worker for context %q (PID %d)", w.ContextName, w.PID)
		_ = w.cmd.Process.Kill()
		select {
		case <-w.exited:
		case <-time.After(1 * time.Second):
		}
	}
}

// GetSessions returns the current list of sessions for the Control Center.
func (cs *ClusterSupervisor) GetSessions() []SessionInfo {
	available, _ := k8s.GetAvailableContexts()
	currentCtx := k8s.GetContextName()

	cs.mu.Lock()
	primary := cs.primaryCtx
	primarySlug := cs.registerSlug(primary)

	var sessions []SessionInfo
	seen := make(map[string]bool)

	// Primary session (the parent process itself)
	sessions = append(sessions, SessionInfo{
		ContextName: primary,
		Slug:        primarySlug,
		IsCurrent:   primary == currentCtx,
		IsPrimary:   true,
		Status:      "active",
		PID:         os.Getpid(),
	})
	seen[primary] = true

	// Active and starting workers
	for slug, w := range cs.workers {
		if seen[w.ContextName] {
			continue
		}
		lastAcc := w.getLastAccess()
		createdAt := w.CreatedAt
		sessions = append(sessions, SessionInfo{
			ContextName: w.ContextName,
			Slug:        slug,
			IsCurrent:   w.ContextName == currentCtx,
			IsPrimary:   false,
			Status:      w.getStatus(),
			Port:        w.Port,
			PID:         w.PID,
			LastAccess:  &lastAcc,
			CreatedAt:   &createdAt,
		})
		seen[w.ContextName] = true
	}

	// Sleeping/available contexts
	for _, ctx := range available {
		if seen[ctx.Name] {
			continue
		}
		slug := cs.registerSlug(ctx.Name)
		sessions = append(sessions, SessionInfo{
			ContextName: ctx.Name,
			Slug:        slug,
			IsCurrent:   ctx.Name == currentCtx,
			IsPrimary:   false,
			Status:      "sleeping",
		})
		seen[ctx.Name] = true
	}
	cs.mu.Unlock()

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
				toIdle := make([]*WorkerSession, 0)
				for slug, w := range cs.workers {
					if w.getStatus() == "active" && w.inFlight.Load() == 0 && now.Sub(w.getLastAccess()) > idleTimeout {
						delete(cs.workers, slug)
						toIdle = append(toIdle, w)
					}
				}
				cs.mu.Unlock()

				for _, w := range toIdle {
					log.Printf("[supervisor] auto-idling worker for context %q (PID %d)", w.ContextName, w.PID)
					cs.terminateWorker(w)
				}
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
