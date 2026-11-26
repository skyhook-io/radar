// Package portforward provides shared metrics port-forwarding infrastructure.
// It is used by both the traffic package (for Caretta/Hubble) and the prometheus
// package (for resource metrics), breaking what would otherwise be an import cycle.
package portforward

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pfpkg "github.com/skyhook-io/radar/pkg/portforward"
)

// Owner labels the subsystem in tunnel diagnostics.
type Owner = string

const (
	OwnerPrometheus Owner = "prometheus"
	OwnerTraffic    Owner = "traffic"
	OwnerCost       Owner = "cost"
	OwnerArgoCD     Owner = "argocd"
	// EstablishTimeout lets compound discovery flows budget for each managed tunnel attempt.
	EstablishTimeout = 10 * time.Second
)

// ErrAutoDisabled is returned instead of opening a forward when the operator
// turned automatic port-forwarding off (--disable-auto-port-forward).
var ErrAutoDisabled = errors.New("automatic port-forwarding is turned off")

type managerError string

func (err managerError) Error() string { return string(err) }

// ErrClosed ends work on a connection that is being torn down.
const ErrClosed managerError = "port-forward manager closed"

var autoDisabled atomic.Bool

// SetAutoDisabled turns automatic port-forwarding off or on for the process.
// It covers every forward Radar opens on its own: the owners in this package
// and Argo CD discovery. Port forwards a user starts are not affected.
func SetAutoDisabled(disabled bool) { autoDisabled.Store(disabled) }

// AutoDisabled reports whether automatic port-forwarding is turned off.
func AutoDisabled() bool { return autoDisabled.Load() }

// ConnectionInfo contains info about the metrics connection
type ConnectionInfo struct {
	Connected   bool   `json:"connected"`
	LocalPort   int    `json:"localPort,omitempty"`
	Address     string `json:"address,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	ContextName string `json:"contextName,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Manager owns all automatic tunnels created with one connection's clients.
type Manager struct {
	mu          sync.Mutex
	client      kubernetes.Interface
	config      *rest.Config
	contextName string
	forwards    map[*Handle]struct{}
	lifetime    context.Context
	cancel      context.CancelFunc
	run         func(context.Context, kubernetes.Interface, *rest.Config, string, string, int, int, chan struct{}, chan struct{}) error
}

func NewManager(client kubernetes.Interface, config *rest.Config, contextName string) *Manager {
	lifetime, cancel := context.WithCancel(context.Background())
	return &Manager{lifetime: lifetime, cancel: cancel, client: client, config: config, contextName: contextName, forwards: make(map[*Handle]struct{}), run: pfpkg.RunPortForward}
}

// Handle owns one tunnel, which the caller releases with Stop.
type Handle struct {
	manager *Manager
	owner   Owner
	info    ConnectionInfo
	active  bool // guarded by the manager
	stopCh  chan struct{}
}

func (h *Handle) Address() string {
	if h == nil {
		return ""
	}
	return h.info.Address
}

func (h *Handle) Alive() bool {
	if h == nil || h.manager == nil {
		return false
	}
	m := h.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	return h.active
}

func (h *Handle) Info() *ConnectionInfo {
	if !h.Alive() {
		return &ConnectionInfo{}
	}
	info := h.info
	return &info
}

var closedDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

// Done closes when the tunnel is removed, including deliberate connection teardown.
func (h *Handle) Done() <-chan struct{} {
	if h == nil || h.stopCh == nil {
		return closedDone
	}
	return h.stopCh
}

func (h *Handle) Stop() bool {
	if h == nil || h.manager == nil {
		return false
	}
	m := h.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if !h.active {
		return false
	}
	m.removeLocked(h)
	return true
}

func (m *Manager) removeLocked(h *Handle) {
	delete(m.forwards, h)
	h.active = false
	close(h.stopCh)
}

func (m *Manager) Closed() bool {
	return m != nil && m.lifetime.Err() != nil
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel()
	for h := range m.forwards {
		m.removeLocked(h)
	}
}

// The subscriber follows the global integration; the handle identifies the
// lost tunnel even if its local port has since been reassigned.
var onForwardLost atomic.Pointer[func(*Handle)]

func SetOnForwardLost(fn func(*Handle)) {
	if fn == nil {
		onForwardLost.Store(nil)
	} else {
		onForwardLost.Store(&fn)
	}
}

func (m *Manager) Start(owner Owner, ctx context.Context, namespace, service string, targetPort int) (*Handle, error) {
	if AutoDisabled() {
		return nil, ErrAutoDisabled
	}
	if m == nil {
		return nil, fmt.Errorf("K8s client not initialized")
	}
	m.mu.Lock()
	if m.Closed() {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.client == nil || m.config == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("K8s client not initialized")
	}
	h := &Handle{
		manager: m,
		owner:   owner,
		info:    ConnectionInfo{Namespace: namespace, ServiceName: service, ContextName: m.contextName},
		stopCh:  make(chan struct{}),
	}
	m.forwards[h] = struct{}{}
	m.mu.Unlock()
	handle, err := m.establish(ctx, h, targetPort)
	if err != nil && m.Closed() {
		return nil, ErrClosed
	}
	return handle, err
}

func (m *Manager) establish(ctx context.Context, h *Handle, targetPort int) (*Handle, error) {
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, present := m.forwards[h]; present && !h.active {
			m.removeLocked(h)
		}
	}()
	ns, svc := h.info.Namespace, h.info.ServiceName
	lookup, cancel := context.WithCancel(ctx)
	detach := context.AfterFunc(m.lifetime, cancel)
	pod, err := pfpkg.FindPodForService(lookup, m.client, ns, svc)
	detach()
	cancel()
	if err != nil {
		return nil, fmt.Errorf("failed to find pod for service %s: %w", svc, err)
	}
	port, err := pfpkg.FindFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	ready := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		err := m.run(m.lifetime, m.client, m.config, ns, pod, port, targetPort, h.stopCh, ready)
		m.mu.Lock()
		lost := h.active
		if _, present := m.forwards[h]; present {
			m.removeLocked(h)
		}
		m.mu.Unlock()
		ended <- err
		if lost {
			log.Printf("[portforward] Lost: %s -> %s/%s (owner=%s): %v", h.Address(), ns, svc, h.owner, err)
			if hook := onForwardLost.Load(); hook != nil {
				(*hook)(h)
			}
		}
	}()
	select {
	case <-ready:
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, present := m.forwards[h]; !present {
			return nil, fmt.Errorf("port-forward closed during establishment")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h.info.LocalPort, h.info.Address, h.info.Connected = port, fmt.Sprintf("http://localhost:%d", port), true
		h.active = true
		log.Printf("[portforward] Ready: %s -> %s/%s:%d (owner=%s, context: %s)", h.Address(), ns, svc, targetPort, h.owner, h.info.ContextName)
		return h, nil
	case err := <-ended:
		return nil, fmt.Errorf("port-forward failed: %w", err)
	case <-m.lifetime.Done():
		return nil, ErrClosed
	case <-time.After(EstablishTimeout):
		return nil, fmt.Errorf("port-forward timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
