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
	runtimeAuthProbeCooldown                = 30 * time.Second
	runtimeAuthInconclusiveProbeCooldown    = 5 * time.Second
	runtimeAuthInconclusiveProbeCooldownMax = 5 * time.Minute
	// Must fit a full TLS handshake + response on high-RTT links: the
	// confirmation probe has already proven the failure within its own 5s
	// budget, and a shorter endpoint budget would let slow-but-healthy
	// networks veto every demotion as "inconclusive" forever.
	runtimeAuthEndpointProbeTimeout = 5 * time.Second

	// Published instead of the raw probe error, which broadcasts to every SSE
	// client and can embed credential material on exec-plugin failure paths.
	runtimeAuthLostMessage     = "Kubernetes credentials for this context are no longer valid; re-authenticate to reconnect"
	runtimeAuthRejectedMessage = "The cluster could not authenticate this request (HTTP 401)"
	// A wedged exec plugin gets its own auth-shaped type so recovery owns it
	// without the UI suggesting that the credentials are known to be invalid.
	runtimeAuthPluginStuckMessage = "The credential plugin for this context stopped responding; Kubernetes requests cannot be authenticated until it recovers"
)

var (
	clientGenerationCounter atomic.Uint64
	// Guarded by clientMu (written in doInit/SwitchContext, read via
	// runtimeAuthStateIsCurrent) — not an atomic like its neighbors.
	activeClientGeneration  atomic.Uint64
	discoveryRequestTimeout = 32 * time.Second

	runtimeAuthChecksMu sync.Mutex
	runtimeAuthChecks   = make(map[uint64]struct{})
	// Cooldown for the single generation that can currently report (older
	// generations are rejected by runtimeAuthCandidateIsCurrent, so one
	// scalar pair suffices where a per-generation map would only accrete
	// garbage).
	runtimeAuthCooldownGeneration uint64
	runtimeAuthProbeNotBefore     time.Time
	runtimeAuthInconclusiveStreak int
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

// isAuthClassification reports whether a classification is authentication-shaped.
func isAuthClassification(classification string) bool {
	return classification == "auth" || classification == "auth-rejected" || classification == "auth-plugin-stuck"
}

func (rt *runtimeAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.next.RoundTrip(req)
	// Two distinct shapes of credential loss: transport errors carry exec
	// plugin failures (client-go surfaces them before any request is sent),
	// while an expired-but-presentable token comes back as a plain 401
	// response — at this layer it is not an error. 403 is deliberately not a
	// candidate: RBAC denial is not credential loss.
	if err != nil && isAuthClassification(ClassifyError(err)) {
		reportRuntimeAuthFailure(rt.generation, err)
	} else if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		// This synthetic message must keep classifying as auth-shaped
		// (isAuthRejectedMessage matches "unauthorized") or the report gate
		// below drops it silently.
		reportRuntimeAuthFailure(rt.generation, errors.New("Kubernetes API returned HTTP 401 Unauthorized"))
	}
	return resp, err
}

func reportRuntimeAuthFailure(generation uint64, candidate error) {
	if candidate == nil || !isAuthClassification(ClassifyError(candidate)) || !runtimeAuthCandidateIsCurrent(generation) {
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
	classification := ClassifyError(err)
	// A hung exec plugin never produces an auth-classified error — the fresh
	// probe times out inside the plugin instead ("auth plugin timeout", the
	// exec-specific deadline branch). With the endpoint still reachable that
	// IS a credential-system failure: without this, a wedged plugin keeps the
	// UI "connected" while every request hangs behind client-go's shared
	// plugin mutex.
	hungPlugin := classification == "timeout" && err != nil && UsesExecAuth() &&
		strings.Contains(err.Error(), "auth plugin timeout")
	if !isAuthClassification(classification) && !hungPlugin {
		setRuntimeAuthCooldown(generation, runtimeAuthCooldown(err), true)
		if err == nil {
			log.Printf("[k8s] Runtime authentication candidate dismissed for context %q: credentials accepted by a fresh probe",
				SanitizeForLog(GetContextName()))
		} else {
			log.Printf("[k8s] Runtime authentication candidate dismissed for context %q: confirmation classified %q",
				SanitizeForLog(GetContextName()), classification)
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
		// Escalating cooldown: an offline laptop otherwise re-runs the exec
		// plugin every ~5s indefinitely, and a MITM that 401s credentialed
		// requests while dropping the anonymous probe could pin that loop
		// forever.
		setRuntimeAuthCooldown(generation, nextInconclusiveCooldown(), false)
		log.Printf("[k8s] Runtime authentication candidate inconclusive for context %q: API endpoint is unreachable: %v",
			SanitizeForLog(GetContextName()), SanitizeForLog(endpointErr.Error()))
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
	// A hung plugin's classification is "timeout". Publish a distinct auth-shaped
	// type so recovery ownership engages without showing generic re-auth guidance.
	demotedType := classification
	if hungPlugin {
		demotedType = "auth-plugin-stuck"
	}
	if !transitionConnectedToRuntimeAuthFailure(demotedType) {
		return
	}
	// Demotion is a conclusive outcome: the next episode's inconclusive
	// probes must restart from the short cooldown, not inherit this
	// episode's escalated one.
	resetInconclusiveStreak()

	// The whole teardown below runs while holding contextOpMu: connection-change
	// and before-switch callbacks must never take contextOpMu or trigger a
	// synchronous context operation, or this deadlocks.
	currentContext := GetContextName()
	// The raw error stays in the log only — exec-plugin failures can embed
	// credential material (a malformed plugin's stdout is quoted verbatim by
	// client-go), and the published status broadcasts to every SSE client.
	demotionErr := err
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		demotionErr = unwrapped
	}
	log.Printf("[k8s] Runtime Kubernetes authentication lost for context %q (%s); stopping cluster-backed work",
		SanitizeForLog(currentContext), SanitizeForLog(demotionErr.Error()))
	// Quiesce-in-place: fired with the current context so registered
	// consumers cancel cluster-bound work; no actual switch follows.
	notifyBeforeContextSwitch(currentContext)
	CancelOngoingOperations()
	stopActiveSessions()
	ResetAllSubsystems()
	startRuntimeAuthRecovery()
}

func setRuntimeAuthCooldown(generation uint64, cooldown time.Duration, conclusive bool) {
	runtimeAuthChecksMu.Lock()
	runtimeAuthCooldownGeneration = generation
	runtimeAuthProbeNotBefore = time.Now().Add(cooldown)
	if conclusive {
		runtimeAuthInconclusiveStreak = 0
	}
	runtimeAuthChecksMu.Unlock()
}

func nextInconclusiveCooldown() time.Duration {
	runtimeAuthChecksMu.Lock()
	defer runtimeAuthChecksMu.Unlock()
	cooldown := min(runtimeAuthInconclusiveProbeCooldown<<runtimeAuthInconclusiveStreak, runtimeAuthInconclusiveProbeCooldownMax)
	if runtimeAuthInconclusiveStreak < 30 {
		runtimeAuthInconclusiveStreak++
	}
	return cooldown
}

func resetInconclusiveStreak() {
	runtimeAuthChecksMu.Lock()
	runtimeAuthInconclusiveStreak = 0
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
	isCurrent := !isInCluster && activeClientGeneration.Load() == generation
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
		// A TLS alert IS a response from the endpoint: an mTLS-terminating
		// proxy (Teleport, nginx with client verification) rejects this
		// credential-free probe at the handshake, and treating that as
		// unreachable would veto every demotion behind such a proxy forever.
		if strings.Contains(strings.ToLower(err.Error()), "remote error: tls:") {
			return nil
		}
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// The published Error stays fixed for each auth variant; raw probe errors can
// embed credential material and this status broadcasts to every SSE client.
func transitionConnectedToRuntimeAuthFailure(classification string) bool {
	connectionStatusMu.Lock()
	if connectionStatus.State != StateConnected {
		connectionStatusMu.Unlock()
		return false
	}
	message := runtimeAuthLostMessage
	switch classification {
	case "auth-plugin-stuck":
		message = runtimeAuthPluginStuckMessage
	case "auth-rejected":
		message = runtimeAuthRejectedMessage
	}
	status := ConnectionStatus{
		State:       StateDisconnected,
		Context:     connectionStatus.Context,
		ClusterName: connectionStatus.ClusterName,
		Error:       message,
		ErrorType:   classification,
	}
	connectionStatus = status
	connectionStatusMu.Unlock()

	runtimeAuthRecoveryOwed.Store(true)
	notifyConnectionChange(status)
	return true
}
