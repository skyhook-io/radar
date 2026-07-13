package argocd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	pfpkg "github.com/skyhook-io/radar/pkg/portforward"
)

const serverLabelSelector = "app.kubernetes.io/name=argocd-server"

type candidate struct {
	namespace  string
	name       string
	scheme     string
	port       int
	targetPort int
}

func (c candidate) clusterURL() string {
	return fmt.Sprintf("%s://%s.%s.svc:%d", c.scheme, c.name, c.namespace, c.port)
}

// discoverCandidates lists Services labeled app.kubernetes.io/name=argocd-server
// across all namespaces. Sorted with the conventional "argocd" namespace first
// so multi-install clusters probe the default install before exotic ones.
func discoverCandidates(ctx context.Context, client kubernetes.Interface) ([]candidate, error) {
	svcs, err := client.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: serverLabelSelector,
	})
	if err != nil {
		return nil, err
	}
	var out []candidate
	for i := range svcs.Items {
		if c, ok := pickPort(svcs.Items[i]); ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].namespace == "argocd") != (out[j].namespace == "argocd") {
			return out[i].namespace == "argocd"
		}
		if out[i].namespace != out[j].namespace {
			return out[i].namespace < out[j].namespace
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// pickPort selects the service port to target: https (443) preferred, http
// (80) fallback, else the first declared port.
func pickPort(svc corev1.Service) (candidate, bool) {
	var httpsPort, httpPort *corev1.ServicePort
	for i := range svc.Spec.Ports {
		p := &svc.Spec.Ports[i]
		switch {
		case p.Port == 443 || strings.EqualFold(p.Name, "https"):
			if httpsPort == nil {
				httpsPort = p
			}
		case p.Port == 80 || strings.EqualFold(p.Name, "http"):
			if httpPort == nil {
				httpPort = p
			}
		}
	}

	pick := httpsPort
	scheme := "https"
	if pick == nil {
		pick = httpPort
		scheme = "http"
	}
	if pick == nil {
		if len(svc.Spec.Ports) == 0 {
			return candidate{}, false
		}
		pick = &svc.Spec.Ports[0]
		scheme = "http"
	}

	return candidate{
		namespace:  svc.Namespace,
		name:       svc.Name,
		scheme:     scheme,
		port:       int(pick.Port),
		targetPort: resolveTargetPort(pick),
	}, true
}

// resolveTargetPort returns the container port for port-forwarding, which
// bypasses the Service (e.g., service:443 → container:8080).
func resolveTargetPort(p *corev1.ServicePort) int {
	if p.TargetPort.IntVal > 0 {
		return int(p.TargetPort.IntVal)
	}
	return int(p.Port)
}

// discover finds a reachable Argo CD API server:
//  1. Every candidate at its in-cluster Service address (works in-cluster, or
//     when the user's machine can route to cluster DNS).
//  2. Out-of-cluster only: port-forward candidates in priority order, probing
//     https then http on the forwarded port (argocd-server serves both on one
//     port via cmux).
//
// The port-forward is owned by this package rather than internal/portforward:
// that package manages a single shared forward for the metrics stack
// (Prometheus/traffic), and starting an argocd-server forward through it
// would tear down the active metrics forward.
func (m *Manager) discover(ctx context.Context, snap probeSnapshot) (string, error) {
	// The client is captured in the snapshot, NOT read live — so a context
	// switch that swaps the live client mid-probe can't redirect this token's
	// discovery at a different cluster's argocd-server.
	k8sc := snap.k8sClient
	if k8sc == nil {
		return "", fmt.Errorf("%w: no Kubernetes client available for discovery", ErrUnreachable)
	}

	cands, err := discoverCandidates(ctx, k8sc)
	if err != nil {
		return "", fmt.Errorf("%w: listing argocd-server services: %v", ErrUnreachable, err)
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("%w: no Service labeled %s found in cluster — set an explicit Argo CD URL", ErrUnreachable, serverLabelSelector)
	}

	for _, cand := range cands {
		addr := cand.clusterURL()
		if m.probeEndpoint(ctx, addr, snap) == nil {
			log.Printf("[argocd] Discovered Argo CD at %s (service %s/%s)", addr, cand.namespace, cand.name)
			return addr, nil
		}
	}

	if m.inCluster() {
		return "", fmt.Errorf("%w: argocd-server service found but not reachable in-cluster (if it serves a self-signed certificate, set argoCdInsecureTls)", ErrUnreachable)
	}

	var lastErr error
	for _, cand := range cands {
		log.Printf("[argocd] No candidate reachable in-cluster, starting port-forward to %s/%s...", cand.namespace, cand.name)
		fwd, pfErr := m.startPortForward(ctx, snap, cand.namespace, cand.name, cand.targetPort)
		if pfErr != nil {
			lastErr = fmt.Errorf("port-forward to %s/%s failed: %w", cand.namespace, cand.name, pfErr)
			continue
		}

		connected := ""
		for _, scheme := range []string{"https", "http"} {
			addr := fmt.Sprintf("%s://localhost:%d", scheme, fwd.localPort)
			if m.probeEndpoint(ctx, addr, snap) == nil {
				connected = addr
				break
			}
		}
		if connected != "" {
			m.mu.Lock()
			if m.staleLocked(snap) {
				// A config change (SetConfig/Reset) superseded this probe while
				// we were forwarding. Don't install the forward — the caller
				// will discard the whole result; leaving it would dangle a
				// forward to a target the user has moved away from.
				m.mu.Unlock()
				fwd.stop()
				return "", errStaleProbe
			}
			old := m.forward
			m.forward = fwd
			m.mu.Unlock()
			if old != nil {
				old.stop()
			}
			log.Printf("[argocd] Connected to %s/%s via port-forward at %s", cand.namespace, cand.name, connected)
			return connected, nil
		}

		fwd.stop()
		lastErr = fmt.Errorf("argocd-server at %s/%s not responding after port-forward (if it serves a self-signed certificate, set argoCdInsecureTls)", cand.namespace, cand.name)
	}

	return "", fmt.Errorf("%w: %v", ErrUnreachable, lastErr)
}

type activeForward struct {
	localPort int
	stopCh    chan struct{}
	cancel    context.CancelFunc
	stopOnce  sync.Once
}

func (f *activeForward) stop() {
	f.stopOnce.Do(func() {
		f.cancel()
		close(f.stopCh)
	})
}

// watchForwardExit blocks until a live port-forward's RunPortForward returns
// (the tunnel died or was deliberately stopped), then drops the connection IFF
// this forward is still the installed one. A deliberate stop() (reconnect,
// context switch) already replaced m.forward, so the != guard makes those exits
// a no-op; only an UNEXPECTED death of the current forward drops the connection,
// which flips Get() to not-connected and lets maybeProbeInBackgroundLocked
// re-discover and re-establish it instead of every call failing against a dead
// local port.
func (m *Manager) watchForwardExit(fwd *activeForward, errCh <-chan error) {
	err := <-errCh
	m.mu.Lock()
	if m.forward != fwd {
		m.mu.Unlock()
		return
	}
	dropped := m.dropConnectionLocked()
	m.mu.Unlock()
	if dropped != nil {
		dropped.stop()
	}
	log.Printf("[argocd] Port-forward on localhost:%d exited (%v); dropped connection, will reprobe on next use", fwd.localPort, err)
}

func (m *Manager) startPortForward(ctx context.Context, snap probeSnapshot, namespace, service string, targetPort int) (*activeForward, error) {
	// Use the snapshot's client/config (frozen at probe start), not the live
	// ones — otherwise a context switch mid-probe would dial the forward through
	// the NEW cluster, sending this probe's token to a different cluster's Argo.
	client := snap.k8sClient
	cfg := snap.k8sConfig
	if client == nil || cfg == nil {
		return nil, errors.New("kubernetes client not initialized")
	}

	podName, err := pfpkg.FindPodForService(ctx, client, namespace, service)
	if err != nil {
		return nil, fmt.Errorf("find pod for service %s/%s: %w", namespace, service, err)
	}
	localPort, err := pfpkg.FindFreePort()
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	pfCtx, cancel := context.WithCancel(context.Background())
	fwd := &activeForward{localPort: localPort, stopCh: stopCh, cancel: cancel}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pfpkg.RunPortForward(pfCtx, client, cfg, namespace, podName, localPort, targetPort, stopCh, readyCh)
	}()

	select {
	case <-readyCh:
		log.Printf("[argocd] Port-forward ready: localhost:%d -> %s/%s:%d", localPort, namespace, service, targetPort)
		// The forward is live, but RunPortForward keeps running and will return on
		// errCh if the tunnel later dies (argocd-server pod restart, network drop).
		// Consume that so the manager doesn't sit "connected" against a dead local
		// port until a manual reset — on an unexpected exit, drop the connection so
		// the next Get() reprobes and re-establishes the forward.
		go m.watchForwardExit(fwd, errCh)
		return fwd, nil
	case err := <-errCh:
		fwd.stop()
		return nil, fmt.Errorf("port-forward failed: %w", err)
	case <-time.After(10 * time.Second):
		fwd.stop()
		return nil, errors.New("port-forward timed out")
	case <-ctx.Done():
		fwd.stop()
		return nil, ctx.Err()
	}
}
