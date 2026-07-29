package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type sharedKubernetesClients struct {
	clientset  *kubernetes.Clientset
	discovery  *discovery.DiscoveryClient
	dynamic    dynamic.Interface
	generation uint64
}

const (
	runtimeAuthProbeCooldown             = 30 * time.Second
	runtimeAuthInconclusiveProbeCooldown = 5 * time.Second
	// Must fit a full TLS handshake + response on high-RTT links: the
	// confirmation probe has already proven the failure within its own 5s
	// budget, and a shorter endpoint budget would let slow-but-healthy
	// networks veto every demotion as "inconclusive" forever.
	runtimeAuthEndpointProbeTimeout = 5 * time.Second
)

var (
	clientGenerationCounter atomic.Uint64
	// Guarded by clientMu (written in doInit/SwitchContext, read via
	// runtimeAuthStateIsCurrent) — not an atomic like its neighbors.
	activeClientGeneration  uint64
	discoveryRequestTimeout = 32 * time.Second

	runtimeAuthChecksMu sync.Mutex
	runtimeAuthChecks   = make(map[uint64]struct{})
	// Cooldown for the single generation that can currently report (older
	// generations are rejected by runtimeAuthCandidateIsCurrent, so one
	// scalar pair suffices where a per-generation map would only accrete
	// garbage).
	runtimeAuthCooldownGeneration uint64
	runtimeAuthProbeNotBefore     time.Time
	runtimeAuthProbe              = TestClusterConnection
	runtimeAuthEndpointProbe      = defaultRuntimeAuthEndpointProbe
)

func newSharedKubernetesClients(config *rest.Config) (*sharedKubernetesClients, error) {
	clientConfig := rest.CopyConfig(config)
	if clientConfig.UserAgent == "" {
		clientConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	httpClient, err := rest.HTTPClientFor(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes HTTP client: %w", err)
	}

	generation := clientGenerationCounter.Add(1)
	// Struct copy matters: rest.HTTPClientFor returns http.DefaultClient
	// itself for default transports, and swapping Transport in place would
	// mutate the process-wide client.
	observedClient := *httpClient
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	observedClient.Transport = &runtimeAuthRoundTripper{
		next:       transport,
		generation: generation,
	}
	// The discovery client needs its own shallow copy: client-go applies its
	// 32s discovery default only when it builds the http.Client itself, so a
	// caller-supplied client must replicate it — and the Timeout must NOT land
	// on the shared observedClient, where it would kill long-lived watch and
	// exec streams.
	observedDiscoveryClient := observedClient
	if observedDiscoveryClient.Timeout == 0 {
		observedDiscoveryClient.Timeout = discoveryRequestTimeout
	}

	clientset, err := kubernetes.NewForConfigAndClient(clientConfig, &observedClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s clientset: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(clientConfig, &observedDiscoveryClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfigAndClient(clientConfig, &observedClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &sharedKubernetesClients{
		clientset:  clientset,
		discovery:  discoveryClient,
		dynamic:    dynamicClient,
		generation: generation,
	}, nil
}

type runtimeAuthRoundTripper struct {
	next       http.RoundTripper
	generation uint64
}

func (rt *runtimeAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.next.RoundTrip(req)
	// Two distinct shapes of credential loss: transport errors carry exec
	// plugin failures (client-go surfaces them before any request is sent),
	// while an expired-but-presentable token comes back as a plain 401
	// response — at this layer it is not an error. 403 is deliberately not a
	// candidate: RBAC denial is not credential loss.
	if err != nil && ClassifyError(err) == "auth" {
		reportRuntimeAuthFailure(rt.generation, err)
	} else if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		// This synthetic message must keep classifying as "auth"
		// (isAuthErrorMessage matches "unauthorized") or the report gate
		// below drops it silently.
		reportRuntimeAuthFailure(rt.generation, errors.New("Kubernetes API returned HTTP 401 Unauthorized"))
	}
	return resp, err
}

func reportRuntimeAuthFailure(generation uint64, candidate error) {
	if candidate == nil || ClassifyError(candidate) != "auth" || !runtimeAuthCandidateIsCurrent(generation) {
		return
	}

	operationGeneration := currentOperationGen()
	runtimeAuthChecksMu.Lock()
	if _, exists := runtimeAuthChecks[generation]; exists {
		runtimeAuthChecksMu.Unlock()
		return
	}
	if runtimeAuthCooldownGeneration == generation && time.Now().Before(runtimeAuthProbeNotBefore) {
		runtimeAuthChecksMu.Unlock()
		return
	}
	runtimeAuthChecks[generation] = struct{}{}
	runtimeAuthChecksMu.Unlock()

	go confirmRuntimeAuthFailure(generation, operationGeneration)
}

func confirmRuntimeAuthFailure(generation, operationGeneration uint64) {
	defer func() {
		runtimeAuthChecksMu.Lock()
		delete(runtimeAuthChecks, generation)
		runtimeAuthChecksMu.Unlock()
	}()

	if !runtimeAuthCandidateIsCurrent(generation) || currentOperationGen() != operationGeneration {
		return
	}

	ctx, cancel, current := newOperationContextForGeneration(operationGeneration, connectionTestOperationTimeout())
	if !current {
		return
	}
	probe := getRuntimeAuthProbe()
	err := probe(ctx)
	cancel()
	if ClassifyError(err) != "auth" {
		setRuntimeAuthCooldown(generation, runtimeAuthCooldown(err))
		if err == nil {
			log.Printf("[k8s] Runtime authentication candidate dismissed for context %q: credentials accepted by a fresh probe",
				SanitizeForLog(GetContextName()))
		} else {
			log.Printf("[k8s] Runtime authentication candidate dismissed for context %q: confirmation classified %q",
				SanitizeForLog(GetContextName()), ClassifyError(err))
		}
		return
	}

	// Second gate: the anonymous endpoint probe separates "credentials died"
	// from "machine is offline" — an unreachable IdP makes some exec plugin
	// failures classify as auth, and demoting on those would tear down a
	// healthy session over a VPN blip.
	endpointConfig := GetConfig()
	endpointCtx, endpointCancel, current := newOperationContextForGeneration(operationGeneration, runtimeAuthEndpointProbeTimeout)
	if !current {
		return
	}
	endpointErr := getRuntimeAuthEndpointProbe()(endpointCtx, endpointConfig)
	endpointCancel()
	if endpointErr != nil {
		setRuntimeAuthCooldown(generation, runtimeAuthInconclusiveProbeCooldown)
		log.Printf("[k8s] Runtime authentication candidate inconclusive for context %q: API endpoint is unreachable: %v",
			SanitizeForLog(GetContextName()), endpointErr)
		return
	}

	contextOpMu.Lock()
	defer contextOpMu.Unlock()

	// Deliberately the WEAKER state check here (no activeContextOperations
	// gate, unlike candidate intake): a queued context operation is blocked on
	// contextOpMu right now and cannot abort a demotion that already holds the
	// lock — the generation and operation-epoch re-checks are what invalidate
	// a stale demotion.
	if !runtimeAuthStateIsCurrent(generation) || currentOperationGen() != operationGeneration {
		return
	}
	// The probe wraps every failure as "cluster unreachable: %w" — exactly the
	// wrong headline for a credential failure, so surface the underlying error.
	demotionErr := err
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		demotionErr = unwrapped
	}
	if !transitionConnectedToRuntimeAuthFailure(demotionErr) {
		return
	}

	// The whole teardown below runs while holding contextOpMu: connection-change
	// and before-switch callbacks must never take contextOpMu or trigger a
	// synchronous context operation, or this deadlocks.
	currentContext := GetContextName()
	log.Printf("[k8s] Runtime Kubernetes authentication lost for context %q; stopping cluster-backed work", SanitizeForLog(currentContext))
	// Quiesce-in-place: fired with the current context so registered
	// consumers cancel cluster-bound work; no actual switch follows.
	notifyBeforeContextSwitch(currentContext)
	CancelOngoingOperations()
	stopActiveSessions()
	ResetAllSubsystems()
	startRuntimeAuthRecovery(currentContext)
}

func setRuntimeAuthCooldown(generation uint64, cooldown time.Duration) {
	runtimeAuthChecksMu.Lock()
	runtimeAuthCooldownGeneration = generation
	runtimeAuthProbeNotBefore = time.Now().Add(cooldown)
	runtimeAuthChecksMu.Unlock()
}

func runtimeAuthCooldown(err error) time.Duration {
	classification := ClassifyError(err)
	if err == nil || classification == "rbac" {
		return runtimeAuthProbeCooldown
	}
	return runtimeAuthInconclusiveProbeCooldown
}

// runtimeAuthCandidateIsCurrent gates candidate INTAKE: a queued or in-flight
// context operation either explains the auth failures or is about to replace
// the client, so new candidates are refused while one is pending. Demotion
// commit uses the weaker runtimeAuthStateIsCurrent instead — see
// confirmRuntimeAuthFailure.
func runtimeAuthCandidateIsCurrent(generation uint64) bool {
	if activeContextOperations.Load() != 0 {
		return false
	}
	return runtimeAuthStateIsCurrent(generation)
}

func runtimeAuthStateIsCurrent(generation uint64) bool {
	clientMu.RLock()
	isInCluster := isInClusterLocked()
	// In-cluster SA tokens are refreshed from disk by client-go, so
	// credential loss there is transient by construction — demotion is a
	// kubeconfig-mode concern only.
	isCurrent := !isInCluster && activeClientGeneration == generation
	clientMu.RUnlock()
	return isCurrent && GetConnectionStatus().State == StateConnected
}

func getRuntimeAuthProbe() func(context.Context) error {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	return runtimeAuthProbe
}

func setRuntimeAuthProbe(probe func(context.Context) error) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	runtimeAuthProbe = probe
}

func getRuntimeAuthEndpointProbe() func(context.Context, *rest.Config) error {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	return runtimeAuthEndpointProbe
}

func setRuntimeAuthEndpointProbe(probe func(context.Context, *rest.Config) error) {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	runtimeAuthEndpointProbe = probe
}

func defaultRuntimeAuthEndpointProbe(ctx context.Context, config *rest.Config) error {
	if config == nil {
		return errors.New("Kubernetes config is not initialized")
	}
	rawHost := strings.TrimSpace(config.Host)
	if rawHost == "" {
		return errors.New("Kubernetes API host is empty")
	}
	if !strings.Contains(rawHost, "://") {
		rawHost = "https://" + rawHost
	}
	endpoint, err := url.Parse(rawHost)
	if err != nil || endpoint.Hostname() == "" {
		return fmt.Errorf("invalid Kubernetes API host %q", config.Host)
	}

	// Strip every credential carrier so this is a pure reachability check —
	// including client certs and transport wrappers, which CopyConfig carries
	// over. Only the CA trust settings survive.
	probeConfig := rest.CopyConfig(config)
	probeConfig.Host = strings.TrimRight(rawHost, "/")
	probeConfig.Username = ""
	probeConfig.Password = ""
	probeConfig.BearerToken = ""
	probeConfig.BearerTokenFile = ""
	probeConfig.AuthProvider = nil
	probeConfig.ExecProvider = nil
	probeConfig.Impersonate = rest.ImpersonationConfig{}
	probeConfig.WrapTransport = nil
	probeConfig.Transport = nil
	probeConfig.TLSClientConfig.CertData = nil
	probeConfig.TLSClientConfig.KeyData = nil
	probeConfig.TLSClientConfig.CertFile = ""
	probeConfig.TLSClientConfig.KeyFile = ""
	probeConfig.Timeout = runtimeAuthEndpointProbeTimeout
	httpClient, err := rest.HTTPClientFor(probeConfig)
	if err != nil {
		return err
	}
	// Any response — 401, or a 30x from an auth proxy fronting the apiserver —
	// proves reachability. Following the redirect instead could chase a dead
	// login portal and misread a reachable endpoint as unreachable.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeConfig.Host+"/version", nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func transitionConnectedToRuntimeAuthFailure(err error) bool {
	connectionStatusMu.Lock()
	if connectionStatus.State != StateConnected {
		connectionStatusMu.Unlock()
		return false
	}
	status := ConnectionStatus{
		State:       StateDisconnected,
		Context:     connectionStatus.Context,
		ClusterName: connectionStatus.ClusterName,
		Error:       err.Error(),
		ErrorType:   "auth",
	}
	connectionStatus = status
	connectionStatusMu.Unlock()

	notifyConnectionChange(status)
	return true
}
