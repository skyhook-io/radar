// Package portforward provides shared metrics port-forwarding infrastructure.
// It is used by both the traffic package (for Caretta/Hubble) and the prometheus
// package (for resource metrics), breaking what would otherwise be an import cycle.
//
// The low-level primitives (RunPortForward, FindPodForService, FindFreePort)
// live in pkg/portforward. This package holds one metrics forward per owner —
// prometheus discovery and the traffic subsystem each get their own — so that
// starting or stopping one owner's forward never tears down the other's. (A
// single shared forward previously let them clobber each other whenever they
// wanted different services.) Owners may still read each other's forward
// address (GetAddress) to reuse a compatible endpoint, but only ever stop their
// own.
package portforward

import (
	"context"
	"fmt"
	"log"
	"sync"
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
)

// metricsForward is one owner's active port-forward state.
type metricsForward struct {
	// establishMu serializes Start for this owner. It is held across the
	// port-forward establishment (pod lookup + up to a 10s ready wait) so two
	// concurrent Starts for the same owner can't both bring up a forward and leak
	// one. It is deliberately NOT reg.mu: establishing one owner's forward must
	// not block reads (GetAddress/GetConnectionInfo), Stop, or the other owner's
	// establishment.
	establishMu sync.Mutex

	// epoch bumps on every teardown (Stop, or Start replacing this owner's
	// forward). Start captures it before establishing and refuses to publish if it
	// changed meanwhile — so a Stop that lands while a forward is still coming up
	// (e.g. a context-switch Reset) is not silently lost when readyCh then wins.
	epoch uint64

	active      bool
	localPort   int
	namespace   string
	serviceName string
	podName     string
	targetPort  int
	contextName string // K8s context this forward belongs to

	stopCh chan struct{}
	cancel context.CancelFunc
}

// info builds the ConnectionInfo for an active forward. Caller must hold reg.mu.
func (f *metricsForward) info() *ConnectionInfo {
	return &ConnectionInfo{
		Connected:   true,
		LocalPort:   f.localPort,
		Address:     fmt.Sprintf("http://localhost:%d", f.localPort),
		Namespace:   f.namespace,
		ServiceName: f.serviceName,
		ContextName: f.contextName,
	}
}

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

// registry holds one metrics forward per owner plus the shared K8s clients.
type registry struct {
	mu        sync.RWMutex
	forwards  map[Owner]*metricsForward
	k8sClient kubernetes.Interface
	k8sConfig *rest.Config
}

var reg = &registry{forwards: map[Owner]*metricsForward{}}

// forwardFor returns the owner's forward state, creating an empty one on first use.
// Caller must hold reg.mu.
func forwardFor(owner Owner) *metricsForward {
	f := reg.forwards[owner]
	if f == nil {
		f = &metricsForward{}
		reg.forwards[owner] = f
	}
	return f
}

// SetK8sClients sets the K8s client and config for port-forwarding.
// Must be called before using port-forward features.
func SetK8sClients(client kubernetes.Interface, config *rest.Config) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.k8sClient = client
	reg.k8sConfig = config
}

// Start starts a port-forward to the specified metrics service for the given
// owner. It only replaces that owner's own forward — other owners' forwards are
// left untouched.
func Start(owner Owner, ctx context.Context, namespace, serviceName string, targetPort int, contextName string) (*ConnectionInfo, error) {
	// Fast path + client capture under reg.mu, held only briefly.
	reg.mu.Lock()
	f := forwardFor(owner)
	if f.active && f.namespace == namespace && f.serviceName == serviceName && f.contextName == contextName {
		info := f.info()
		reg.mu.Unlock()
		return info, nil
	}
	client := reg.k8sClient
	config := reg.k8sConfig
	reg.mu.Unlock()

	if client == nil || config == nil {
		return nil, fmt.Errorf("K8s client not initialized")
	}

	// Serialize establishment for THIS owner only. Held across the pod lookup and
	// the up-to-10s ready wait below — but it is not reg.mu, so reads
	// (GetAddress/GetConnectionInfo), Stop, and the other owner's establishment
	// all stay unblocked meanwhile.
	f.establishMu.Lock()
	defer f.establishMu.Unlock()

	// Re-check under reg.mu: a concurrent establish for this owner may have just
	// connected to the same target while we waited on establishMu.
	reg.mu.Lock()
	if f.active && f.namespace == namespace && f.serviceName == serviceName && f.contextName == contextName {
		info := f.info()
		reg.mu.Unlock()
		return info, nil
	}
	stopForwardLocked(f) // replace only this owner's existing forward
	startEpoch := f.epoch
	reg.mu.Unlock()

	// Establish the forward WITHOUT holding reg.mu.
	podName, err := findPodForService(ctx, client, namespace, serviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to find pod for service %s: %w", serviceName, err)
	}
	localPort, err := findFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	stopCh := make(chan struct{})
	pfCtx, cancel := context.WithCancel(context.Background())
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		err := runPortForward(pfCtx, client, config, namespace, podName, localPort, targetPort, stopCh, readyCh)
		if err != nil {
			errCh <- err
		}
		close(errCh)

		reg.mu.Lock()
		if f.podName == podName && f.localPort == localPort {
			f.active = false
		}
		reg.mu.Unlock()
	}()

	// teardown stops the just-launched forward when establishment fails. The
	// forward was never committed to f (only the ready path publishes it), so the
	// goroutine's own cleanup is a no-op and there is nothing to unpublish.
	teardown := func() {
		cancel()
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	}

	select {
	case <-readyCh:
		reg.mu.Lock()
		// A Stop (or a replacing Start) that landed while we were establishing
		// bumped the epoch. Honor it: tear this forward down instead of publishing
		// a connection the caller already asked to stop (e.g. after a context
		// switch), which would otherwise report the wrong cluster as connected.
		if f.epoch != startEpoch {
			reg.mu.Unlock()
			teardown()
			return nil, fmt.Errorf("port-forward superseded during establishment")
		}
		f.active = true
		f.localPort = localPort
		f.namespace = namespace
		f.serviceName = serviceName
		f.podName = podName
		f.targetPort = targetPort
		f.contextName = contextName
		f.stopCh = stopCh
		f.cancel = cancel
		info := f.info()
		reg.mu.Unlock()
		log.Printf("[portforward] Ready: localhost:%d -> %s/%s:%d (owner=%s, context: %s)",
			localPort, namespace, serviceName, targetPort, owner, contextName)
		return info, nil

	case err := <-errCh:
		teardown()
		return nil, fmt.Errorf("port-forward failed: %w", err)

	case <-time.After(10 * time.Second):
		teardown()
		return nil, fmt.Errorf("port-forward timed out")

	case <-ctx.Done():
		teardown()
		return nil, ctx.Err()
	}
}

// Stop stops the given owner's metrics port-forward, if any.
func Stop(owner Owner) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if f := reg.forwards[owner]; f != nil {
		stopForwardLocked(f)
	}
}

// stopForwardLocked stops one owner's forward (caller must hold reg.mu).
func stopForwardLocked(f *metricsForward) {
	if f == nil {
		return
	}
	// Bump before the active check so a Stop that lands while a forward is still
	// establishing (not yet active) still invalidates it — the in-flight Start
	// checks this epoch before publishing.
	f.epoch++
	if !f.active {
		return
	}

	log.Printf("[portforward] Stopping: localhost:%d -> %s/%s", f.localPort, f.namespace, f.serviceName)

	if f.cancel != nil {
		f.cancel()
	}
	if f.stopCh != nil {
		select {
		case <-f.stopCh:
			// Already closed
		default:
			close(f.stopCh)
		}
	}

	f.active = false
	f.localPort = 0
	f.namespace = ""
	f.serviceName = ""
	f.podName = ""
	f.targetPort = 0
	f.contextName = ""
	f.stopCh = nil
	f.cancel = nil
}

// GetAddress returns the address of an active metrics forward for the given
// context, preferring the caller's own forward (preferOwner) before falling back
// to another owner's compatible endpoint (read-only reuse — the caller never
// takes ownership). Preferring the caller's own forward keeps selection
// deterministic and avoids probing an incompatible peer (e.g. Hubble's relay)
// while the caller's own forward is still live. Empty if none.
func GetAddress(preferOwner Owner, currentContext string) string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if f := reg.forwards[preferOwner]; f != nil && f.active && f.contextName == currentContext {
		return fmt.Sprintf("http://localhost:%d", f.localPort)
	}
	for owner, f := range reg.forwards {
		if owner != preferOwner && f.active && f.contextName == currentContext {
			return fmt.Sprintf("http://localhost:%d", f.localPort)
		}
	}
	return ""
}

// GetConnectionInfo returns the given owner's connection info.
func GetConnectionInfo(owner Owner) *ConnectionInfo {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	f := reg.forwards[owner]
	if f == nil || !f.active {
		return &ConnectionInfo{Connected: false}
	}

	return &ConnectionInfo{
		Connected:   true,
		LocalPort:   f.localPort,
		Address:     fmt.Sprintf("http://localhost:%d", f.localPort),
		Namespace:   f.namespace,
		ServiceName: f.serviceName,
		ContextName: f.contextName,
	}
}

// IsConnectedForContext reports whether any owner has an active forward for the context.
func IsConnectedForContext(contextName string) bool {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	for _, f := range reg.forwards {
		if f.active && f.contextName == contextName {
			return true
		}
	}
	return false
}

func runPortForward(ctx context.Context, client kubernetes.Interface, config *rest.Config,
	namespace, podName string, localPort, targetPort int, stopCh chan struct{}, readyCh chan struct{},
) error {
	return pfpkg.RunPortForward(ctx, client, config, namespace, podName, localPort, targetPort, stopCh, readyCh)
}

func findPodForService(ctx context.Context, client kubernetes.Interface, namespace, serviceName string) (string, error) {
	return pfpkg.FindPodForService(ctx, client, namespace, serviceName)
}

func findFreePort() (int, error) {
	return pfpkg.FindFreePort()
}
