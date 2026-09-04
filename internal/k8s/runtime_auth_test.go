package k8s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestRuntimeAuthRoundTripperPreservesResults(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		wantErr := errors.New("getting credentials: exec: executable plugin failed with exit code 1")
		rt := &runtimeAuthRoundTripper{
			next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, wantErr
			}),
			generation: 99,
		}

		resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "https://cluster.test/version", nil))
		if resp != nil || !errors.Is(err, wantErr) {
			t.Fatalf("RoundTrip() = (%v, %v), want (nil, original error)", resp, err)
		}
	})

	t.Run("401 response", func(t *testing.T) {
		wantResp := &http.Response{StatusCode: http.StatusUnauthorized}
		rt := &runtimeAuthRoundTripper{
			next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return wantResp, nil
			}),
			generation: 99,
		}

		resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "https://cluster.test/version", nil))
		if resp != wantResp || err != nil {
			t.Fatalf("RoundTrip() = (%p, %v), want (%p, nil)", resp, err, wantResp)
		}
	})
}

func TestRuntimeAuthRoundTripperIgnoresForbidden(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("unauthorized")
	})
	rt := &runtimeAuthRoundTripper{
		next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden}, nil
		}),
		generation: generation,
	}

	resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "https://cluster.test/version", nil))
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("RoundTrip() = (%v, %v), want original 403 response", resp, err)
	}
	if probes.Load() != 0 {
		t.Fatalf("confirmation probes = %d, want 0", probes.Load())
	}
}

func TestRuntimeAuthRoundTripperReportsUnauthorizedResponse(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("unauthorized")
	})
	rt := &runtimeAuthRoundTripper{
		next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnauthorized}, nil
		}),
		generation: generation,
	}

	resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "https://cluster.test/version", nil))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("RoundTrip() = (%v, %v), want original 401 response", resp, err)
	}
	waitForRuntimeAuthCheck(t, generation)
	if probes.Load() != 1 {
		t.Fatalf("confirmation probes = %d, want 1", probes.Load())
	}
	status := GetConnectionStatus()
	if status.State != StateDisconnected || status.ErrorType != "auth-rejected" {
		t.Fatalf("connection status = %+v, want disconnected auth-rejected", status)
	}
	if status.Error != runtimeAuthRejectedMessage {
		t.Fatalf("published error = %q, want the fixed identity-rejected message (raw probe errors can embed credential material)", status.Error)
	}
	if !runtimeAuthRecoveryOwed.Load() {
		t.Fatal("demotion did not record the recovery debt")
	}
	waitForRecoveryWorker(t)
}

func waitForRecoveryWorker(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !runtimeAuthRecoveryActive.Load() {
		if time.Now().After(deadline) {
			t.Fatal("recovery worker did not start")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSharedKubernetesClientsPreserveConfiguredTransport(t *testing.T) {
	var wrappedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gitVersion":"v1.35.0"}`))
	}))
	t.Cleanup(server.Close)

	config := &rest.Config{
		Host: server.URL,
		WrapTransport: func(next http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				wrappedCalls.Add(1)
				return next.RoundTrip(req)
			})
		},
	}
	clients, err := newSharedKubernetesClients(config)
	if err != nil {
		t.Fatalf("newSharedKubernetesClients() error = %v", err)
	}

	if _, err := clients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background()); err != nil {
		t.Fatalf("GET /version error = %v", err)
	}
	if wrappedCalls.Load() != 1 {
		t.Fatalf("configured transport calls = %d, want 1", wrappedCalls.Load())
	}
}

func TestSharedKubernetesClientsPreserveUserAgent(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{name: "default", want: rest.DefaultKubernetesUserAgent()},
		{name: "configured", configured: "radar-runtime-auth-test", want: "radar-runtime-auth-test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userAgent := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				userAgent <- r.Header.Get("User-Agent")
				_, _ = w.Write([]byte(`{"gitVersion":"v1.35.0"}`))
			}))
			t.Cleanup(server.Close)

			clients, err := newSharedKubernetesClients(&rest.Config{
				Host:      server.URL,
				UserAgent: tt.configured,
			})
			if err != nil {
				t.Fatalf("newSharedKubernetesClients() error = %v", err)
			}
			if _, err := clients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background()); err != nil {
				t.Fatalf("GET /version error = %v", err)
			}
			if got := <-userAgent; got != tt.want {
				t.Fatalf("User-Agent = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSharedKubernetesClientsPreserveDiscoveryTimeout(t *testing.T) {
	previousTimeout := discoveryRequestTimeout
	discoveryRequestTimeout = 25 * time.Millisecond
	t.Cleanup(func() { discoveryRequestTimeout = previousTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * discoveryRequestTimeout)
		_, _ = w.Write([]byte(`{"gitVersion":"v1.35.0"}`))
	}))
	t.Cleanup(server.Close)

	clients, err := newSharedKubernetesClients(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("newSharedKubernetesClients() error = %v", err)
	}

	if _, err := clients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background()); err != nil {
		t.Fatalf("clientset request unexpectedly inherited discovery timeout: %v", err)
	}
	if _, err := clients.discovery.RESTClient().Get().AbsPath("/version").DoRaw(context.Background()); err == nil {
		t.Fatal("discovery request did not honor its default timeout")
	}
}

func TestSharedKubernetesClientsDoNotMutateDefaultHTTPClient(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	clients, err := newSharedKubernetesClients(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("newSharedKubernetesClients() error = %v", err)
	}
	if clients == nil {
		t.Fatal("newSharedKubernetesClients() returned nil")
	}
	if http.DefaultClient.Transport != originalTransport {
		t.Fatal("newSharedKubernetesClients() mutated http.DefaultClient.Transport")
	}
}

func TestRuntimeAuthFailureRequiresPersistentAuthFailure(t *testing.T) {
	tests := []struct {
		name     string
		probeErr error
	}{
		{name: "credential refresh succeeds"},
		{name: "forbidden", probeErr: apierrors.NewForbidden(clientcmdapi.SchemeGroupVersion.WithResource("pods").GroupResource(), "", errors.New("denied"))},
		{name: "network", probeErr: errors.New("dial tcp: connection refused")},
		{name: "timeout", probeErr: context.DeadlineExceeded},
		{name: "tls", probeErr: x509.UnknownAuthorityError{}},
		{name: "unknown", probeErr: errors.New("temporary server failure")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generation := prepareRuntimeAuthTest(t)
			probeReturned := make(chan struct{})
			setRuntimeAuthProbe(func(context.Context) error {
				defer close(probeReturned)
				return tt.probeErr
			})

			reportRuntimeAuthFailure(generation, errors.New("Kubernetes API returned HTTP 401 Unauthorized"))
			select {
			case <-probeReturned:
			case <-time.After(2 * time.Second):
				t.Fatal("runtime auth confirmation probe did not run")
			}
			waitForRuntimeAuthCheck(t, generation)

			if got := GetConnectionStatus().State; got != StateConnected {
				t.Fatalf("connection state = %q, want %q", got, StateConnected)
			}
		})
	}
}

func TestRuntimeAuthFailureThrottlesDismissedCandidates(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return nil
	})

	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)
	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)

	if probes.Load() != 1 {
		t.Fatalf("confirmation probes = %d, want 1 during cooldown", probes.Load())
	}
}

func TestRuntimeAuthCooldown(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{name: "credential refresh succeeds", want: runtimeAuthProbeCooldown},
		{
			name: "credential accepted but forbidden",
			err:  apierrors.NewForbidden(clientcmdapi.SchemeGroupVersion.WithResource("pods").GroupResource(), "", errors.New("denied")),
			want: runtimeAuthProbeCooldown,
		},
		{name: "network is inconclusive", err: errors.New("dial tcp: connection refused"), want: runtimeAuthInconclusiveProbeCooldown},
		{name: "timeout is inconclusive", err: context.DeadlineExceeded, want: runtimeAuthInconclusiveProbeCooldown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runtimeAuthCooldown(tt.err); got != tt.want {
				t.Fatalf("runtimeAuthCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRuntimeAuthFailureDemotesAndQuiescesOnce(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("getting credentials: exec: executable gke-gcloud-auth-plugin failed with exit code 1")
	})

	var callbacks atomic.Int32
	OnConnectionChange(func(status ConnectionStatus) {
		if status.State == StateDisconnected {
			callbacks.Add(1)
		}
	})
	var beforeSwitchCalls atomic.Int32
	var beforeSwitchArg atomic.Value
	OnBeforeContextSwitch(func(contextName string) {
		beforeSwitchCalls.Add(1)
		beforeSwitchArg.Store(contextName)
	})
	sessionStopped := make(chan struct{}, 1)
	SetSessionStopper(func() {
		select {
		case sessionStopped <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() { SetSessionStopper(nil) })
	genBefore := currentOperationGen()

	var callers sync.WaitGroup
	for range 20 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			reportRuntimeAuthFailure(generation, errors.New("getting credentials: exec plugin failed"))
		}()
	}
	callers.Wait()

	select {
	case <-sessionStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime auth teardown did not stop sessions")
	}
	waitForRuntimeAuthCheck(t, generation)

	status := GetConnectionStatus()
	if status.State != StateDisconnected || status.ErrorType != "auth" {
		t.Fatalf("connection status = %+v, want disconnected auth", status)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("disconnected callbacks = %d, want 1", callbacks.Load())
	}
	if probes.Load() != 1 {
		t.Fatalf("confirmation probes = %d, want 1", probes.Load())
	}
	// Quiesce-in-place contract: fired exactly once, with the CURRENT context
	// (consumers must not infer "changed" by comparing against the active name).
	if beforeSwitchCalls.Load() != 1 {
		t.Fatalf("before-switch callbacks = %d, want 1", beforeSwitchCalls.Load())
	}
	if got := beforeSwitchArg.Load(); got != GetContextName() {
		t.Fatalf("before-switch context = %v, want the current context %q", got, GetContextName())
	}
	if currentOperationGen() == genBefore {
		t.Fatal("demotion did not cancel ongoing operations (operation generation unchanged)")
	}
}

func TestRuntimeAuthFailureKeepsStateWhenEndpointIsUnreachable(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	setRuntimeAuthProbe(func(context.Context) error {
		return errors.New("getting credentials: exec plugin failed")
	})
	var endpointProbes atomic.Int32
	setRuntimeAuthEndpointProbe(func(context.Context, *rest.Config) error {
		endpointProbes.Add(1)
		return errors.New("dial tcp: network is unreachable")
	})
	var sessionsStopped atomic.Int32
	SetSessionStopper(func() { sessionsStopped.Add(1) })
	t.Cleanup(func() { SetSessionStopper(nil) })

	reportRuntimeAuthFailure(generation, errors.New("getting credentials: exec plugin failed"))
	waitForRuntimeAuthCheck(t, generation)

	if got := GetConnectionStatus().State; got != StateConnected {
		t.Fatalf("connection state = %q, want %q", got, StateConnected)
	}
	if endpointProbes.Load() != 1 {
		t.Fatalf("endpoint probes = %d, want 1", endpointProbes.Load())
	}
	if sessionsStopped.Load() != 0 {
		t.Fatalf("session stops = %d, want 0", sessionsStopped.Load())
	}
	runtimeAuthChecksMu.Lock()
	cooldownGeneration := runtimeAuthCooldownGeneration
	nextProbe := runtimeAuthProbeNotBefore
	runtimeAuthChecksMu.Unlock()
	if cooldownGeneration != generation {
		t.Fatalf("cooldown generation = %d, want %d", cooldownGeneration, generation)
	}
	if remaining := time.Until(nextProbe); remaining <= 0 || remaining > runtimeAuthInconclusiveProbeCooldown {
		t.Fatalf("next endpoint probe delay = %v, want within %v", remaining, runtimeAuthInconclusiveProbeCooldown)
	}
}

func TestRuntimeAuthEndpointProbeGetsIndependentDeadline(t *testing.T) {
	prepareRuntimeAuthTest(t)
	operationGeneration := currentOperationGen()

	confirmationCtx, confirmationCancel, current := newOperationContextForGeneration(operationGeneration, time.Millisecond)
	if !current {
		t.Fatal("confirmation context generation was unexpectedly stale")
	}
	<-confirmationCtx.Done()
	confirmationCancel()

	endpointCtx, endpointCancel, current := newOperationContextForGeneration(operationGeneration, time.Second)
	if !current {
		t.Fatal("endpoint context generation was unexpectedly stale")
	}
	defer endpointCancel()
	if err := endpointCtx.Err(); err != nil {
		t.Fatalf("endpoint context inherited the exhausted confirmation deadline: %v", err)
	}
}

func TestDefaultRuntimeAuthEndpointProbeSkipsExecAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	err := defaultRuntimeAuthEndpointProbe(context.Background(), &rest.Config{
		Host: server.URL,
		ExecProvider: &clientcmdapi.ExecConfig{
			Command:         "/does/not/exist",
			APIVersion:      "client.authentication.k8s.io/v1",
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		},
	})
	if err != nil {
		t.Fatalf("defaultRuntimeAuthEndpointProbe() error = %v, want reachable endpoint", err)
	}
}

func TestRuntimeAuthFailureIgnoresInClusterAndStaleClients(t *testing.T) {
	t.Run("in cluster", func(t *testing.T) {
		generation := prepareRuntimeAuthTest(t)
		clientMu.Lock()
		ForceInCluster = true
		clientMu.Unlock()
		t.Cleanup(func() {
			clientMu.Lock()
			ForceInCluster = false
			clientMu.Unlock()
		})
		var probes atomic.Int32
		setRuntimeAuthProbe(func(context.Context) error {
			probes.Add(1)
			return errors.New("unauthorized")
		})

		reportRuntimeAuthFailure(generation, errors.New("unauthorized"))

		if probes.Load() != 0 {
			t.Fatalf("confirmation probes = %d, want 0", probes.Load())
		}
		if got := GetConnectionStatus().State; got != StateConnected {
			t.Fatalf("connection state = %q, want %q", got, StateConnected)
		}
	})

	t.Run("stale generation", func(t *testing.T) {
		generation := prepareRuntimeAuthTest(t)
		var probes atomic.Int32
		setRuntimeAuthProbe(func(context.Context) error {
			probes.Add(1)
			return errors.New("unauthorized")
		})

		reportRuntimeAuthFailure(generation-1, errors.New("unauthorized"))

		if probes.Load() != 0 {
			t.Fatalf("confirmation probes = %d, want 0", probes.Load())
		}
		if got := GetConnectionStatus().State; got != StateConnected {
			t.Fatalf("connection state = %q, want %q", got, StateConnected)
		}
	})
}

func TestRuntimeAuthFailureIgnoresNonConnectedState(t *testing.T) {
	for _, state := range []ConnectionState{StateConnecting, StateDisconnected} {
		t.Run(string(state), func(t *testing.T) {
			generation := prepareRuntimeAuthTest(t)
			SetConnectionStatus(ConnectionStatus{State: state, Context: "test-context"})
			var probes atomic.Int32
			setRuntimeAuthProbe(func(context.Context) error {
				probes.Add(1)
				return errors.New("unauthorized")
			})

			reportRuntimeAuthFailure(generation, errors.New("unauthorized"))

			if probes.Load() != 0 {
				t.Fatalf("confirmation probes = %d, want 0", probes.Load())
			}
			if got := GetConnectionStatus().State; got != state {
				t.Fatalf("connection state = %q, want %q", got, state)
			}
		})
	}
}

func TestRuntimeAuthFailureIgnoresActiveContextOperation(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	activeContextOperations.Add(1)
	t.Cleanup(func() { activeContextOperations.Add(-1) })
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("unauthorized")
	})

	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))

	if probes.Load() != 0 {
		t.Fatalf("confirmation probes = %d, want 0", probes.Load())
	}
	if got := GetConnectionStatus().State; got != StateConnected {
		t.Fatalf("connection state = %q, want %q", got, StateConnected)
	}
}

func TestRuntimeAuthFailureWinsAgainstQueuedContextOperation(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	setRuntimeAuthProbe(func(context.Context) error {
		return errors.New("getting credentials: exec plugin failed")
	})
	endpointReached := make(chan struct{})
	releaseEndpoint := make(chan struct{})
	setRuntimeAuthEndpointProbe(func(context.Context, *rest.Config) error {
		close(endpointReached)
		<-releaseEndpoint
		return nil
	})
	sessionStopped := make(chan struct{}, 1)
	SetSessionStopper(func() { sessionStopped <- struct{}{} })
	t.Cleanup(func() { SetSessionStopper(nil) })

	contextOpMu.Lock()
	mutexLocked := true
	defer func() {
		if mutexLocked {
			contextOpMu.Unlock()
		}
	}()

	reportRuntimeAuthFailure(generation, errors.New("getting credentials: exec plugin failed"))
	select {
	case <-endpointReached:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime auth endpoint probe did not start")
	}
	activeContextOperations.Add(1)
	t.Cleanup(func() { activeContextOperations.Add(-1) })
	close(releaseEndpoint)
	contextOpMu.Unlock()
	mutexLocked = false

	select {
	case <-sessionStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("queued context operation prevented runtime auth teardown")
	}
	waitForRuntimeAuthCheck(t, generation)
	if status := GetConnectionStatus(); status.State != StateDisconnected || status.ErrorType != "auth" {
		t.Fatalf("connection status = %+v, want disconnected auth", status)
	}
}

func TestRuntimeAuthFailureDoesNotTearDownRecoveredClient(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	setRuntimeAuthProbe(func(context.Context) error {
		close(probeStarted)
		<-releaseProbe
		return errors.New("getting credentials: exec plugin failed")
	})

	reportRuntimeAuthFailure(generation, errors.New("getting credentials: exec plugin failed"))
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime auth confirmation probe did not start")
	}

	contextOpMu.Lock()
	CancelOngoingOperations()
	clientMu.Lock()
	activeClientGeneration = generation + 1
	clientMu.Unlock()
	contextOpMu.Unlock()
	close(releaseProbe)
	waitForRuntimeAuthCheck(t, generation)

	if got := GetConnectionStatus().State; got != StateConnected {
		t.Fatalf("connection state = %q, want %q", got, StateConnected)
	}
}

func TestRuntimeAuthRecoveryRearmsThroughSwitchContext(t *testing.T) {
	oldGeneration := prepareRuntimeAuthTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gitVersion":"v1.35.0"}`))
	}))
	t.Cleanup(server.Close)

	kubeconfigFile := t.TempDir() + "/config"
	if err := clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"recovered": {Server: server.URL},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"recovered": {Token: "test-token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"recovered": {
				Cluster:  "recovered",
				AuthInfo: "recovered",
			},
		},
		CurrentContext: "recovered",
	}, kubeconfigFile); err != nil {
		t.Fatalf("write recovery kubeconfig error = %v", err)
	}
	clientMu.Lock()
	kubeconfigPath = kubeconfigFile
	clientMu.Unlock()
	if err := SwitchContext("recovered"); err != nil {
		t.Fatalf("SwitchContext() error = %v", err)
	}
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "recovered"})

	clientMu.RLock()
	recoveredGeneration := activeClientGeneration
	clientMu.RUnlock()
	if recoveredGeneration == oldGeneration {
		t.Fatal("SwitchContext() did not publish a new client generation")
	}

	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("unauthorized")
	})
	reportRuntimeAuthFailure(oldGeneration, errors.New("unauthorized"))
	if probes.Load() != 0 || GetConnectionStatus().State != StateConnected {
		t.Fatal("stale pre-recovery generation was not ignored")
	}

	reportRuntimeAuthFailure(recoveredGeneration, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, recoveredGeneration)
	if probes.Load() != 1 {
		t.Fatalf("recovered generation probes = %d, want 1", probes.Load())
	}
	if status := GetConnectionStatus(); status.State != StateDisconnected || status.ErrorType != "auth-rejected" {
		t.Fatalf("connection status = %+v, want disconnected auth-rejected", status)
	}
}

func TestRuntimeAuthCooldownExpiryReArmsDetection(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("dial tcp: connection refused")
	})

	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)
	if probes.Load() != 1 {
		t.Fatalf("confirmation probes = %d, want 1", probes.Load())
	}

	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)
	if probes.Load() != 1 {
		t.Fatalf("probes during cooldown = %d, want still 1", probes.Load())
	}

	runtimeAuthChecksMu.Lock()
	runtimeAuthProbeNotBefore = time.Now().Add(-time.Second)
	runtimeAuthChecksMu.Unlock()

	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)
	if probes.Load() != 2 {
		t.Fatalf("probes after cooldown expiry = %d, want 2", probes.Load())
	}
}

func TestRuntimeAuthRecoveryReconnectsWhenCredentialsReturn(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		if probes.Add(1) < 3 {
			return errors.New("getting credentials: exec plugin failed")
		}
		return nil
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(contextName string, _ uint64) error {
		if contextName != "recovery-context" {
			t.Errorf("reconnect context = %q, want %q", contextName, "recovery-context")
		}
		reconnects.Add(1)
		// The real reconnect (performContextSwitch) publishes Connected
		// while holding contextOpMu; the stub mirrors that contract.
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: contextName})
		return nil
	})

	// The publish-side hook alone must set the debt and start the worker —
	// this is the startup-expired-credentials path, no demotion involved.
	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "auth",
	})

	deadline := time.Now().Add(2 * time.Second)
	for reconnects.Load() == 0 || runtimeAuthRecoveryActive.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("recovery did not reconnect: probes=%d reconnects=%d active=%v",
				probes.Load(), reconnects.Load(), runtimeAuthRecoveryActive.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if probes.Load() < 3 {
		t.Fatalf("probes = %d, want >= 3 (failed probes back off before success)", probes.Load())
	}
	if reconnects.Load() != 1 {
		t.Fatalf("reconnects = %d, want 1", reconnects.Load())
	}
	if status := GetConnectionStatus(); status.State != StateConnected {
		t.Fatalf("connection did not settle connected, got %+v", status)
	}
	if runtimeAuthRecoveryOwed.Load() {
		t.Fatal("recovery debt not cleared by the connected publish")
	}
}

func TestRuntimeAuthRecoveryStopsWhenNoLongerNeeded(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	setRuntimeAuthProbe(func(context.Context) error {
		return errors.New("getting credentials: exec plugin failed")
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(string, uint64) error {
		reconnects.Add(1)
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "auth",
	})
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "recovery-context"})

	deadline := time.Now().Add(2 * time.Second)
	for runtimeAuthRecoveryActive.Load() {
		if time.Now().After(deadline) {
			t.Fatal("recovery loop did not exit after connection recovered elsewhere")
		}
		time.Sleep(time.Millisecond)
	}
	if reconnects.Load() != 0 {
		t.Fatalf("reconnects = %d, want 0", reconnects.Load())
	}
}

func TestPerformContextSwitchIfOperationCurrentAbortsWhenSuperseded(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	var sessionsStopped atomic.Int32
	SetSessionStopper(func() { sessionsStopped.Add(1) })
	t.Cleanup(func() { SetSessionStopper(nil) })
	var beforeSwitchFired atomic.Int32
	OnBeforeContextSwitch(func(string) { beforeSwitchFired.Add(1) })

	observedGen := currentOperationGen()
	CancelOngoingOperations()

	err := PerformContextSwitchIfOperationCurrent("any-context", observedGen)
	if !errors.Is(err, ErrReconnectSuperseded) {
		t.Fatalf("error = %v, want ErrReconnectSuperseded", err)
	}
	if sessionsStopped.Load() != 0 || beforeSwitchFired.Load() != 0 {
		t.Fatalf("superseded reconnect ran teardown: sessions=%d beforeSwitch=%d",
			sessionsStopped.Load(), beforeSwitchFired.Load())
	}
}

func TestRuntimeAuthRecoveryContinuesWhenSuperseded(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	setRuntimeAuthProbe(func(context.Context) error { return nil })
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(contextName string, _ uint64) error {
		if reconnects.Add(1) == 1 {
			return ErrReconnectSuperseded
		}
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: contextName})
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "auth",
	})

	deadline := time.Now().Add(2 * time.Second)
	for runtimeAuthRecoveryActive.Load() || reconnects.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("recovery loop did not finish: reconnects=%d", reconnects.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if reconnects.Load() != 2 {
		t.Fatalf("reconnects = %d, want 2 (superseded attempt retried next tick)", reconnects.Load())
	}
	if status := GetConnectionStatus(); status.State != StateConnected {
		t.Fatalf("connection did not settle connected, got %+v", status)
	}
}

func TestRuntimeAuthRecoveryRetargetsNewEpisode(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	var probeOK atomic.Bool
	setRuntimeAuthProbe(func(context.Context) error {
		if probeOK.Load() {
			return nil
		}
		return errors.New("getting credentials: exec plugin failed")
	})
	var reconnectedContext atomic.Value
	setRuntimeAuthReconnect(func(contextName string, _ uint64) error {
		// Mirror the real precondition: a reconnect aimed at a context the
		// live status has moved past is superseded, not applied.
		if GetConnectionStatus().Context != contextName {
			return ErrReconnectSuperseded
		}
		reconnectedContext.Store(contextName)
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: contextName})
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "context-a",
		ErrorType: "auth",
	})

	// New episode on a different context: the publish-side hook nudges the
	// worker awake so it retargets without waiting out the backoff.
	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "context-b",
		ErrorType: "auth",
	})
	probeOK.Store(true)

	deadline := time.Now().Add(2 * time.Second)
	for runtimeAuthRecoveryActive.Load() || reconnectedContext.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatalf("recovery loop did not finish; reconnected=%v", reconnectedContext.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if got := reconnectedContext.Load(); got != "context-b" {
		t.Fatalf("reconnected context = %v, want context-b", got)
	}
}

func TestRuntimeAuthRecoverySurvivesErrorTypeFlip(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	var probes atomic.Int32
	var probeOK atomic.Bool
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		if probeOK.Load() {
			return nil
		}
		return errors.New("getting credentials: exec plugin failed")
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(contextName string, _ uint64) error {
		reconnects.Add(1)
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: contextName})
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "auth",
	})
	waitForRecoveryWorker(t)

	// A failed user retry with a hung plugin republishes as "timeout", and a
	// Helm handler marking the cluster unreachable does the same with no user
	// action at all. Neither settles the recovery debt.
	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		Error:     "cluster connection failed: auth plugin timeout: context deadline exceeded",
		ErrorType: "timeout",
	})
	probeOK.Store(true)

	deadline := time.Now().Add(2 * time.Second)
	for reconnects.Load() == 0 || runtimeAuthRecoveryActive.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("worker died on errorType flip: probes=%d reconnects=%d", probes.Load(), reconnects.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if status := GetConnectionStatus(); status.State != StateConnected {
		t.Fatalf("connection did not settle connected, got %+v", status)
	}
}

func TestRuntimeAuthHungExecPluginDemotesWhenEndpointReachable(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	withContextExecAuth(t, true)
	setRuntimeAuthProbe(func(context.Context) error {
		return fmt.Errorf("auth plugin timeout: %w", context.DeadlineExceeded)
	})

	reportRuntimeAuthFailure(generation, errors.New("Kubernetes API returned HTTP 401 Unauthorized"))
	waitForRuntimeAuthCheck(t, generation)

	status := GetConnectionStatus()
	if status.State != StateDisconnected || status.ErrorType != "auth-plugin-stuck" {
		t.Fatalf("connection status = %+v, want disconnected auth-plugin-stuck (hung plugin with reachable endpoint)", status)
	}
	// The evidence is a wedged plugin, not a credential rejection — saying
	// "credentials are no longer valid" would be a guess.
	if status.Error != runtimeAuthPluginStuckMessage {
		t.Fatalf("published error = %q, want the plugin-stuck message", status.Error)
	}
}

func TestRuntimeAuthHungPluginTimeoutWithoutExecAuthStaysConnected(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	withContextExecAuth(t, false)
	setRuntimeAuthProbe(func(context.Context) error {
		return fmt.Errorf("auth plugin timeout: %w", context.DeadlineExceeded)
	})

	reportRuntimeAuthFailure(generation, errors.New("Kubernetes API returned HTTP 401 Unauthorized"))
	waitForRuntimeAuthCheck(t, generation)

	if got := GetConnectionStatus().State; got != StateConnected {
		t.Fatalf("connection state = %q, want %q (plugin-timeout rule is exec-auth only)", got, StateConnected)
	}
}

func TestDefaultRuntimeAuthEndpointProbeTreatsTLSAlertAsReachable(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert}
	server.StartTLS()
	t.Cleanup(server.Close)

	// The credential-free probe fails the mTLS handshake with a TLS alert —
	// which proves the endpoint is reachable, so the probe must report nil or
	// every demotion behind an mTLS proxy would be vetoed as inconclusive.
	err := defaultRuntimeAuthEndpointProbe(context.Background(), &rest.Config{
		Host:            server.URL,
		TLSClientConfig: rest.TLSClientConfig{Insecure: true},
	})
	if err != nil {
		t.Fatalf("probe against mTLS-required endpoint = %v, want nil (TLS alert proves reachability)", err)
	}
}

func TestInconclusiveStreakResetsOnConclusiveOutcomes(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)

	runtimeAuthChecksMu.Lock()
	runtimeAuthInconclusiveStreak = 8
	runtimeAuthChecksMu.Unlock()

	// A connected publish is conclusive: the next episode's first
	// inconclusive probe must restart at the short cooldown.
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "test-context"})
	if got := nextInconclusiveCooldown(); got != runtimeAuthInconclusiveProbeCooldown {
		t.Fatalf("cooldown after connected publish = %v, want %v", got, runtimeAuthInconclusiveProbeCooldown)
	}

	// So is a committed demotion.
	runtimeAuthChecksMu.Lock()
	runtimeAuthInconclusiveStreak = 8
	runtimeAuthChecksMu.Unlock()
	setRuntimeAuthProbe(func(context.Context) error {
		return errors.New("getting credentials: exec plugin failed")
	})
	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)
	if got := GetConnectionStatus().State; got != StateDisconnected {
		t.Fatalf("state = %q, want demoted", got)
	}
	if got := nextInconclusiveCooldown(); got != runtimeAuthInconclusiveProbeCooldown {
		t.Fatalf("cooldown after demotion = %v, want %v", got, runtimeAuthInconclusiveProbeCooldown)
	}
}

func TestRuntimeAuthDemotionPublishesClassificationSpecificCopy(t *testing.T) {
	for _, tt := range []struct {
		name        string
		probeErr    error
		wantType    string
		wantMessage string
	}{
		{
			name:        "client-side credential failure",
			probeErr:    errors.New("getting credentials: exec: executable aws failed with exit code 255"),
			wantType:    "auth",
			wantMessage: runtimeAuthLostMessage,
		},
		{
			name:        "server rejected the identity",
			probeErr:    errors.New("the server has asked for the client to provide credentials"),
			wantType:    "auth-rejected",
			wantMessage: runtimeAuthRejectedMessage,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			generation := prepareRuntimeAuthTest(t)
			setRuntimeAuthProbe(func(context.Context) error { return tt.probeErr })

			reportRuntimeAuthFailure(generation, errors.New("Kubernetes API returned HTTP 401 Unauthorized"))
			waitForRuntimeAuthCheck(t, generation)

			status := GetConnectionStatus()
			if status.ErrorType != tt.wantType {
				t.Fatalf("errorType = %q, want %q", status.ErrorType, tt.wantType)
			}
			if status.Error != tt.wantMessage {
				t.Fatalf("published error = %q, want %q", status.Error, tt.wantMessage)
			}
			// Both variants are auth-shaped: recovery must own either one.
			if !runtimeAuthRecoveryOwed.Load() {
				t.Fatalf("%s did not record the recovery debt", tt.wantType)
			}
		})
	}
}

func TestRuntimeAuthCooldownIsScopedToGeneration(t *testing.T) {
	generation := prepareRuntimeAuthTest(t)
	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("dial tcp: connection refused")
	})

	reportRuntimeAuthFailure(generation, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, generation)
	if probes.Load() != 1 {
		t.Fatalf("confirmation probes = %d, want 1", probes.Load())
	}

	// A context switch mints a new client generation; the old generation's
	// cooldown must not suppress the new one's first candidate.
	newGeneration := clientGenerationCounter.Add(1)
	clientMu.Lock()
	activeClientGeneration = newGeneration
	clientMu.Unlock()

	reportRuntimeAuthFailure(newGeneration, errors.New("unauthorized"))
	waitForRuntimeAuthCheck(t, newGeneration)
	if probes.Load() != 2 {
		t.Fatalf("probes after generation change = %d, want 2 (old cooldown must not apply)", probes.Load())
	}
}

func TestRuntimeAuthObserverCoversAllSharedClients(t *testing.T) {
	for _, tt := range []struct {
		name    string
		request func(t *testing.T, clients *sharedKubernetesClients)
	}{
		{name: "dynamic client", request: func(t *testing.T, clients *sharedKubernetesClients) {
			gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
			if _, err := clients.dynamic.Resource(gvr).List(context.Background(), metav1.ListOptions{}); err == nil {
				t.Fatal("expected 401 error from dynamic list")
			}
		}},
		{name: "discovery client", request: func(t *testing.T, clients *sharedKubernetesClients) {
			if _, err := clients.discovery.ServerVersion(); err == nil {
				t.Fatal("expected 401 error from discovery")
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prepareRuntimeAuthTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(server.Close)

			var probes atomic.Int32
			setRuntimeAuthProbe(func(context.Context) error {
				probes.Add(1)
				return errors.New("dial tcp: connection refused")
			})

			clients, err := newSharedKubernetesClients(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatalf("newSharedKubernetesClients() error = %v", err)
			}
			clientMu.Lock()
			activeClientGeneration = clients.generation
			clientMu.Unlock()

			tt.request(t, clients)
			waitForRuntimeAuthCheck(t, clients.generation)
			if probes.Load() != 1 {
				t.Fatalf("confirmation probes = %d, want 1 (401 through this client must reach the observer)", probes.Load())
			}
		})
	}
}

func TestRuntimeAuthRecoveryYieldsToActiveContextOperation(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return nil
	})
	setRuntimeAuthReconnect(func(contextName string, _ uint64) error {
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: contextName})
		return nil
	})

	activeContextOperations.Add(1)
	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "auth",
	})
	waitForRecoveryWorker(t)
	time.Sleep(30 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatalf("probes while a context operation is active = %d, want 0", probes.Load())
	}

	activeContextOperations.Add(-1)
	deadline := time.Now().Add(2 * time.Second)
	for runtimeAuthRecoveryActive.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("recovery did not proceed after the operation cleared: probes=%d", probes.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if probes.Load() == 0 {
		t.Fatal("recovery never probed after the context operation cleared")
	}
}

func TestSetConnectionStatusStartsRecoveryForAuthTypes(t *testing.T) {
	for _, errorType := range []string{"auth", "auth-rejected", "auth-plugin-stuck"} {
		t.Run(errorType, func(t *testing.T) {
			ResetTestState()
			t.Cleanup(ResetTestState)
			setRuntimeAuthRecoveryIntervalsForTest(time.Hour, time.Hour)

			SetConnectionStatus(ConnectionStatus{
				State:     StateDisconnected,
				Context:   "some-context",
				ErrorType: errorType,
			})
			if !runtimeAuthRecoveryOwed.Load() {
				t.Fatalf("%s disconnect did not record the recovery debt", errorType)
			}
			waitForRecoveryWorker(t)
		})
	}
}

func TestRuntimeAuthRecoveryStaticCredentialsReconnectViaDiskReread(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	clientMu.Lock()
	k8sConfig = &rest.Config{Host: "https://cluster.test", BearerToken: "expired-inline-token"}
	clientMu.Unlock()

	setRuntimeAuthProbe(func(context.Context) error {
		return errors.New("Kubernetes API returned HTTP 401 Unauthorized")
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(contextName string, _ uint64) error {
		// First attempt: kubeconfig still holds the dead token. Second:
		// the user re-logged in and the disk re-read picks it up.
		if reconnects.Add(1) == 1 {
			return errors.New("cluster connection failed: Unauthorized")
		}
		SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: contextName})
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "openshift-context",
		ErrorType: "auth",
	})

	deadline := time.Now().Add(2 * time.Second)
	for runtimeAuthRecoveryActive.Load() || reconnects.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("static-credential recovery did not reconnect: reconnects=%d", reconnects.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if status := GetConnectionStatus(); status.State != StateConnected {
		t.Fatalf("connection did not settle connected, got %+v", status)
	}
}

func TestMarkDisconnectedAuthShapedMessageRoutesThroughDemotionPipeline(t *testing.T) {
	for _, tt := range []struct {
		name    string
		message string
	}{
		{
			name:    "client-side credential failure",
			message: `failed to list helm releases: Kubernetes cluster unreachable: Get "https://cluster.test/version": getting credentials: exec: executable aws failed with exit code 255`,
		},
		{
			name:    "server rejected authentication",
			message: `failed to list helm releases: Kubernetes cluster unreachable: the server has asked for the client to provide credentials`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			generation := prepareRuntimeAuthTest(t)
			probeReturned := make(chan struct{}, 1)
			setRuntimeAuthProbe(func(context.Context) error {
				select {
				case probeReturned <- struct{}{}:
				default:
				}
				return errors.New("dial tcp: connection refused")
			})

			if MarkDisconnectedIfClusterUnreachable(tt.message) {
				t.Fatal("auth-shaped message must not mark disconnected directly — the pipeline decides")
			}
			select {
			case <-probeReturned:
			case <-time.After(2 * time.Second):
				t.Fatal("auth-shaped message did not reach the demotion pipeline")
			}
			waitForRuntimeAuthCheck(t, generation)
			if got := GetConnectionStatus().State; got != StateConnected {
				t.Fatalf("state = %q, want %q (pipeline dismissed the candidate; no direct publish)", got, StateConnected)
			}
		})
	}
}

func TestRuntimeAuthRecoveryStaticCredentialsSkipDiskRereadWhenOffline(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	clientMu.Lock()
	k8sConfig = &rest.Config{Host: "https://cluster.test", BearerToken: "expired-inline-token"}
	clientMu.Unlock()

	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("dial tcp: connection refused")
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(string, uint64) error {
		reconnects.Add(1)
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "openshift-context",
		ErrorType: "auth",
	})
	waitForRecoveryWorker(t)

	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if probes.Load() == 0 {
		t.Fatal("recovery never probed")
	}
	if reconnects.Load() != 0 {
		t.Fatalf("reconnects = %d, want 0 (a network failure cannot be fixed by a kubeconfig re-read)", reconnects.Load())
	}
}

func TestRuntimeAuthRecoveryExecCredentialsSkipDiskRereadAttempts(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	clientMu.Lock()
	k8sConfig = &rest.Config{
		Host:         "https://cluster.test",
		ExecProvider: &clientcmdapi.ExecConfig{Command: "aws"},
	}
	clientMu.Unlock()

	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		probes.Add(1)
		return errors.New("getting credentials: exec plugin failed")
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(string, uint64) error {
		reconnects.Add(1)
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "eks-context",
		ErrorType: "auth",
	})
	waitForRecoveryWorker(t)

	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if probes.Load() == 0 {
		t.Fatal("recovery never probed")
	}
	if reconnects.Load() != 0 {
		t.Fatalf("reconnects = %d, want 0 (failed probes with an exec plugin must not trigger disk-reread attempts)", reconnects.Load())
	}
}

func TestNextRuntimeAuthRecoveryInterval(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	maxInterval := defaultRuntimeAuthRecoveryMaxInterval
	hungInterval := defaultRuntimeAuthRecoveryHungInterval
	if got := nextRuntimeAuthRecoveryInterval(30*time.Second, errors.New("unauthorized"), maxInterval, hungInterval); got != time.Minute {
		t.Fatalf("interval after auth failure = %v, want %v", got, time.Minute)
	}
	if got := nextRuntimeAuthRecoveryInterval(4*time.Minute, errors.New("unauthorized"), maxInterval, hungInterval); got != maxInterval {
		t.Fatalf("interval at cap = %v, want %v", got, maxInterval)
	}
	withContextExecAuth(t, true)
	if got := nextRuntimeAuthRecoveryInterval(30*time.Second, errors.New("auth plugin timeout: context deadline exceeded"), maxInterval, hungInterval); got != hungInterval {
		t.Fatalf("interval after hung plugin = %v, want %v", got, hungInterval)
	}
	if got := nextRuntimeAuthRecoveryInterval(30*time.Second, errors.New("subsystem init failed: context deadline exceeded"), maxInterval, hungInterval); got != time.Minute {
		t.Fatalf("interval after generic timeout under exec auth = %v, want %v (hung interval is for wedged plugins only)", got, time.Minute)
	}
}

func setRuntimeAuthRecoveryIntervalsForTest(initial, maxInterval time.Duration) {
	runtimeAuthChecksMu.Lock()
	runtimeAuthRecoveryInitialInterval = initial
	runtimeAuthRecoveryMaxInterval = maxInterval
	runtimeAuthChecksMu.Unlock()
}

func prepareRuntimeAuthTest(t *testing.T) uint64 {
	t.Helper()
	ResetTestState()
	t.Cleanup(ResetTestState)

	generation := clientGenerationCounter.Add(1)
	clientMu.Lock()
	previousPath := kubeconfigPath
	// Without a kubeconfig path the package reads as in-cluster
	// (isInClusterLocked), where runtimeAuthStateIsCurrent is always false and
	// every assertion below would pass vacuously.
	kubeconfigPath = "/tmp/radar-runtime-auth-test"
	activeClientGeneration = generation
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		kubeconfigPath = previousPath
		clientMu.Unlock()
	})
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "test-context",
		ClusterName: "test-cluster",
	})
	setRuntimeAuthEndpointProbe(func(context.Context, *rest.Config) error { return nil })
	return generation
}

func waitForRuntimeAuthCheck(t *testing.T, generation uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtimeAuthChecksMu.Lock()
		_, running := runtimeAuthChecks[generation]
		runtimeAuthChecksMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime auth confirmation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRuntimeAuthExecCredentialHelper(t *testing.T) {
	if os.Getenv("RADAR_RUNTIME_AUTH_HELPER") != "1" {
		return
	}
	state, err := os.ReadFile(os.Getenv("RADAR_RUNTIME_AUTH_STATE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if string(state) == "fail" {
		fmt.Fprintln(os.Stderr, "reauthentication required")
		os.Exit(1)
	}

	expiration := time.Now().Add(100 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1",
		"kind":       "ExecCredential",
		"status": map[string]string{
			"expirationTimestamp": expiration,
			"token":               "runtime-auth-test-token",
		},
	})
	os.Exit(0)
}

func TestRuntimeAuthExecCredentialExpiry(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	statePath := t.TempDir() + "/state"
	if err := os.WriteFile(statePath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-auth-test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"gitVersion":"v1.35.0"}`))
	}))
	t.Cleanup(server.Close)

	config := &rest.Config{
		Host: server.URL,
		ExecProvider: &clientcmdapi.ExecConfig{
			Command:         os.Args[0],
			Args:            []string{"-test.run=^TestRuntimeAuthExecCredentialHelper$"},
			APIVersion:      "client.authentication.k8s.io/v1",
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
			Env: []clientcmdapi.ExecEnvVar{
				{Name: "RADAR_RUNTIME_AUTH_HELPER", Value: "1"},
				{Name: "RADAR_RUNTIME_AUTH_STATE", Value: statePath},
			},
		},
	}
	clients, err := newSharedKubernetesClients(config)
	if err != nil {
		t.Fatalf("newSharedKubernetesClients() error = %v", err)
	}
	clientMu.Lock()
	previousPath := kubeconfigPath
	kubeconfigPath = statePath
	k8sConfig = config
	k8sClient = clients.clientset
	discoveryClient = clients.discovery
	dynamicClient = clients.dynamic
	activeClientGeneration = clients.generation
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		kubeconfigPath = previousPath
		clientMu.Unlock()
	})
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "exec-test"})

	if _, err := clients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background()); err != nil {
		t.Fatalf("initial GET /version error = %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(statePath, []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessionStopped := make(chan struct{}, 1)
	SetSessionStopper(func() { sessionStopped <- struct{}{} })
	t.Cleanup(func() { SetSessionStopper(nil) })
	_, requestErr := clients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background())
	if ClassifyError(requestErr) != "auth" {
		t.Fatalf("expired credential request error = %v, want auth", requestErr)
	}
	select {
	case <-sessionStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime exec credential failure did not quiesce Radar")
	}
	waitForRuntimeAuthCheck(t, clients.generation)
	if status := GetConnectionStatus(); status.State != StateDisconnected || status.ErrorType != "auth" {
		t.Fatalf("connection status = %+v, want disconnected auth", status)
	}

	if err := os.WriteFile(statePath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	recoveredClients, err := newSharedKubernetesClients(config)
	if err != nil {
		t.Fatalf("recreate clients after reauth error = %v", err)
	}
	if _, err := recoveredClients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background()); err != nil {
		t.Fatalf("GET /version after reauth error = %v", err)
	}

	clientMu.Lock()
	k8sConfig = config
	k8sClient = recoveredClients.clientset
	discoveryClient = recoveredClients.discovery
	dynamicClient = recoveredClients.dynamic
	activeClientGeneration = recoveredClients.generation
	clientMu.Unlock()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "exec-test"})

	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(statePath, []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, requestErr = recoveredClients.clientset.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(context.Background())
	if ClassifyError(requestErr) != "auth" {
		t.Fatalf("second expired credential request error = %v, want auth", requestErr)
	}
	select {
	case <-sessionStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("second runtime exec credential failure did not quiesce Radar")
	}
	waitForRuntimeAuthCheck(t, recoveredClients.generation)
	if status := GetConnectionStatus(); status.State != StateDisconnected || status.ErrorType != "auth" {
		t.Fatalf("second connection status = %+v, want disconnected auth", status)
	}
}

// A non-auth disconnect must also leave a reconnect loop running. A browser
// retries these itself, but a deployment with no tab attached (MCP-only,
// API-only) has nothing that would, so the server is the floor.
func TestRuntimeRecoveryReconnectsAfterNetworkDisconnect(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(2*time.Millisecond, 4*time.Millisecond)

	var probes atomic.Int32
	setRuntimeAuthProbe(func(context.Context) error {
		// Unreachable for the first few passes, then the cluster comes back.
		if probes.Add(1) < 3 {
			return errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")
		}
		return nil
	})
	var reconnects atomic.Int32
	setRuntimeAuthReconnect(func(string, uint64) error {
		reconnects.Add(1)
		return nil
	})

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "network",
	})

	deadline := time.Now().Add(2 * time.Second)
	for reconnects.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("server never retried a network-shaped disconnect; a headless deployment would stay wedged")
		}
		time.Sleep(time.Millisecond)
	}
}

// The browser suppresses its own retry while the server owns recovery, so the
// network debt must stay invisible to it: publishing it would replace the
// browser's 10s-60s retry with the server's much slower backoff.
func TestNetworkRecoveryDebtIsNotPublishedToTheBrowser(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	setRuntimeAuthRecoveryIntervalsForTest(time.Hour, time.Hour)
	setRuntimeAuthProbe(func(context.Context) error { return errors.New("unreachable") })
	setRuntimeAuthReconnect(func(string, uint64) error { return nil })

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "network",
	})
	if RuntimeAuthRecoveryOwed() {
		t.Fatal("a network disconnect reported authRecoveryOwed; the browser would stand down and retry far more slowly")
	}

	SetConnectionStatus(ConnectionStatus{
		State:     StateDisconnected,
		Context:   "recovery-context",
		ErrorType: "auth",
	})
	if !RuntimeAuthRecoveryOwed() {
		t.Fatal("an auth disconnect no longer reports authRecoveryOwed")
	}
}
