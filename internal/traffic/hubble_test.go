package traffic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/portforward"
)

type stubObserver struct {
	observerpb.UnimplementedObserverServer
}

func (s *stubObserver) ServerStatus(ctx context.Context, req *observerpb.ServerStatusRequest) (*observerpb.ServerStatusResponse, error) {
	return &observerpb.ServerStatusResponse{}, nil
}

// startStubRelay serves a minimal Hubble observer on 127.0.0.1 and returns its
// port. Pass nil creds for plaintext.
func startStubRelay(t *testing.T, creds credentials.TransportCredentials) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var opts []grpc.ServerOption
	if creds != nil {
		opts = append(opts, grpc.Creds(creds))
	}
	srv := grpc.NewServer(opts...)
	observerpb.RegisterObserverServer(srv, &stubObserver{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().(*net.TCPAddr).Port
}

func relayService(port int, targetPort intstr.IntOrString) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: hubbleRelayService, Namespace: "kube-system"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "127.0.0.1",
			Ports:     []corev1.ServicePort{{Port: int32(port), TargetPort: targetPort}},
		},
	}
}

func TestConnectDirectPlaintext(t *testing.T) {
	port := startStubRelay(t, nil)
	client := fake.NewSimpleClientset(relayService(port, intstr.FromInt(4245)))

	h := NewHubbleSource(client)
	h.relayNamespace = "kube-system"

	info, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !info.Connected {
		t.Fatalf("expected connected, got error: %s", info.Error)
	}
	wantAddr := fmt.Sprintf("127.0.0.1:%d", port)
	if info.Address != wantAddr {
		t.Errorf("Address = %q, want %q", info.Address, wantAddr)
	}
	if info.LocalPort != 0 {
		t.Errorf("LocalPort = %d, want 0 (no port-forward in direct mode)", info.LocalPort)
	}
	if pf := portforward.GetConnectionInfo(portforward.OwnerTraffic); pf.Connected {
		t.Error("direct connect must not involve the port-forward registry")
	}

	// The lazy Service fetch must fill both ports, not just the container port.
	if h.servicePort != port {
		t.Errorf("servicePort = %d, want %d", h.servicePort, port)
	}
	if h.relayPort != 4245 {
		t.Errorf("relayPort = %d, want 4245", h.relayPort)
	}

	// Repeat Connect reuses the live connection and reports the real address,
	// not a reconstructed localhost:0.
	info2, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("repeat Connect: %v", err)
	}
	if !info2.Connected || info2.Address != wantAddr {
		t.Errorf("repeat Connect = {connected:%v addr:%q}, want {true %q}", info2.Connected, info2.Address, wantAddr)
	}

	// Reporter: direct mode is authoritative from stored state.
	ci := h.ConnectionInfo()
	if !ci.Connected || ci.Address != wantAddr {
		t.Errorf("ConnectionInfo = {connected:%v addr:%q}, want {true %q}", ci.Connected, ci.Address, wantAddr)
	}
}

func TestConnectDirectTLSWithSANDiscovery(t *testing.T) {
	cert, pool := selfSignedCert(t, "relay.test.example")
	port := startStubRelay(t, credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}}))
	client := fake.NewSimpleClientset(relayService(port, intstr.FromInt(4245)))

	h := NewHubbleSource(client)
	h.relayNamespace = "kube-system"
	h.useTLS = true
	// Deliberately wrong ServerName: SAN discovery must find the real one.
	h.tlsConfig = &tls.Config{
		RootCAs:    pool,
		ServerName: "hubble-relay.kube-system.svc.cluster.local",
		MinVersion: tls.VersionTLS12,
	}

	info, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !info.Connected {
		t.Fatalf("expected TLS connect via SAN discovery, got error: %s", info.Error)
	}
	if info.LocalPort != 0 {
		t.Errorf("LocalPort = %d, want 0 (direct mode)", info.LocalPort)
	}
}

func TestConnectBothLanesFailReportsBoth(t *testing.T) {
	closedPort := reserveClosedPort(t)
	client := fake.NewSimpleClientset(relayService(closedPort, intstr.FromInt(4245)))

	h := NewHubbleSource(client)
	h.relayNamespace = "kube-system"

	info, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if info.Connected {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"direct connection to", "port-forward fallback failed"} {
		if !strings.Contains(info.Error, want) {
			t.Errorf("error %q missing %q", info.Error, want)
		}
	}
}

func TestComposeConnectErrorForbiddenRemediation(t *testing.T) {
	forbidden := errors.New(`port-forward failed: error upgrading connection: pods "hubble-relay-x" is forbidden: User "system:serviceaccount:radar:radar" cannot create resource "pods/portforward" in API group "" in the namespace "kube-system"`)

	// Unreachable direct lane + forbidden forward: both remediations apply.
	msg := composeConnectError("10.0.0.1:80", errDirectUnreachable, forbidden)
	for _, want := range []string{"rbac.portForward=true", "allow network traffic"} {
		if !strings.Contains(msg, want) {
			t.Errorf("forbidden error %q missing remediation %q", msg, want)
		}
	}

	// Direct lane reached the relay but failed TLS: opening the network path
	// would fix nothing, so only the RBAC remediation applies.
	tlsFailed := composeConnectError("10.0.0.1:80", errors.New("TLS gRPC connection test failed"), forbidden)
	if !strings.Contains(tlsFailed, "rbac.portForward=true") {
		t.Errorf("forbidden error %q missing RBAC remediation", tlsFailed)
	}
	if strings.Contains(tlsFailed, "allow network traffic") {
		t.Errorf("TLS failure should not suggest opening the network path: %q", tlsFailed)
	}

	plain := composeConnectError("10.0.0.1:80", errDirectUnreachable, errors.New("port-forward timed out"))
	if strings.Contains(plain, "rbac.portForward") {
		t.Errorf("non-RBAC failure should not carry RBAC remediation: %q", plain)
	}
}

func TestStaleUnreachableCacheHealsViaDirectRetry(t *testing.T) {
	port := startStubRelay(t, nil)
	client := fake.NewSimpleClientset(relayService(port, intstr.FromInt(4245)))

	h := NewHubbleSource(client)
	h.relayNamespace = "kube-system"
	h.servicePort = port
	h.relayPort = 4245
	h.clusterIP = "127.0.0.1"
	// Stale cached verdict: unreachable, though the relay actually answers —
	// e.g. a background probe that raced relay startup. The port-forward
	// fallback is dead here too (no K8s clients wired), so Connect must
	// retry the direct lane for real instead of failing on the stale cache.
	h.probedAddr = fmt.Sprintf("127.0.0.1:%d", port)
	h.probeReachable = false

	info, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !info.Connected {
		t.Fatalf("stale cache should not defeat a reachable relay: %s", info.Error)
	}
	if info.LocalPort != 0 {
		t.Errorf("LocalPort = %d, want 0 (direct mode)", info.LocalPort)
	}
	h.mu.RLock()
	healed := h.probeReachable
	h.mu.RUnlock()
	if !healed {
		t.Error("successful retry should refresh the cached verdict")
	}
}

func TestProbeGenerationsKeepNewestVerdict(t *testing.T) {
	h := NewHubbleSource(fake.NewSimpleClientset())

	// A probe launches, then an inline observation lands first as a newer
	// generation: the probe's late write must be refused.
	h.mu.Lock()
	h.probeSeq++
	earlyGen := h.probeSeq
	h.recordProbeLocked("10.0.0.1:80", true)
	h.mu.Unlock()

	if h.applyProbeResult(earlyGen, "10.0.0.1:80", false) {
		t.Error("stale probe generation was applied over a newer verdict")
	}
	h.mu.RLock()
	reachable := h.probeReachable
	h.mu.RUnlock()
	if !reachable {
		t.Error("older probe overwrote a newer inline verdict")
	}

	// A probe launched after the inline observation is newer information and
	// must win.
	h.mu.Lock()
	h.probeSeq++
	lateGen := h.probeSeq
	h.mu.Unlock()
	if !h.applyProbeResult(lateGen, "10.0.0.1:80", false) {
		t.Error("newer probe generation was refused")
	}
}

func TestDetectKicksBackgroundProbe(t *testing.T) {
	port := startStubRelay(t, nil)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hubble-relay-abc", Namespace: "kube-system",
			Labels: map[string]string{"k8s-app": "hubble-relay"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewSimpleClientset(pod, relayService(port, intstr.FromInt(4245)))

	h := NewHubbleSource(client)
	result, err := h.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !result.Available {
		t.Fatalf("expected detection, got: %s", result.Message)
	}

	wantAddr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.RLock()
		addr, reachable := h.probedAddr, h.probeReachable
		h.mu.RUnlock()
		if addr == wantAddr {
			if !reachable {
				t.Errorf("probe of live stub reported unreachable")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("Detect did not populate the background probe cache")
}

func TestConnectRefusedAfterClose(t *testing.T) {
	h := NewHubbleSource(fake.NewSimpleClientset())
	h.relayNamespace = "kube-system"
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := h.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if info.Connected || !strings.Contains(info.Error, "closed") {
		t.Errorf("closed source Connect = {connected:%v err:%q}, want refusal", info.Connected, info.Error)
	}

	c := &CarettaSource{}
	if err := c.Close(); err != nil {
		t.Fatalf("caretta Close: %v", err)
	}
	cInfo, err := c.Connect(context.Background(), "test-ctx")
	if err != nil {
		t.Fatalf("caretta Connect: %v", err)
	}
	if cInfo.Connected || !strings.Contains(cInfo.Error, "closed") {
		t.Errorf("closed caretta Connect = {connected:%v err:%q}, want refusal", cInfo.Connected, cInfo.Error)
	}
}

func TestForwardMatches(t *testing.T) {
	live := &portforward.ConnectionInfo{
		Connected: true, Namespace: "kube-system", ServiceName: hubbleRelayService, ContextName: "ctx-a",
	}
	if !forwardMatches(live, "kube-system", hubbleRelayService, "ctx-a") {
		t.Error("matching live forward rejected")
	}
	foreign := &portforward.ConnectionInfo{
		Connected: true, Namespace: "caretta", ServiceName: "caretta-vm", ContextName: "ctx-a",
	}
	if forwardMatches(foreign, "kube-system", hubbleRelayService, "ctx-a") {
		t.Error("another source's forward accepted as hubble's")
	}
	otherCluster := &portforward.ConnectionInfo{
		Connected: true, Namespace: "kube-system", ServiceName: hubbleRelayService, ContextName: "ctx-b",
	}
	if forwardMatches(otherCluster, "kube-system", hubbleRelayService, "ctx-a") {
		t.Error("previous cluster's forward accepted")
	}
	if forwardMatches(&portforward.ConnectionInfo{Connected: false}, "kube-system", hubbleRelayService, "ctx-a") {
		t.Error("dead forward accepted")
	}
}

func TestHubbleConnectionInfoPortForwardModeUsesRegistry(t *testing.T) {
	h := NewHubbleSource(fake.NewSimpleClientset())
	h.isConnected = true
	h.viaPortForward = true
	h.connectedAddr = "localhost:55555"

	// No live forward in the registry: a dead forward must not read as connected.
	if ci := h.ConnectionInfo(); ci.Connected {
		t.Error("dead port-forward reported as connected")
	}
}

func TestHubbleConnectionInfoDisconnected(t *testing.T) {
	h := NewHubbleSource(fake.NewSimpleClientset())
	if ci := h.ConnectionInfo(); ci.Connected {
		t.Error("fresh source reported as connected")
	}
}

func TestManagerGetConnectionInfoUsesReporter(t *testing.T) {
	h := NewHubbleSource(fake.NewSimpleClientset())
	h.isConnected = true
	h.connectedAddr = "10.0.0.7:80"
	h.relayNamespace = "kube-system"

	m := &Manager{activeSource: h}
	ci := m.GetConnectionInfo()
	if !ci.Connected || ci.Address != "10.0.0.7:80" {
		t.Errorf("GetConnectionInfo = {connected:%v addr:%q}, want direct reporter state", ci.Connected, ci.Address)
	}

	// No active source: registry fallback.
	m2 := &Manager{}
	if ci := m2.GetConnectionInfo(); ci.Connected {
		t.Error("empty manager reported as connected")
	}
}

func TestCarettaConnectionInfoModes(t *testing.T) {
	direct := &CarettaSource{
		prometheusAddr:   "http://caretta-vm.caretta.svc.cluster.local:8428",
		metricsNamespace: "caretta",
		metricsService:   "caretta-vm",
		currentContext:   "test-ctx",
	}
	if ci := direct.ConnectionInfo(); !ci.Connected || ci.ServiceName != "caretta-vm" {
		t.Errorf("direct binding = {connected:%v svc:%q}, want {true caretta-vm}", ci.Connected, ci.ServiceName)
	}

	deadForward := &CarettaSource{
		prometheusAddr:   "http://localhost:54321",
		metricsNamespace: "caretta",
		metricsService:   "caretta-vm",
		currentContext:   "test-ctx",
	}
	if ci := deadForward.ConnectionInfo(); ci.Connected {
		t.Error("dead forward binding reported as connected")
	}

	manual := &CarettaSource{
		prometheusAddr: "http://localhost:9090",
		metricsURL:     "http://localhost:9090",
		currentContext: "test-ctx",
	}
	if ci := manual.ConnectionInfo(); !ci.Connected {
		t.Error("manual localhost URL reported as disconnected")
	}

	if ci := (&CarettaSource{}).ConnectionInfo(); ci.Connected {
		t.Error("unbound source reported as connected")
	}
}

func TestResolveRelayPorts(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hubble-relay-abc", Namespace: "kube-system",
			Labels: map[string]string{"k8s-app": "hubble-relay"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "hubble-relay",
			Ports: []corev1.ContainerPort{{Name: "grpc", ContainerPort: 4245}},
		}}},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: hubbleRelayService, Namespace: "kube-system"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.0.10",
			Selector:  map[string]string{"k8s-app": "hubble-relay"},
			Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("grpc")}},
		},
	}
	client := fake.NewSimpleClientset(pod, svc)

	h := NewHubbleSource(client)
	h.relayNamespace = "kube-system"

	got := h.resolveTargetPort(context.Background(), svc)
	if got != 4245 {
		t.Errorf("resolveTargetPort = %d, want 4245 (named port)", got)
	}
}

func TestDirectAddress(t *testing.T) {
	h := &HubbleSource{servicePort: 80, clusterIP: "10.96.0.10"}
	if got := h.directAddressLocked("kube-system"); got != "10.96.0.10:80" {
		t.Errorf("clusterIP address = %q", got)
	}

	// Headless DNS resolves to pod IPs — the container port is the only one
	// that answers there.
	headless := &HubbleSource{servicePort: 443, relayPort: 4245, clusterIP: corev1.ClusterIPNone}
	if got := headless.directAddressLocked("cilium"); got != "hubble-relay.cilium.svc:4245" {
		t.Errorf("headless address = %q", got)
	}
}

func selfSignedCert(t *testing.T, san string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: san},
		DNSNames:              []string{san},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// reserveClosedPort returns a port that briefly listened and is now closed, so
// dialing it is refused immediately.
func reserveClosedPort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	lis.Close()
	return port
}
