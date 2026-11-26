package argocd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/portforward"
)

const serverLabelSelector = "app.kubernetes.io/name=argocd-server"

// errNoArgoServer: discovery found nothing to connect to, because the
// cluster has no argocd-server Service or Radar may not list Services.
// Waiting won't change that, so it is not retried on its own.
var errNoArgoServer = errors.New("no Argo CD server to connect to")

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
func (m *Manager) discover(ctx context.Context, snap probeSnapshot) (string, activeForward, error) {
	// The client is captured in the snapshot, NOT read live — so a context
	// switch that swaps the live client mid-probe can't redirect this token's
	// discovery at a different cluster's argocd-server.
	k8sc := snap.k8sClient
	if k8sc == nil {
		return "", nil, fmt.Errorf("%w: no Kubernetes client available for discovery", ErrUnreachable)
	}

	cands, err := discoverCandidates(ctx, k8sc)
	if err != nil {
		if apierrors.IsForbidden(err) {
			return "", nil, fmt.Errorf("%w: %w: listing argocd-server services: %v", ErrUnreachable, errNoArgoServer, err)
		}
		return "", nil, fmt.Errorf("%w: listing argocd-server services: %v", ErrUnreachable, err)
	}
	if len(cands) == 0 {
		return "", nil, fmt.Errorf("%w: %w: no Service labeled %s found in cluster — set an explicit Argo CD URL", ErrUnreachable, errNoArgoServer, serverLabelSelector)
	}

	for _, cand := range cands {
		addr := cand.clusterURL()
		if m.probeEndpoint(ctx, addr, snap) == nil {
			log.Printf("[argocd] Discovered Argo CD at %s (service %s/%s)", addr, cand.namespace, cand.name)
			return addr, nil, nil
		}
	}

	if m.inCluster() {
		return "", nil, fmt.Errorf("%w: argocd-server service found but not reachable in-cluster (if it serves a self-signed certificate, set argoCdInsecureTls)", ErrUnreachable)
	}

	if portforward.AutoDisabled() {
		log.Printf("[argocd] No candidate reachable in-cluster; automatic port-forwarding is turned off, not attempting port-forward")
		return "", nil, fmt.Errorf("%w: %w", ErrUnreachable, portforward.ErrAutoDisabled)
	}

	var lastErr error
	for _, cand := range cands {
		log.Printf("[argocd] No candidate reachable in-cluster, starting port-forward to %s/%s...", cand.namespace, cand.name)
		start := m.startPortForward
		if m.portForward != nil {
			start = m.portForward
		}
		fwd, pfErr := start(ctx, snap, cand.namespace, cand.name, cand.targetPort)
		if errors.Is(pfErr, portforward.ErrClosed) {
			return "", nil, errStaleProbe
		}
		if pfErr != nil {
			lastErr = fmt.Errorf("port-forward to %s/%s failed: %w", cand.namespace, cand.name, pfErr)
			continue
		}

		connected := ""
		for _, scheme := range []string{"https", "http"} {
			addr := fmt.Sprintf("%s://localhost:%d", scheme, fwd.Info().LocalPort)
			if m.probeEndpoint(ctx, addr, snap) == nil {
				connected = addr
				break
			}
		}
		if snap.forwards.Closed() {
			fwd.Stop()
			return "", nil, errStaleProbe
		}
		if connected != "" {
			// Not installed here: the probe installs it with the client
			// once the token is verified, so a connection in use keeps its
			// own forward until then.
			log.Printf("[argocd] Reached %s/%s via port-forward at %s", cand.namespace, cand.name, connected)
			return connected, fwd, nil
		}

		fwd.Stop()
		lastErr = fmt.Errorf("argocd-server at %s/%s not responding after port-forward (if it serves a self-signed certificate, set argoCdInsecureTls)", cand.namespace, cand.name)
	}

	return "", nil, fmt.Errorf("%w: %v", ErrUnreachable, lastErr)
}

type activeForward interface {
	Info() *portforward.ConnectionInfo
	Stop() bool
	Alive() bool
}

func (m *Manager) watchForwardExit(fwd activeForward, done <-chan struct{}) {
	<-done
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forward == fwd {
		m.dropConnectionLocked()
		log.Printf("[argocd] Port-forward exited; will reprobe on next use")
	}
}

func (m *Manager) startPortForward(ctx context.Context, snap probeSnapshot, namespace, service string, targetPort int) (activeForward, error) {
	handle, err := snap.forwards.StartDedicated(portforward.OwnerArgoCD, ctx, namespace, service, targetPort)
	if err != nil {
		return nil, err
	}
	go m.watchForwardExit(handle, handle.Done())
	return handle, nil
}
