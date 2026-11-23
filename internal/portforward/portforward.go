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

// Owner identifies the subsystem that owns a metrics forward.
type Owner = string

const (
	OwnerPrometheus Owner = "prometheus"
	OwnerTraffic    Owner = "traffic"
	OwnerCost       Owner = "cost"
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
	mu       sync.Mutex
	client   kubernetes.Interface
	config   *rest.Config
	forwards map[*forward]struct{}
	lifetime context.Context
	cancel   context.CancelFunc
	run      func(context.Context, kubernetes.Interface, *rest.Config, string, string, int, int, chan struct{}, chan struct{}) error
}

func NewManager(client kubernetes.Interface, config *rest.Config) *Manager {
	lifetime, cancel := context.WithCancel(context.Background())
	return &Manager{lifetime: lifetime, cancel: cancel, client: client, config: config, forwards: make(map[*forward]struct{}), run: pfpkg.RunPortForward}
}

type forward struct {
	manager    *Manager
	owner      Owner
	info       ConnectionInfo
	targetPort int
	owners     int
	// established wakes callers sharing an in-flight Start for the same target.
	established chan struct{}
	active      bool
	stopCh      chan struct{}
}

func (f *forward) matches(namespace, service string, port int, contextName string) bool {
	return f.info.Namespace == namespace && f.info.ServiceName == service && f.targetPort == port && f.info.ContextName == contextName
}

// Handle identifies one tunnel. Each owning Start releases its own reference.
// Service lookups borrow the tunnel without prolonging its lifetime.
type Handle struct {
	f        *forward
	borrowed bool
	released bool // guarded by the manager
}

func (h *Handle) SameForward(other *Handle) bool {
	return h != nil && other != nil && (h == other || h.f != nil && h.f == other.f)
}

func (h *Handle) Address() string {
	if h == nil || h.f == nil {
		return ""
	}
	return h.f.info.Address
}

// Replace releases the old handle when adopting another. Rediscovering the
// same tunnel through a service lookup must keep an existing owning handle.
func (h *Handle) Replace(next *Handle) *Handle {
	if h == next || (next != nil && next.borrowed && h.SameForward(next)) {
		return h
	}
	h.Stop()
	return next
}

func (h *Handle) Alive() bool {
	if h == nil || h.f == nil {
		return false
	}
	m := h.f.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	return !h.released && h.f.active
}

func (h *Handle) Info() *ConnectionInfo {
	if !h.Alive() {
		return &ConnectionInfo{}
	}
	info := h.f.info
	return &info
}

func (h *Handle) Stop() bool {
	if h == nil || h.f == nil || h.borrowed {
		return false
	}
	f := h.f
	m := f.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.released || !f.active {
		return false
	}
	h.released = true
	f.owners--
	if f.owners == 0 {
		m.removeLocked(f)
	}
	return true
}

func (m *Manager) removeLocked(f *forward) {
	delete(m.forwards, f)
	f.active = false
	close(f.stopCh)
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
	for f := range m.forwards {
		m.removeLocked(f)
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

func (m *Manager) Start(owner Owner, ctx context.Context, namespace, service string, targetPort int, contextName string) (*Handle, error) {
	if AutoDisabled() {
		return nil, ErrAutoDisabled
	}
	if m == nil {
		return nil, fmt.Errorf("K8s client not initialized")
	}
	for {
		m.mu.Lock()
		if m.Closed() {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		var pending *forward
		for f := range m.forwards {
			if f.owner == owner && f.matches(namespace, service, targetPort, contextName) {
				if f.active {
					f.owners++
					m.mu.Unlock()
					return &Handle{f: f}, nil
				}
				pending = f
				break
			}
		}
		if pending == nil {
			break
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.stopCh:
		case <-pending.established:
		}
	}
	if m.client == nil || m.config == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("K8s client not initialized")
	}
	f := &forward{manager: m, owner: owner, targetPort: targetPort, owners: 1,
		info:        ConnectionInfo{Namespace: namespace, ServiceName: service, ContextName: contextName},
		established: make(chan struct{}), stopCh: make(chan struct{})}
	m.forwards[f] = struct{}{}
	m.mu.Unlock()
	defer close(f.established)
	handle, err := m.establish(ctx, f)
	if err != nil && m.Closed() {
		return nil, ErrClosed
	}
	return handle, err
}

func (m *Manager) establish(ctx context.Context, f *forward) (*Handle, error) {
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, present := m.forwards[f]; present && !f.active {
			m.removeLocked(f)
		}
	}()
	ns, svc := f.info.Namespace, f.info.ServiceName
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
		err := m.run(m.lifetime, m.client, m.config, ns, pod, port, f.targetPort, f.stopCh, ready)
		m.mu.Lock()
		lost := f.active
		if _, present := m.forwards[f]; present {
			m.removeLocked(f)
		}
		m.mu.Unlock()
		ended <- err
		if lost {
			log.Printf("[portforward] Lost: %s -> %s/%s (owner=%s): %v", f.info.Address, ns, svc, f.owner, err)
			if hook := onForwardLost.Load(); hook != nil {
				(*hook)(&Handle{f: f, borrowed: true})
			}
		}
	}()
	select {
	case <-ready:
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, present := m.forwards[f]; !present {
			return nil, fmt.Errorf("port-forward closed during establishment")
		}
		f.info.LocalPort, f.info.Address, f.info.Connected = port, fmt.Sprintf("http://localhost:%d", port), true
		f.active = true
		log.Printf("[portforward] Ready: %s -> %s/%s:%d (owner=%s, context: %s)", f.info.Address, ns, svc, f.targetPort, f.owner, f.info.ContextName)
		return &Handle{f: f}, nil
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

// GetAddressForService prefers the caller's own tunnel. Only the requested
// Service can supply the backend's metrics, even if a peer also speaks PromQL.
func (m *Manager) GetAddressForService(prefer Owner, contextName, namespace, service string) *Handle {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var match *forward
	for f := range m.forwards {
		if f.active && f.info.ContextName == contextName && f.info.Namespace == namespace && f.info.ServiceName == service {
			match = f
			if f.owner == prefer {
				break
			}
		}
	}
	if match == nil {
		return nil
	}
	return &Handle{f: match, borrowed: true}
}

func (m *Manager) IsActiveAddress(address string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for f := range m.forwards {
		if f.active && f.info.Address == address {
			return true
		}
	}
	return false
}

func (m *Manager) IsConnectedForContext(contextName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for f := range m.forwards {
		if f.active && f.info.ContextName == contextName {
			return true
		}
	}
	return false
}
