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
	runtimeAuthEndpointProbeTimeout      = 2 * time.Second
)

var (
	clientGenerationCounter atomic.Uint64
	activeClientGeneration  uint64
	discoveryRequestTimeout = 32 * time.Second

	runtimeAuthChecksMu      sync.Mutex
	runtimeAuthChecks        = make(map[uint64]struct{})
	runtimeAuthProbeAfter    = make(map[uint64]time.Time)
	runtimeAuthProbe         = TestClusterConnection
	runtimeAuthEndpointProbe = defaultRuntimeAuthEndpointProbe
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
	observedClient := *httpClient
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	observedClient.Transport = &runtimeAuthRoundTripper{
		next:       transport,
		generation: generation,
	}
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
	if err != nil && ClassifyError(err) == "auth" {
		reportRuntimeAuthFailure(rt.generation, err)
	} else if resp != nil && resp.StatusCode == http.StatusUnauthorized {
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
	if nextProbe, exists := runtimeAuthProbeAfter[generation]; exists && time.Now().Before(nextProbe) {
		runtimeAuthChecksMu.Unlock()
		return
	}
	for staleGeneration := range runtimeAuthProbeAfter {
		if staleGeneration != generation {
			delete(runtimeAuthProbeAfter, staleGeneration)
		}
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
		runtimeAuthChecksMu.Lock()
		runtimeAuthProbeAfter[generation] = time.Now().Add(runtimeAuthCooldown(err))
		runtimeAuthChecksMu.Unlock()
		log.Printf("[k8s] Runtime authentication candidate dismissed for context %q: confirmation classified %q",
			SanitizeForLog(GetContextName()), ClassifyError(err))
		return
	}

	endpointConfig, _ := GetConfigSnapshot()
	endpointCtx, endpointCancel, current := newOperationContextForGeneration(operationGeneration, runtimeAuthEndpointProbeTimeout)
	if !current {
		return
	}
	endpointErr := getRuntimeAuthEndpointProbe()(endpointCtx, endpointConfig)
	endpointCancel()
	if endpointErr != nil {
		runtimeAuthChecksMu.Lock()
		runtimeAuthProbeAfter[generation] = time.Now().Add(runtimeAuthInconclusiveProbeCooldown)
		runtimeAuthChecksMu.Unlock()
		log.Printf("[k8s] Runtime authentication candidate inconclusive for context %q: API endpoint is unreachable: %v",
			SanitizeForLog(GetContextName()), endpointErr)
		return
	}

	contextOpMu.Lock()
	defer contextOpMu.Unlock()

	if !runtimeAuthStateIsCurrent(generation) || currentOperationGen() != operationGeneration {
		return
	}
	if !transitionConnectedToRuntimeAuthFailure(err) {
		return
	}

	currentContext := GetContextName()
	log.Printf("[k8s] Runtime Kubernetes authentication lost for context %q; stopping cluster-backed work", SanitizeForLog(currentContext))
	notifyBeforeContextSwitch(currentContext)
	CancelOngoingOperations()
	stopActiveSessions()
	ResetAllSubsystems()
}

func runtimeAuthCooldown(err error) time.Duration {
	classification := ClassifyError(err)
	if err == nil || classification == "rbac" {
		return runtimeAuthProbeCooldown
	}
	return runtimeAuthInconclusiveProbeCooldown
}

func runtimeAuthCandidateIsCurrent(generation uint64) bool {
	if activeContextOperations.Load() != 0 {
		return false
	}
	return runtimeAuthStateIsCurrent(generation)
}

func runtimeAuthStateIsCurrent(generation uint64) bool {
	clientMu.RLock()
	isInCluster := isInClusterLocked()
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

	probeConfig := rest.CopyConfig(config)
	probeConfig.Host = strings.TrimRight(rawHost, "/")
	probeConfig.Username = ""
	probeConfig.Password = ""
	probeConfig.BearerToken = ""
	probeConfig.BearerTokenFile = ""
	probeConfig.AuthProvider = nil
	probeConfig.ExecProvider = nil
	probeConfig.Impersonate = rest.ImpersonationConfig{}
	probeConfig.Timeout = runtimeAuthEndpointProbeTimeout
	httpClient, err := rest.HTTPClientFor(probeConfig)
	if err != nil {
		return err
	}
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
