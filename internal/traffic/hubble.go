package traffic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	relaypb "github.com/cilium/cilium/api/v1/relay"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/portforward"
)

const (
	hubbleRelayService    = "hubble-relay"
	hubbleRelayLabel      = "k8s-app=hubble-relay"
	hubbleRelayCertSecret = "hubble-relay-client-certs"
)

// allowedHTTPHeaders is the curated set of safe, useful HTTP headers to extract.
var allowedHTTPHeaders = map[string]bool{
	"content-type": true,
	"x-request-id": true,
	"user-agent":   true,
	"grpc-status":  true,
}

// HubbleSource implements TrafficSource for Hubble/Cilium
type HubbleSource struct {
	k8sClient      kubernetes.Interface
	grpcConn       *grpc.ClientConn
	observerClient observerpb.ObserverClient
	localPort      int    // Port-forward local port (0 when connected directly)
	currentContext string // K8s context for port-forward validation
	relayNamespace string // Discovered namespace where hubble-relay lives
	relayPort      int    // Hubble relay container port (for port-forward)
	servicePort    int    // Hubble relay service port (direct dial; 443 hints TLS)
	clusterIP      string // Hubble relay service ClusterIP (direct dial)
	connectedAddr  string // Address of the live gRPC connection
	viaPortForward bool   // Connection rides the traffic-owned port-forward
	useTLS         bool   // Whether TLS certs are available
	tlsConfig      *tls.Config
	isConnected    bool
	closed         bool // Set by Close; a late Connect must not resurrect the source

	// Cached direct-lane reachability, probed off the request path (kicked at
	// detection) so Connect's lane decision costs nothing inline. Keyed by the
	// probed address: a stale entry for a different endpoint is a cache miss.
	// Generations order concurrent writers so an older in-flight probe can't
	// overwrite a newer verdict.
	probedAddr     string
	probeReachable bool
	probeSeq       uint64 // last launched probe generation
	probeApplied   uint64 // generation whose result is cached

	// Relay's connected-node count, which splits the flow budget per node.
	nodeCount   int
	nodeCountAt time.Time

	mu sync.RWMutex
}

// NewHubbleSource creates a new Hubble traffic source
func NewHubbleSource(client kubernetes.Interface) *HubbleSource {
	return &HubbleSource{
		k8sClient: client,
	}
}

// Name returns the source identifier
func (h *HubbleSource) Name() string {
	return "hubble"
}

// Detect checks if Hubble is available in the cluster using label-based discovery
func (h *HubbleSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{
		Available: false,
	}

	// Step 1: Find hubble-relay pods by label across ALL namespaces
	relayPods, err := h.k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: hubbleRelayLabel,
	})
	if err != nil {
		return result, fmt.Errorf("failed to search for hubble-relay pods: %w", err)
	}

	if len(relayPods.Items) == 0 {
		result.Message = "Hubble Relay not found. Install Cilium with Hubble enabled for traffic visibility."
		return result, nil
	}
	// From here on Hubble is installed, so every way this can still fail is a
	// problem with that install and its message has to reach the user — without
	// Present it is reported as never installed, with advice to install it.
	result.Present = true

	// Count running pods and get the namespace
	var relayNamespace string
	runningPods := 0
	for _, pod := range relayPods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods++
			if relayNamespace == "" {
				relayNamespace = pod.Namespace
			}
		}
	}

	if runningPods == 0 {
		result.Message = fmt.Sprintf("Hubble Relay pods found (%d) but none are running", len(relayPods.Items))
		return result, nil
	}

	log.Printf("[hubble] Found hubble-relay in namespace %q with %d running pod(s)", relayNamespace, runningPods)

	// Step 2: Find the hubble-relay service in the same namespace
	relaySvc, err := h.k8sClient.CoreV1().Services(relayNamespace).Get(ctx, hubbleRelayService, metav1.GetOptions{})
	if err != nil {
		result.Message = fmt.Sprintf("Hubble Relay pods running but service not found in namespace %s", relayNamespace)
		return result, nil
	}

	// Step 3: Check for TLS certs
	tlsConfig, useTLS := h.loadTLSConfig(ctx, relayNamespace)

	// Step 4: Determine service port and whether it's TLS
	servicePort := 80
	if len(relaySvc.Spec.Ports) > 0 {
		servicePort = int(relaySvc.Spec.Ports[0].Port)
	}

	// Port 443 typically means TLS is required
	if servicePort == 443 && !useTLS {
		result.Message = fmt.Sprintf("Hubble Relay requires TLS (port 443) but client certs not found in secret %s/%s", relayNamespace, hubbleRelayCertSecret)
		return result, nil
	}

	// Step 5: Store discovered configuration
	h.mu.Lock()
	h.relayNamespace = relayNamespace
	h.relayPort = h.resolveTargetPort(ctx, relaySvc)
	h.servicePort = servicePort
	h.clusterIP = relaySvc.Spec.ClusterIP
	h.useTLS = useTLS
	h.tlsConfig = tlsConfig
	directAddr := h.directAddressLocked(relayNamespace)
	h.mu.Unlock()

	// Detection always precedes Connect in the UI flow, so probing here means
	// the lane decision is already cached when the user connects — the common
	// local case (unroutable ClusterIP) skips the direct lane without paying
	// the pre-check timeout inline.
	go h.probeDirectAsync(directAddr)

	// Determine if this is GKE native Hubble
	isNative := h.isNativeHubble(ctx)

	result.Available = true
	result.Native = isNative

	tlsStatus := "plaintext"
	if useTLS {
		tlsStatus = "TLS"
	}
	result.Message = fmt.Sprintf("Hubble Relay detected in %s with %d running pod(s) (%s)", relayNamespace, runningPods, tlsStatus)

	// Try to get version from Cilium config
	ciliumConfig, err := h.k8sClient.CoreV1().ConfigMaps(relayNamespace).Get(ctx, "cilium-config", metav1.GetOptions{})
	if err == nil && ciliumConfig.Labels != nil {
		if ver, ok := ciliumConfig.Labels["cilium.io/version"]; ok {
			result.Version = ver
		}
	}

	return result, nil
}

// loadTLSConfig attempts to load TLS credentials from the hubble-relay-client-certs secret
func (h *HubbleSource) loadTLSConfig(ctx context.Context, namespace string) (*tls.Config, bool) {
	secret, err := h.k8sClient.CoreV1().Secrets(namespace).Get(ctx, hubbleRelayCertSecret, metav1.GetOptions{})
	if err != nil {
		log.Printf("[hubble] TLS cert secret not found in %s/%s: %v", namespace, hubbleRelayCertSecret, err)
		return nil, false
	}

	caCert, hasCa := secret.Data["ca.crt"]
	tlsCert, hasCert := secret.Data["tls.crt"]
	tlsKey, hasKey := secret.Data["tls.key"]

	if !hasCa || !hasCert || !hasKey {
		log.Printf("[hubble] TLS secret missing required keys (need ca.crt, tls.crt, tls.key)")
		return nil, false
	}

	// Parse CA cert
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		log.Printf("[hubble] Failed to parse CA certificate")
		return nil, false
	}

	// Parse client cert
	clientCert, err := tls.X509KeyPair(tlsCert, tlsKey)
	if err != nil {
		log.Printf("[hubble] Failed to parse client certificate: %v", err)
		return nil, false
	}

	// ServerName must match the certificate's SAN
	// GKE uses: *.gke-managed-dpv2-observability.svc.cluster.local
	// Standard Cilium uses: *.hubble-grpc.cilium.io or similar
	serverName := fmt.Sprintf("hubble-relay.%s.svc.cluster.local", namespace)

	tlsConfig := &tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}

	log.Printf("[hubble] Loaded TLS credentials from %s/%s (ServerName: %s)", namespace, hubbleRelayCertSecret, serverName)
	return tlsConfig, true
}

// discoverTLSServerName probes the server certificate to discover the correct TLS ServerName.
// This handles environments like AKS where the Hubble Relay cert has a different SAN
// (e.g., *.hubble-relay.cilium.io) than the default k8s service DNS name.
func (h *HubbleSource) discoverTLSServerName(ctx context.Context, address string) (string, error) {
	probeCfg := &tls.Config{
		InsecureSkipVerify: true,
	}
	// Include client certs in case the server requires mTLS
	if h.tlsConfig != nil && len(h.tlsConfig.Certificates) > 0 {
		probeCfg.Certificates = h.tlsConfig.Certificates
	}

	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: probeCfg}
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", fmt.Errorf("failed to probe server certificate: %w", err)
	}
	defer rawConn.Close()
	conn := rawConn.(*tls.Conn)

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("server returned no certificates")
	}

	serverCert := certs[0]
	if len(serverCert.DNSNames) == 0 {
		return "", fmt.Errorf("server certificate has no DNS SANs")
	}

	san := serverCert.DNSNames[0]
	if strings.HasPrefix(san, "*.") {
		// Wildcard cert (e.g., *.hubble-relay.cilium.io) — construct a concrete match
		san = "relay" + san[1:]
	}
	return san, nil
}

// isNativeHubble checks if this is GKE Dataplane V2 (native Hubble)
func (h *HubbleSource) isNativeHubble(ctx context.Context) bool {
	// Check for GKE by looking at node provider ID
	nodes, err := h.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil || len(nodes.Items) == 0 {
		return false
	}

	node := nodes.Items[0]

	// GKE nodes have gce:// provider ID
	if strings.HasPrefix(node.Spec.ProviderID, "gce://") {
		// Check for Dataplane V2 specific labels or annotations
		if _, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok {
			return true
		}
	}

	return false
}

// resolveTargetPort resolves the actual container port from the service
// The service may use a named targetPort (e.g., "grpc") that maps to a container port
func (h *HubbleSource) resolveTargetPort(ctx context.Context, svc *corev1.Service) int {
	if len(svc.Spec.Ports) == 0 {
		return 80
	}

	svcPort := svc.Spec.Ports[0]

	// If targetPort is a number, use it directly
	if svcPort.TargetPort.IntValue() > 0 {
		return svcPort.TargetPort.IntValue()
	}

	// If targetPort is a named port, we need to find the actual port from pods
	if svcPort.TargetPort.StrVal != "" {
		// Find a pod backing this service
		if svc.Spec.Selector != nil {
			var labelSelector string
			for k, v := range svc.Spec.Selector {
				if labelSelector != "" {
					labelSelector += ","
				}
				labelSelector += k + "=" + v
			}

			pods, err := h.k8sClient.CoreV1().Pods(svc.Namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
				Limit:         1,
			})
			if err == nil && len(pods.Items) > 0 {
				pod := pods.Items[0]
				for _, container := range pod.Spec.Containers {
					for _, port := range container.Ports {
						if port.Name == svcPort.TargetPort.StrVal {
							log.Printf("[hubble] Resolved named port %q to %d", svcPort.TargetPort.StrVal, port.ContainerPort)
							return int(port.ContainerPort)
						}
					}
				}
			}
		}
	}

	// Fallback to service port
	if svcPort.Port > 0 {
		return int(svcPort.Port)
	}
	return 80
}

const (
	// directDialTimeout bounds the TCP reachability pre-check for the direct
	// lane. Blackholed traffic (NetworkPolicy drop, or a laptop dialing an
	// unroutable ClusterIP — the usual local case) costs exactly this much on
	// the first connect before the port-forward fallback starts, so it is
	// deliberately tight: the pre-check is a single TCP handshake (one
	// round-trip), which finishes well under a second on any network where it
	// can succeed at all, including high-latency VPNs into the cluster.
	// Refused/unroutable fails in milliseconds.
	directDialTimeout = 1500 * time.Millisecond

	// directConnectBudget bounds the full gRPC/TLS/SAN sequence against a
	// TCP-reachable relay. Generous on purpose: once the endpoint answered TCP
	// it deserves the sequence's normal per-step timeouts (plaintext test +
	// TLS + SAN probe + TLS retry are up to ~3s each).
	directConnectBudget = 15 * time.Second
)

// Connect establishes a gRPC connection to Hubble Relay. The relay Service is
// dialed directly first — in-cluster (or on a network that routes ClusterIPs)
// that path needs no pods/portforward RBAC — with a managed port-forward as
// the fallback when the direct address is unreachable.
func (h *HubbleSource) Connect(ctx context.Context, contextName string) (*portforward.ConnectionInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// A Connect that raced Close (context switch) must not resurrect the
	// source — its forward would point at the previous cluster and outlive
	// Reset's cleanup.
	if h.closed {
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     "traffic source closed (context switched)",
		}, nil
	}

	namespace := h.relayNamespace
	if namespace == "" {
		namespace = "kube-system" // fallback
	}

	// If already connected to the same context, verify connection is still valid
	if h.grpcConn != nil && h.currentContext == contextName {
		// Test the connection
		if h.testConnection(ctx) {
			h.stopOwnStaleForwardLocked(namespace)
			return h.connectionInfoLocked(namespace, contextName), nil
		}
		// Connection lost, clean up
		h.closeConnectionLocked()
	}

	// Clear stale state if context changed
	if h.currentContext != contextName {
		h.closeConnectionLocked()
		h.currentContext = contextName
	}

	// Resolve ports from the Service if detection hasn't already. Fills both the
	// service port (direct dial) and the container port (port-forward bypasses
	// the Service) — they routinely differ (e.g. 80 -> 4245).
	if h.servicePort == 0 || h.relayPort == 0 {
		relaySvc, err := h.k8sClient.CoreV1().Services(namespace).Get(ctx, hubbleRelayService, metav1.GetOptions{})
		if err != nil {
			return &portforward.ConnectionInfo{
				Connected: false,
				Error:     fmt.Sprintf("Hubble Relay service not found in %s: %v", namespace, err),
			}, nil
		}
		if len(relaySvc.Spec.Ports) > 0 {
			h.servicePort = int(relaySvc.Spec.Ports[0].Port)
		}
		h.relayPort = h.resolveTargetPort(ctx, relaySvc)
		h.clusterIP = relaySvc.Spec.ClusterIP
	}

	// Direct lane. A raw TCP dial decides whether the address is reachable at
	// all; only then does the full gRPC/TLS sequence run against it, with its
	// normal per-step timeouts intact. A cached-unreachable verdict from the
	// background probe skips the lane without paying the pre-check timeout —
	// but only as a fast-path hint: if the fallback also fails, the lane is
	// retried for real below.
	directAddr := h.directAddressLocked(namespace)
	var directErr error
	skippedDirect := h.probedAddr == directAddr && !h.probeReachable
	if skippedDirect {
		directErr = errDirectUnreachable
		// Re-probe off the request path so a network change (a VPN coming up)
		// can restore the direct lane on a later connect.
		go h.probeDirectAsync(directAddr)
	} else {
		directErr = h.tryDirectLocked(ctx, directAddr)
		if directErr == nil {
			return h.commitDirectLocked(directAddr, namespace, contextName), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log.Printf("[hubble] Direct connection to %s failed (%v); falling back to port-forward", directAddr, directErr)

	// The cached verdict that skipped the direct lane may itself be stale (a
	// probe that raced relay startup); once the fallback is dead too, paying
	// the pre-check inline is free — try the lane for real before reporting
	// failure. Covers every fallback failure, including a reused forward that
	// turns out to be broken.
	retryDirectAfterSkip := func() *portforward.ConnectionInfo {
		if !skippedDirect || ctx.Err() != nil {
			return nil
		}
		if retryErr := h.tryDirectLocked(ctx, directAddr); retryErr == nil {
			return h.commitDirectLocked(directAddr, namespace, contextName)
		} else {
			directErr = retryErr
		}
		return nil
	}

	// Fallback lane: managed port-forward, which needs pods/portforward RBAC.
	log.Printf("[hubble] Starting port-forward to %s/%s:%d", namespace, hubbleRelayService, h.relayPort)
	connInfo, err := portforward.Start(portforward.OwnerTraffic, ctx, namespace, hubbleRelayService, h.relayPort, contextName)
	if err != nil {
		if info := retryDirectAfterSkip(); info != nil {
			return info, nil
		}
		return &portforward.ConnectionInfo{
			Connected:   false,
			Namespace:   namespace,
			ServiceName: hubbleRelayService,
			Error:       composeConnectError(directAddr, directErr, err),
		}, nil
	}

	if !connInfo.Connected {
		return connInfo, nil
	}

	h.localPort = connInfo.LocalPort
	grpcAddr := fmt.Sprintf("localhost:%d", h.localPort)
	if err := h.connectGRPCLocked(ctx, grpcAddr); err != nil {
		portforward.Stop(portforward.OwnerTraffic)
		h.localPort = 0
		if info := retryDirectAfterSkip(); info != nil {
			return info, nil
		}
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     fmt.Sprintf("Failed to connect to Hubble Relay via port-forward: %v (direct connection to %s also failed: %v)", err, directAddr, directErr),
		}, nil
	}
	h.viaPortForward = true
	return h.connectionInfoLocked(namespace, contextName), nil
}

// tryDirectLocked attempts the direct lane: TCP pre-check, then the full
// gRPC/TLS sequence. Records the reachability observation either way. Caller
// must hold h.mu.
func (h *HubbleSource) tryDirectLocked(ctx context.Context, directAddr string) error {
	if !tcpReachable(directAddr, directDialTimeout) {
		h.recordProbeLocked(directAddr, false)
		return errDirectUnreachable
	}
	h.recordProbeLocked(directAddr, true)
	directCtx, cancel := context.WithTimeout(ctx, directConnectBudget)
	defer cancel()
	return h.connectGRPCLocked(directCtx, directAddr)
}

// commitDirectLocked finalizes a successful direct connection. Caller must
// hold h.mu.
func (h *HubbleSource) commitDirectLocked(directAddr, namespace, contextName string) *portforward.ConnectionInfo {
	h.viaPortForward = false
	h.localPort = 0
	h.stopOwnStaleForwardLocked(namespace)
	log.Printf("[hubble] Connected to Hubble Relay directly at %s", directAddr)
	return h.connectionInfoLocked(namespace, contextName)
}

// connectionInfoLocked builds the ConnectionInfo for the live connection.
// Caller must hold h.mu (read or write).
func (h *HubbleSource) connectionInfoLocked(namespace, contextName string) *portforward.ConnectionInfo {
	return &portforward.ConnectionInfo{
		Connected:   true,
		LocalPort:   h.localPort,
		Address:     h.connectedAddr,
		Namespace:   namespace,
		ServiceName: hubbleRelayService,
		ContextName: contextName,
	}
}

// directAddressLocked builds the relay Service address for a direct dial.
// ClusterIP is preferred — dialing an IP can't wedge in the resolver the way
// unresolvable cluster DNS names can off-cluster. The DNS fallback (headless
// Service) deliberately stops at ".svc" so clusters with a custom cluster
// domain still resolve it through their search domains.
func (h *HubbleSource) directAddressLocked(namespace string) string {
	if h.clusterIP != "" && h.clusterIP != corev1.ClusterIPNone {
		return net.JoinHostPort(h.clusterIP, fmt.Sprintf("%d", h.servicePort))
	}
	name := fmt.Sprintf("%s.%s.svc", hubbleRelayService, namespace)
	if h.clusterIP == corev1.ClusterIPNone {
		// Headless DNS resolves to pod IPs, which do no service-to-container
		// port mapping — dial the container port.
		return net.JoinHostPort(name, fmt.Sprintf("%d", h.relayPort))
	}
	return net.JoinHostPort(name, fmt.Sprintf("%d", h.servicePort))
}

// tcpReachable reports whether addr accepts a TCP connection within timeout.
func tcpReachable(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// probeDirectAsync measures TCP reachability of the direct relay address off
// the request path and caches the outcome for Connect's lane decision. The
// dial runs before taking the lock, so an in-flight Connect is never blocked;
// the generation check keeps a slow older probe from overwriting a newer
// verdict (including Connect's own inline observations).
func (h *HubbleSource) probeDirectAsync(addr string) {
	h.mu.Lock()
	h.probeSeq++
	gen := h.probeSeq
	h.mu.Unlock()

	reachable := tcpReachable(addr, directDialTimeout)

	if h.applyProbeResult(gen, addr, reachable) && !reachable {
		log.Printf("[hubble] Direct address %s unreachable (background probe); connects will use port-forward", addr)
	}
}

// applyProbeResult commits a probe outcome unless a newer generation already
// landed; reports whether the write was applied.
func (h *HubbleSource) applyProbeResult(gen uint64, addr string, reachable bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if gen <= h.probeApplied {
		return false
	}
	h.probeApplied = gen
	h.probedAddr = addr
	h.probeReachable = reachable
	return true
}

// recordProbeLocked caches an inline reachability observation as the newest
// verdict. Caller must hold h.mu.
func (h *HubbleSource) recordProbeLocked(addr string, reachable bool) {
	h.probeSeq++
	h.probeApplied = h.probeSeq
	h.probedAddr = addr
	h.probeReachable = reachable
}

// errDirectUnreachable marks a direct lane that failed at TCP reachability
// (as opposed to reaching the relay but failing TLS/gRPC) — only that failure
// mode makes "open the network path" valid remediation.
var errDirectUnreachable = errors.New("tcp dial failed (unreachable or blocked)")

// composeConnectError reports both failed lanes so the user sees the whole
// picture; an RBAC-denied port-forward additionally names the real fixes.
func composeConnectError(directAddr string, directErr, pfErr error) string {
	msg := fmt.Sprintf("direct connection to %s (%s) failed (%v); port-forward fallback failed: %v", hubbleRelayService, directAddr, directErr, pfErr)
	if isForbiddenPortForward(pfErr) {
		msg += " — grant Radar port-forward permission (Helm: rbac.portForward=true)"
		if errors.Is(directErr, errDirectUnreachable) {
			msg += ", or allow network traffic from Radar's namespace to hubble-relay so the direct connection works"
		}
	}
	return msg
}

func isForbiddenPortForward(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "is forbidden") && strings.Contains(s, "portforward")
}

// ConnectionInfo implements ConnectionReporter. In direct mode the stored
// state is authoritative; in port-forward mode the live registry decides —
// but only a forward that still targets hubble-relay in our context counts: a
// dead forward, or one replaced by another source, must not read as connected.
func (h *HubbleSource) ConnectionInfo() *portforward.ConnectionInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.isConnected {
		return &portforward.ConnectionInfo{Connected: false}
	}
	namespace := h.relayNamespace
	if namespace == "" {
		namespace = "kube-system"
	}
	if h.viaPortForward {
		pf := portforward.GetConnectionInfo(portforward.OwnerTraffic)
		if !forwardMatches(pf, namespace, hubbleRelayService, h.currentContext) {
			return &portforward.ConnectionInfo{Connected: false}
		}
		return pf
	}
	return h.connectionInfoLocked(namespace, h.currentContext)
}

// forwardMatches reports whether a registry forward is live and targets the
// given service in the given context.
func forwardMatches(pf *portforward.ConnectionInfo, namespace, serviceName, contextName string) bool {
	return pf != nil && pf.Connected &&
		pf.Namespace == namespace && pf.ServiceName == serviceName && pf.ContextName == contextName
}

// stopOwnStaleForwardLocked drops a traffic-owned forward left from our own
// earlier port-forward connect once the direct connection is the data path.
// Deliberately scoped to forwards targeting hubble-relay in our context: a
// stale Connect can race a source switch, and an unconditional Stop here
// would kill the forward the newly active source just established. Caller
// must hold h.mu.
func (h *HubbleSource) stopOwnStaleForwardLocked(namespace string) {
	if h.viaPortForward {
		return
	}
	pf := portforward.GetConnectionInfo(portforward.OwnerTraffic)
	if forwardMatches(pf, namespace, hubbleRelayService, h.currentContext) {
		portforward.Stop(portforward.OwnerTraffic)
	}
}

// connectGRPCLocked runs the plaintext/TLS/SAN-discovery connection sequence
// against grpcAddr and commits the connection state on success. Caller must
// hold h.mu.
func (h *HubbleSource) connectGRPCLocked(ctx context.Context, grpcAddr string) error {
	// Use service port as heuristic: port 443 suggests TLS, otherwise try plaintext first
	// This avoids unnecessary latency from failed connection attempts
	tryTLSFirst := h.servicePort == 443 && h.tlsConfig != nil

	var conn *grpc.ClientConn
	var lastErr error

	// Define connection attempt functions
	tryPlaintext := func() bool {
		log.Printf("[hubble] Connecting to gRPC at %s (plaintext)", grpcAddr)
		var err error
		conn, err = grpc.NewClient(grpcAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			lastErr = err
			return false
		}
		h.grpcConn = conn
		h.observerClient = observerpb.NewObserverClient(conn)
		h.isConnected = true
		if h.testConnection(ctx) {
			log.Printf("[hubble] Connected to Hubble Relay at %s (plaintext)", grpcAddr)
			return true
		}
		lastErr = fmt.Errorf("plaintext gRPC connection test failed")
		h.closeConnectionLocked()
		return false
	}

	tryTLSWith := func(serverName string) bool {
		if h.tlsConfig == nil {
			return false
		}
		cfg := h.tlsConfig.Clone()
		cfg.ServerName = serverName
		log.Printf("[hubble] Connecting to gRPC at %s (TLS, ServerName: %s)", grpcAddr, serverName)
		var err error
		conn, err = grpc.NewClient(grpcAddr,
			grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		)
		if err != nil {
			lastErr = fmt.Errorf("TLS connection failed: %w", err)
			return false
		}
		h.grpcConn = conn
		h.observerClient = observerpb.NewObserverClient(conn)
		h.isConnected = true
		if h.testConnection(ctx) {
			log.Printf("[hubble] Connected to Hubble Relay at %s (TLS)", grpcAddr)
			return true
		}
		h.closeConnectionLocked()
		return false
	}

	tryTLS := func() bool {
		if h.tlsConfig == nil {
			return false
		}

		if tryTLSWith(h.tlsConfig.ServerName) {
			return true
		}

		// TLS may have failed due to ServerName mismatch (e.g., AKS cert uses *.hubble-relay.cilium.io).
		// Probe the server certificate to discover the correct ServerName.
		discoveredName, err := h.discoverTLSServerName(ctx, grpcAddr)
		if err != nil {
			log.Printf("[hubble] Could not discover server name: %v", err)
			lastErr = fmt.Errorf("TLS gRPC connection test failed")
			return false
		}
		if discoveredName == h.tlsConfig.ServerName {
			lastErr = fmt.Errorf("TLS gRPC connection test failed")
			return false
		}

		log.Printf("[hubble] Retrying TLS with discovered ServerName: %s (was: %s)", discoveredName, h.tlsConfig.ServerName)

		if tryTLSWith(discoveredName) {
			return true
		}

		lastErr = fmt.Errorf("TLS gRPC connection test failed (tried default and discovered ServerName %s)", discoveredName)
		return false
	}

	// Try connections in order based on service port heuristic
	var connected bool
	if tryTLSFirst {
		connected = tryTLS() || tryPlaintext()
	} else {
		connected = tryPlaintext() || tryTLS()
	}

	if connected {
		h.connectedAddr = grpcAddr
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("connection failed")
	}
	return lastErr
}

// testConnection tests the gRPC connection by calling ServerStatus
func (h *HubbleSource) testConnection(ctx context.Context) bool {
	if h.observerClient == nil {
		return false
	}

	testCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := h.observerClient.ServerStatus(testCtx, &observerpb.ServerStatusRequest{})
	if err != nil {
		log.Printf("[hubble] Connection test failed: %v", err)
		return false
	}
	return true
}

// closeConnectionLocked closes the gRPC connection (caller must hold lock)
func (h *HubbleSource) closeConnectionLocked() {
	if h.grpcConn != nil {
		h.grpcConn.Close()
		h.grpcConn = nil
	}
	h.observerClient = nil
	h.isConnected = false
	h.localPort = 0
	h.connectedAddr = ""
	h.viaPortForward = false
}

// GetFlows retrieves flows from Hubble via gRPC
func (h *HubbleSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	h.mu.RLock()
	client := h.observerClient
	connected := h.isConnected
	h.mu.RUnlock()

	if !connected || client == nil {
		// Not connected yet - return empty with message
		return &FlowsResponse{
			Source:    "hubble",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   "Not connected to Hubble Relay. Call Connect() first or use the Traffic view to establish connection.",
		}, nil
	}

	fetched, err := h.fetchFlowsViaGRPC(ctx, opts)
	if err != nil {
		log.Printf("[hubble] gRPC error: %v", err)
		return &FlowsResponse{
			Source:    "hubble",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   fmt.Sprintf("Failed to fetch flows: %v", err),
		}, nil
	}

	flows := fetched.flows
	response := &FlowsResponse{
		Source:        "hubble",
		Timestamp:     time.Now(),
		Flows:         flows,
		CoveredSince:  fetched.coveredSince,
		NodeFlowLimit: fetched.nodeLimit,
		FlowLimit:     fetched.flowLimit,
	}
	if fetched.incomplete != "" {
		response.Warning = fetched.incomplete
		// The fetch worked; it could not see everything. A busy node that keeps
		// dropping events will keep saying so, which is the truth about its data
		// rather than a failure to retry.
		response.WarningKind = WarningIncomplete
		// A stream that failed before any flow arrived is a failed fetch that
		// happened to report some gaps first; retrying it can bring the flows.
		if fetched.streamFailed && len(flows) == 0 {
			response.WarningKind = WarningTransient
		}
	}
	return response, nil
}

// hubbleFetch is what one GetFlows call established.
type hubbleFetch struct {
	flows []Flow
	// incomplete explains, in terms a reader can act on, why the flows are not
	// everything Hubble holds for the window; empty when nothing is missing.
	incomplete   string
	streamFailed bool
	// coveredSince is when the flows start being complete: some node reached
	// the per-node limit, and older traffic from it is not included. Nil when
	// every node's traffic covers the whole window.
	coveredSince *time.Time
	nodeLimit    int
	// flowLimit is the total cap, set when it dropped the oldest flows.
	flowLimit int
}

// hubbleFlowsRequest builds the request for the newest flows. Since is left out
// on purpose: given Since, Hubble rewinds to it and reads forward, so a node
// with more than Number flows in the window returns its oldest ones and the
// view ends minutes before now. Without it each node returns its newest Number,
// and the window is applied as those arrive.
func hubbleFlowsRequest(opts FlowOptions, follow bool) *observerpb.GetFlowsRequest {
	req := &observerpb.GetFlowsRequest{
		Follow:    follow,
		FieldMask: &fieldmaskpb.FieldMask{Paths: hubbleFlowFields},
	}
	if !follow {
		req.Number = hubbleDefaultNodeLimit
		if opts.Limit > 0 {
			req.Number = uint64(opts.Limit)
		}
	}
	// Source OR destination: each filter is AND within itself, filters are OR'd.
	// A selection narrower than the namespaces replaces them: its pods already
	// sit in namespaces the caller sees, and the handler filters by namespace
	// access on the result either way.
	if match := hubbleMatchWhitelist(opts.Match); match != nil {
		req.Whitelist = match
	} else if namespaces := hubbleNamespaces(opts); len(namespaces) > 0 {
		prefixes := make([]string, len(namespaces))
		for i, ns := range namespaces {
			prefixes[i] = ns + "/"
		}
		req.Whitelist = []*flowpb.FlowFilter{
			{SourcePod: prefixes},
			{DestinationPod: prefixes},
		}
	}
	req.Blacklist = []*flowpb.FlowFilter{
		// Reply packets at L3/L4 are dropped by callerOrientedFlow anyway;
		// excluding them here stops them spending the per-node limit, which on a
		// live cluster they took over 40% of. Only packet traces with an explicit
		// is_reply=true match — the only L3/L4 records Cilium marks as replies —
		// so L7 responses, drops, policy verdicts, socket traces and flows of
		// unknown direction all still arrive.
		{
			Reply:     []bool{true},
			EventType: []*flowpb.EventTypeFilter{{Type: hubbleEventTypeTrace}},
		},
		// Agent and debug events are not flows, but a node counts them toward
		// Number as it walks back through its buffer. Left in, a node could hit
		// its limit while returning fewer flows than the limit, and the fetch
		// would read as complete when it was cut.
		{EventType: []*flowpb.EventTypeFilter{{Type: hubbleEventTypeAgent}, {Type: hubbleEventTypeDebug}}},
	}
	// Traffic the view hides is dropped at the node, where it would otherwise
	// fill each node's Number before the traffic the view shows.
	if len(opts.ExcludeNamespaces) > 0 {
		prefixes := make([]string, len(opts.ExcludeNamespaces))
		for i, ns := range opts.ExcludeNamespaces {
			prefixes[i] = ns + "/"
		}
		req.Blacklist = append(req.Blacklist,
			&flowpb.FlowFilter{SourcePod: prefixes},
			&flowpb.FlowFilter{DestinationPod: prefixes},
		)
	}
	if opts.ExcludeHost {
		req.Blacklist = append(req.Blacklist,
			&flowpb.FlowFilter{SourceLabel: hubbleHostLabels},
			&flowpb.FlowFilter{DestinationLabel: hubbleHostLabels},
		)
	}
	return req
}

// hubbleFlowFields are the parts of a flow the conversion reads
// (callerOrientedFlow and what it calls, plus the time and node the fetch
// tracks coverage by). Relay sends only these, so a flow arrives without its
// Ethernet and tunnel headers, node labels, trace context, socket details and
// the rest. A Relay older than Cilium 1.19 ignores the mask and sends whole
// flows. TestHubbleFlowFieldsCoverConversion fails when the conversion starts
// reading a field that is not listed.
//
// l4, l7 and IP are named whole: naming a field inside them makes Hubble
// create the enclosing message on flows that have none, and an absent L7
// record is meaningful here (a reply without one is not an edge).
var hubbleFlowFields = []string{
	"time", "node_name", "verdict", "drop_reason", "drop_reason_desc",
	"IP", "l4", "l7", "is_reply", "traffic_direction",
	"source.namespace", "source.pod_name", "source.labels",
	"destination.namespace", "destination.pod_name", "destination.labels",
	"source_service.name", "destination_service.name",
	"ingress_allowed_by", "egress_allowed_by", "ingress_denied_by", "egress_denied_by",
}

// hubbleHostLabels are the identities classifyNonPodIdentity reports as Host.
var hubbleHostLabels = []string{"reserved:host", "reserved:remote-node", "reserved:kube-apiserver"}

func hubbleNamespaces(opts FlowOptions) []string {
	if len(opts.Namespaces) > 0 {
		return opts.Namespaces
	}
	if opts.Namespace != "" {
		return []string{opts.Namespace}
	}
	return nil
}

// hubbleGeneratedPodOwners are the workload kinds whose pods are named from
// the workload's name: the controller names its pods, or its ReplicaSets or
// Jobs, by appending a suffix to it.
var hubbleGeneratedPodOwners = map[string]bool{
	"Deployment": true, "ReplicaSet": true, "StatefulSet": true, "DaemonSet": true,
	"Job": true, "CronJob": true, "Rollout": true,
}

// hubbleWorkloadPrefixLen is how much of a workload's name its pods are sure
// to start with. Generated names are cut to 58 characters before the random
// suffix, so anything shorter than that survives intact.
const hubbleWorkloadPrefixLen = 40

// hubblePodPrefix is the pod-name prefix that covers every pod of a
// reference: the pod itself, or each pod a workload generated.
func hubblePodPrefix(r EndpointRef) (string, bool) {
	if r.Namespace == "" || r.Name == "" {
		return "", false
	}
	switch {
	case r.Kind == EndpointKindPod:
		return r.Namespace + "/" + r.Name, true
	case r.Kind == EndpointKindWorkload && hubbleGeneratedPodOwners[r.WorkloadKind]:
		if len(r.Name) > hubbleWorkloadPrefixLen {
			return r.Namespace + "/" + r.Name[:hubbleWorkloadPrefixLen], true
		}
		return r.Namespace + "/" + r.Name + "-", true
	}
	return "", false
}

// hubbleMaxMatchPods bounds the pod prefixes a selection sends to every
// node. Each one is checked against each flow there, so a selection naming
// thousands of pods is filtered here instead.
const hubbleMaxMatchPods = 200

// hubbleMatchWhitelist turns a selection into filters Hubble applies at the
// node, or nil when it cannot express it. Hubble matches pod names by prefix,
// so the result is a superset the caller narrows with Match.Matches. Only pods
// can be named this way: an endpoint that is an external address or a host is
// left open rather than guessed at. Each filter is also sent reversed, because
// a reply travels callee to caller and is turned around only after it arrives
// (callerOrientedFlow) — with the service port then on the source side.
//
// A small selection is sent pair by pair, each with its port, so traffic
// between other combinations of the same pods, or on other ports, cannot use
// up a node's limit before the selected traffic. A large one is sent as two
// sets of pods, which is coarser but keeps the filter list short.
func hubbleMatchWhitelist(m *FlowMatch) []*flowpb.FlowFilter {
	if m == nil || len(m.Pairs) == 0 {
		return nil
	}
	if len(m.Pairs) <= hubbleMaxMatchPairs {
		filters := make([]*flowpb.FlowFilter, 0, 2*len(m.Pairs))
		for _, p := range m.Pairs {
			src, srcOK := hubblePodPrefix(p.Source)
			dst, dstOK := hubblePodPrefix(p.Destination)
			if !srcOK && !dstOK {
				return nil
			}
			fwd, rev := &flowpb.FlowFilter{}, &flowpb.FlowFilter{}
			if srcOK {
				fwd.SourcePod, rev.DestinationPod = []string{src}, []string{src}
			}
			if dstOK {
				fwd.DestinationPod, rev.SourcePod = []string{dst}, []string{dst}
			}
			if p.Port > 0 {
				port := strconv.Itoa(p.Port)
				fwd.DestinationPort, rev.SourcePort = []string{port}, []string{port}
			}
			filters = append(filters, fwd, rev)
		}
		return filters
	}
	pods := func(refs []EndpointRef) []string {
		out := make([]string, 0, len(refs))
		seen := map[string]bool{}
		for _, r := range refs {
			p, ok := hubblePodPrefix(r)
			if !ok {
				return nil
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		if len(out) > hubbleMaxMatchPods {
			return nil
		}
		return out
	}
	srcs := make([]EndpointRef, len(m.Pairs))
	dsts := make([]EndpointRef, len(m.Pairs))
	for i, pair := range m.Pairs {
		srcs[i], dsts[i] = pair.Source, pair.Destination
	}
	sp, dp := pods(srcs), pods(dsts)
	if len(sp) == 0 && len(dp) == 0 {
		return nil
	}
	return []*flowpb.FlowFilter{
		{SourcePod: sp, DestinationPod: dp},
		{SourcePod: dp, DestinationPod: sp},
	}
}

// hubbleMaxMatchPairs is the largest selection sent pair by pair.
const hubbleMaxMatchPairs = 50

// Cilium's monitor message types (pkg/monitor/api MessageType*), kept local
// rather than importing that package for three numbers.
const (
	hubbleEventTypeDebug = 2
	hubbleEventTypeTrace = 4
	hubbleEventTypeAgent = 130
)

// hubbleDefaultNodeLimit is how many flows each node returns when the caller
// sets no limit. Hubble Relay applies Number per node, not in total.
const hubbleDefaultNodeLimit = 1000

// hubbleFlowBudget is the most flows one fetch keeps in total. Number applies
// per node, so without it a fetch grows with the cluster: a few hundred nodes
// at the default limit is hundreds of thousands of flows, held here and then
// aggregated per request. Up to 50 nodes it changes nothing.
var hubbleFlowBudget = 50_000

// hubbleMinNodeLimit keeps each node's share of the budget from shrinking to
// a few flows on a very large cluster; the receive-time cap still holds the
// total to the budget.
const hubbleMinNodeLimit = 100

// hubbleNodeLimit spreads the budget over the nodes Relay reads from, never
// asking a node for more than the caller's per-node limit. With the node
// count unknown it keeps the caller's limit and leaves the total to the
// receive-time cap.
func hubbleNodeLimit(perNode uint64, nodes int) uint64 {
	if nodes <= 0 {
		return perNode
	}
	share := uint64((hubbleFlowBudget + nodes - 1) / nodes)
	share = max(share, hubbleMinNodeLimit)
	return min(share, perNode)
}

// hubbleNodeCountTTL is how long the connected-node count from Relay is
// reused; it changes with cluster size, not between refreshes.
const hubbleNodeCountTTL = time.Minute

// connectedNodes asks Relay how many nodes it reads from, 0 when it cannot say.
func (h *HubbleSource) connectedNodes(ctx context.Context, client observerpb.ObserverClient) int {
	h.mu.RLock()
	n, at := h.nodeCount, h.nodeCountAt
	h.mu.RUnlock()
	if !at.IsZero() && time.Since(at) < hubbleNodeCountTTL {
		return n
	}
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	n = 0
	if status, err := client.ServerStatus(statusCtx, &observerpb.ServerStatusRequest{}); err == nil {
		n = int(status.GetNumConnectedNodes().GetValue())
	}
	h.mu.Lock()
	h.nodeCount, h.nodeCountAt = n, time.Now()
	h.mu.Unlock()
	return n
}

// keepNewest trims flows to the newest limit, returning the trimmed slice
// and whether anything was dropped.
func keepNewest(flows []Flow, limit int) ([]Flow, bool) {
	if len(flows) <= limit {
		return flows, false
	}
	slices.SortFunc(flows, func(a, b Flow) int { return b.LastSeen.Compare(a.LastSeen) })
	clear(flows[limit:])
	return flows[:limit], true
}

// fetchFlowsViaGRPC fetches the newest flows in the window.
func (h *HubbleSource) fetchFlowsViaGRPC(ctx context.Context, opts FlowOptions) (hubbleFetch, error) {
	h.mu.RLock()
	client := h.observerClient
	h.mu.RUnlock()

	if client == nil {
		return hubbleFetch{}, fmt.Errorf("not connected to Hubble Relay")
	}

	req := hubbleFlowsRequest(opts, false)
	req.Number = hubbleNodeLimit(req.Number, h.connectedNodes(ctx, client))
	var windowStart time.Time
	if opts.Since > 0 {
		windowStart = time.Now().Add(-opts.Since)
	}
	type nodeSeen struct {
		count  uint64
		oldest time.Time
	}
	perNode := map[string]*nodeSeen{}
	var flows []Flow
	var streamFailed, trimmed bool

	// Create context with timeout
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	stream, err := client.GetFlows(reqCtx, req)
	if err != nil {
		return hubbleFetch{}, fmt.Errorf("failed to get flows stream: %w", err)
	}

	var gaps []string
	var lost, delivered uint64
	var unavailableNodes []string
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Anything the stream delivered before failing — flows, or word of
			// events lost and nodes unreachable — is still an answer, and a
			// bare fetch error would throw away what is known to be missing.
			if len(flows) > 0 || lost > 0 || len(unavailableNodes) > 0 {
				log.Printf("[hubble] Stream ended with partial results: %v", err)
				streamFailed = true
				if len(flows) > 0 {
					gaps = append(gaps, fmt.Sprintf("the stream from Hubble Relay ended early (%v), so flows after the first %d are missing", err, len(flows)))
				} else {
					gaps = append(gaps, fmt.Sprintf("the stream from Hubble Relay failed (%v) before delivering any flows", err))
				}
				break
			}
			return hubbleFetch{}, fmt.Errorf("stream error: %w", err)
		}

		// Hubble reports in-band what it could not deliver: events a full
		// buffer dropped, and nodes the relay could not reach. The flows around
		// them are real, but the window is no longer everything that happened.
		if le := resp.GetLostEvents(); le != nil {
			lost += le.GetNumEventsLost()
			continue
		}
		if ns := resp.GetNodeStatus(); ns != nil {
			switch ns.GetStateChange() {
			case relaypb.NodeState_NODE_UNAVAILABLE, relaypb.NodeState_NODE_ERROR:
				unavailableNodes = append(unavailableNodes, ns.GetNodeNames()...)
			}
			continue
		}

		// Extract flow from response
		pbFlow := resp.GetFlow()
		if pbFlow == nil {
			continue
		}
		delivered++
		// A flow without a timestamp can be placed in no window; it is kept
		// rather than silently lost, and says nothing about coverage.
		if pbFlow.GetTime() != nil {
			ts := pbFlow.GetTime().AsTime()
			seen := perNode[pbFlow.GetNodeName()]
			if seen == nil {
				seen = &nodeSeen{oldest: ts}
				perNode[pbFlow.GetNodeName()] = seen
			}
			seen.count++
			if ts.Before(seen.oldest) {
				seen.oldest = ts
			}
			if !windowStart.IsZero() && ts.Before(windowStart) {
				continue
			}
		}

		flow, ok := callerOrientedFlow(pbFlow)
		if !ok {
			continue
		}
		flows = append(flows, flow)
		// Trimming at twice the budget keeps memory bounded without sorting
		// on every flow.
		if len(flows) >= 2*hubbleFlowBudget {
			var cut bool
			flows, cut = keepNewest(flows, hubbleFlowBudget)
			trimmed = trimmed || cut
		}
	}
	var cut bool
	flows, cut = keepNewest(flows, hubbleFlowBudget)
	trimmed = trimmed || cut

	// A node that returned its full limit may have had older traffic in the
	// window that did not fit. Its oldest returned flow is where its data starts
	// being complete; the view as a whole is complete from the latest of those.
	var coveredSince *time.Time
	for _, seen := range perNode {
		if seen.count < req.Number || !seen.oldest.After(windowStart) {
			continue
		}
		if coveredSince == nil || seen.oldest.After(*coveredSince) {
			t := seen.oldest
			coveredSince = &t
		}
	}
	// Past the budget only the newest flows were kept, so the view is complete
	// from the oldest of them — unless a node's own limit already cut later.
	if trimmed {
		oldest := flows[len(flows)-1].LastSeen
		if coveredSince == nil || oldest.After(*coveredSince) {
			coveredSince = &oldest
		}
	}
	result := hubbleFetch{flows: flows, coveredSince: coveredSince, nodeLimit: int(req.Number)}
	if trimmed {
		result.flowLimit = hubbleFlowBudget
	}

	if lost > 0 {
		log.Printf("[hubble] %d events lost against %d delivered", lost, delivered)
	}
	// A busy relay carries a few loss markers in its buffer nearly all the time —
	// a handful against thousands of delivered events on a live cluster — and a
	// warning on every fetch for those would be ignored along with the ones that
	// matter. The threshold is a noise policy, not a completeness measure: loss
	// markers are not scoped to the request's filters, so the two counts need
	// not cover the same traffic.
	if lost > 0 && float64(lost)*100 >= (float64(lost)+float64(delivered))*lostEventsWarnPercent {
		gaps = append(gaps, fmt.Sprintf("Hubble reported %d events lost before they could be delivered", lost))
	}
	if len(unavailableNodes) > 0 {
		slices.Sort(unavailableNodes)
		unavailableNodes = slices.Compact(unavailableNodes)
		// Counted, not named: this warning outlives namespace filtering, and a
		// node's name is a cluster-scoped read the viewer may not have.
		log.Printf("[hubble] Relay could not read flows from nodes: %s", strings.Join(unavailableNodes, ", "))
		gaps = append(gaps, fmt.Sprintf("Hubble Relay could not read flows from %d node(s), so their traffic is missing", len(unavailableNodes)))
	}

	log.Printf("[hubble] Retrieved %d flows", len(flows))
	if len(gaps) > 0 {
		result.incomplete = "Traffic data is incomplete: " + strings.Join(gaps, "; ") + "."
		result.streamFailed = streamFailed
	}
	return result, nil
}

// lostEventsWarnPercent is the share of a fetch's events, lost ones included,
// that must have been lost before the loss is worth a warning.
const lostEventsWarnPercent = 1

// callerOrientedFlow converts a Hubble flow and orients it from the caller to
// the callee, reporting false for a flow that should not become an edge at all.
//
// Hubble records packets, so a conversation arrives from both ends: the reply
// packets name the server as the source and the client's ephemeral port as the
// destination port. Taken at face value they draw a second, reversed edge per
// connection — server → client on a port nothing listens on. A reply at L3/L4
// adds nothing the request-direction flow did not already say, so it is
// dropped. An L7 response is the only record carrying the status code and
// latency, so it is turned around onto the request's edge instead, and counts
// no connection of its own.
//
// Only an explicit is_reply=true is acted on. Hubble leaves it unset when it
// cannot tell (drops, encapsulation trace points), and those keep their
// orientation — Hubble's own reply filter would discard them, which is why the
// filter is not pushed into the request.
func callerOrientedFlow(pbFlow *flowpb.Flow) (Flow, bool) {
	flow := convertHubbleFlow(pbFlow)
	if !pbFlow.GetIsReply().GetValue() {
		return flow, true
	}
	if pbFlow.GetL7() == nil {
		return Flow{}, false
	}
	flow.Source, flow.Destination = flow.Destination, flow.Source
	flow.SourceService, flow.DestService = flow.DestService, flow.SourceService
	if l4 := pbFlow.GetL4(); l4 != nil {
		switch {
		case l4.GetTCP() != nil:
			flow.Port = int(l4.GetTCP().GetSourcePort())
		case l4.GetUDP() != nil:
			flow.Port = int(l4.GetUDP().GetSourcePort())
		case l4.GetSCTP() != nil:
			flow.Port = int(l4.GetSCTP().GetSourcePort())
		}
	}
	flow.Connections = 0
	return flow, true
}

// convertHubbleFlow converts a Hubble protobuf Flow to our internal Flow type
func convertHubbleFlow(pbFlow *flowpb.Flow) Flow {
	// Extract IP addresses safely (IP may be nil for some flow types)
	var srcIP, dstIP string
	if ip := pbFlow.GetIP(); ip != nil {
		srcIP = ip.GetSource()
		dstIP = ip.GetDestination()
	}

	flow := Flow{
		Source:      convertEndpoint(pbFlow.GetSource(), srcIP),
		Destination: convertEndpoint(pbFlow.GetDestination(), dstIP),
		Verdict:     strings.ToLower(pbFlow.GetVerdict().String()),
		Connections: 1,
	}

	// Extract L4 info
	l4 := pbFlow.GetL4()
	if l4 != nil {
		if tcp := l4.GetTCP(); tcp != nil {
			flow.Protocol = "tcp"
			flow.Port = int(tcp.GetDestinationPort())
		} else if udp := l4.GetUDP(); udp != nil {
			flow.Protocol = "udp"
			flow.Port = int(udp.GetDestinationPort())
		} else if icmpv4 := l4.GetICMPv4(); icmpv4 != nil {
			flow.Protocol = "icmp"
		} else if icmpv6 := l4.GetICMPv6(); icmpv6 != nil {
			flow.Protocol = "icmpv6"
		} else if sctp := l4.GetSCTP(); sctp != nil {
			flow.Protocol = "sctp"
			flow.Port = int(sctp.GetDestinationPort())
		}
	}

	// Extract L7 info if available
	l7 := pbFlow.GetL7()
	if l7 != nil {
		flow.LatencyNs = l7.GetLatencyNs()
		flow.L7Type = l7.GetType().String()

		if http := l7.GetHttp(); http != nil {
			flow.L7Protocol = "HTTP"
			flow.HTTPMethod = http.GetMethod()
			flow.HTTPPath = http.GetUrl()
			flow.HTTPStatus = int(http.GetCode())
			flow.HTTPProtocol = http.GetProtocol()
			// Extract allowlisted headers only
			for _, h := range http.GetHeaders() {
				key := strings.ToLower(h.GetKey())
				if allowedHTTPHeaders[key] {
					flow.HTTPHeaders = append(flow.HTTPHeaders, h.GetKey()+": "+h.GetValue())
				}
			}
		} else if dns := l7.GetDns(); dns != nil {
			flow.L7Protocol = "DNS"
			flow.DNSQuery = dns.GetQuery()
			if ips := dns.GetIps(); len(ips) > 0 {
				flow.DNSIPs = ips
			}
			flow.DNSTTL = dns.GetTtl()
			flow.DNSRCode = dns.GetRcode()
			if qtypes := dns.GetQtypes(); len(qtypes) > 0 {
				flow.DNSQTypes = qtypes
			}
		}
	}

	// Extract flow-level metadata
	if dir := pbFlow.GetTrafficDirection(); dir != flowpb.TrafficDirection_TRAFFIC_DIRECTION_UNKNOWN {
		flow.TrafficDirection = strings.ToLower(dir.String())
	}
	flow.PolicyVerdict = convertPolicyVerdict(pbFlow)
	if flow.Verdict == "dropped" {
		if reason := pbFlow.GetDropReasonDesc(); reason != flowpb.DropReason_DROP_REASON_UNKNOWN {
			flow.DropReasonDesc = reason.String()
		} else if code := pbFlow.GetDropReason(); code != 0 {
			// Older relays fill only the numeric code; its enum name is the
			// same vocabulary.
			flow.DropReasonDesc = flowpb.DropReason(code).String()
		}
	}
	if svc := pbFlow.GetSourceService(); svc != nil && svc.GetName() != "" {
		flow.SourceService = svc.GetName()
	}
	if svc := pbFlow.GetDestinationService(); svc != nil && svc.GetName() != "" {
		flow.DestService = svc.GetName()
	}

	// Parse timestamp
	if ts := pbFlow.GetTime(); ts != nil {
		flow.LastSeen = ts.AsTime()
	} else {
		flow.LastSeen = time.Now()
	}

	return flow
}

// convertEndpoint converts a Hubble Endpoint to our internal Endpoint type.
//
// The kind is decided on positive evidence only, because a policy
// evaluation draws different conclusions from each: a selector can never
// admit an External endpoint, nothing is decidable about a Host one, and an
// Unknown one must not be mistaken for either.
func convertEndpoint(ep *flowpb.Endpoint, ip string) Endpoint {
	if ep == nil {
		return Endpoint{
			Kind: EndpointKindUnknown,
			IP:   ip,
			Name: ip,
		}
	}

	endpoint := Endpoint{
		Namespace: ep.GetNamespace(),
		IP:        ip,
	}

	if podName := ep.GetPodName(); podName != "" {
		endpoint.Name = podName
		endpoint.Kind = EndpointKindPod
	} else {
		endpoint.Kind, endpoint.Name = classifyNonPodIdentity(ep.GetLabels(), ip)
	}

	// Cilium resolves a pod's owning workload itself; the server prefers its
	// own resolution from the pod cache and falls back to this one for pods it
	// cannot see, such as those already gone.
	if endpoint.Kind == EndpointKindPod {
		if wl := ep.GetWorkloads(); len(wl) > 0 && wl[0].GetName() != "" {
			endpoint.Workload = wl[0].GetName()
			endpoint.WorkloadKind = wl[0].GetKind()
		}
	}

	return endpoint
}

// StreamFlows returns a channel of flows for real-time updates
func (h *HubbleSource) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	flowCh := make(chan Flow, 100)

	go func() {
		defer close(flowCh)

		h.mu.RLock()
		client := h.observerClient
		h.mu.RUnlock()

		if client == nil {
			log.Printf("[hubble] Cannot stream: not connected")
			return
		}

		req := hubbleFlowsRequest(opts, true)

		stream, err := client.GetFlows(ctx, req)
		if err != nil {
			log.Printf("[hubble] Failed to start flow stream: %v", err)
			return
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return // Context cancelled
				}
				log.Printf("[hubble] Stream error: %v", err)
				return
			}

			pbFlow := resp.GetFlow()
			if pbFlow == nil {
				continue
			}

			flow, ok := callerOrientedFlow(pbFlow)
			if !ok {
				continue
			}

			select {
			case flowCh <- flow:
			case <-ctx.Done():
				return
			default:
				// Channel full, drop flow
			}
		}
	}()

	return flowCh, nil
}

// Close cleans up resources
func (h *HubbleSource) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeConnectionLocked()
	h.currentContext = ""
	h.relayNamespace = ""
	h.closed = true
	return nil
}

// GetPortForwardInstructions returns kubectl commands for manual access
func (h *HubbleSource) GetPortForwardInstructions() string {
	h.mu.RLock()
	namespace := h.relayNamespace
	h.mu.RUnlock()

	if namespace == "" {
		namespace = "kube-system"
	}

	return fmt.Sprintf(`To access Hubble flows directly, run:

# Port-forward Hubble Relay (gRPC API)
kubectl -n %s port-forward svc/hubble-relay 4245:80

# Then use Hubble CLI:
hubble observe --server localhost:4245

# Or port-forward Hubble UI (if installed):
kubectl -n %s port-forward svc/hubble-ui 12000:80
# Then open http://localhost:12000`, namespace, namespace)
}

// classifyNonPodIdentity reads Cilium's reserved identity labels for an
// endpoint that is not a pod. Host, remote-node and the API server are the
// cluster's own nodes; world and CIDR identities are outside the cluster;
// anything else (init, health, ingress, unknown) carries no identity a policy
// evaluation could use. Node evidence wins over the whole label set: with
// CIDR matching for nodes enabled, a node's identity carries both a cidr:
// label and its reserved one, and it is still a node.
func classifyNonPodIdentity(labels []string, ip string) (kind, name string) {
	external, reserved := false, ""
	for _, label := range labels {
		switch {
		case label == "reserved:host" || label == "reserved:remote-node" || label == "reserved:kube-apiserver":
			return EndpointKindHost, strings.TrimPrefix(label, "reserved:")
		case label == "reserved:world" || label == "reserved:world-ipv4" || label == "reserved:world-ipv6" || strings.HasPrefix(label, "cidr:"):
			external = true
		case strings.HasPrefix(label, "reserved:") && reserved == "":
			reserved = strings.TrimPrefix(label, "reserved:")
		}
	}
	switch {
	case external:
		return EndpointKindExternal, "world"
	case reserved != "":
		return EndpointKindUnknown, reserved
	}
	return EndpointKindUnknown, ip
}

// convertPolicyVerdict carries over the policies Hubble reports as having
// decided the flow. Nil when it reports none, so a consumer can tell "the
// plugin said nothing" from "the plugin said no policy was involved".
func convertPolicyVerdict(pbFlow *flowpb.Flow) *PolicyVerdict {
	toRefs := func(ps []*flowpb.Policy) []PolicyRef {
		out := make([]PolicyRef, 0, len(ps))
		for _, p := range ps {
			if p == nil || p.GetName() == "" {
				continue
			}
			out = append(out, PolicyRef{Kind: p.GetKind(), Namespace: p.GetNamespace(), Name: p.GetName()})
		}
		return out
	}
	allowed := append(toRefs(pbFlow.GetIngressAllowedBy()), toRefs(pbFlow.GetEgressAllowedBy())...)
	denied := append(toRefs(pbFlow.GetIngressDeniedBy()), toRefs(pbFlow.GetEgressDeniedBy())...)
	if len(allowed) == 0 && len(denied) == 0 {
		return nil
	}
	return &PolicyVerdict{AllowedBy: allowed, DeniedBy: denied}
}
