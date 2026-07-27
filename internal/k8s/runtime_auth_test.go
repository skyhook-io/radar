package k8s

import (
	"context"
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
	if status := GetConnectionStatus(); status.State != StateDisconnected || status.ErrorType != "auth" {
		t.Fatalf("connection status = %+v, want disconnected auth", status)
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
	sessionStopped := make(chan struct{}, 1)
	SetSessionStopper(func() {
		select {
		case sessionStopped <- struct{}{}:
		default:
		}
	})
	t.Cleanup(func() { SetSessionStopper(nil) })

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
	nextProbe := runtimeAuthProbeAfter[generation]
	runtimeAuthChecksMu.Unlock()
	if remaining := time.Until(nextProbe); remaining <= 0 || remaining > runtimeAuthInconclusiveProbeCooldown {
		t.Fatalf("next endpoint probe delay = %v, want within %v", remaining, runtimeAuthInconclusiveProbeCooldown)
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
	if status := GetConnectionStatus(); status.State != StateDisconnected || status.ErrorType != "auth" {
		t.Fatalf("connection status = %+v, want disconnected auth", status)
	}
}

func prepareRuntimeAuthTest(t *testing.T) uint64 {
	t.Helper()
	ResetTestState()
	t.Cleanup(ResetTestState)

	generation := clientGenerationCounter.Add(1)
	clientMu.Lock()
	previousPath := kubeconfigPath
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
