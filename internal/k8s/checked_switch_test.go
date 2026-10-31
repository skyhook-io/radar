package k8s

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// writeServerKubeconfig writes a one-context kubeconfig pointing at server.
func writeServerKubeconfig(t *testing.T, dir, filename, ctxName, server string) string {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[ctxName+"-cluster"] = &clientcmdapi.Cluster{Server: server, InsecureSkipTLSVerify: true}
	cfg.AuthInfos[ctxName+"-user"] = &clientcmdapi.AuthInfo{Token: "fake-token"}
	cfg.Contexts[ctxName] = &clientcmdapi.Context{Cluster: ctxName + "-cluster", AuthInfo: ctxName + "-user"}
	cfg.CurrentContext = ctxName
	path := filepath.Join(dir, filename)
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// setupCheckedSwitch registers a "current" and a "target" context, marks the
// session connected to "current", and reports whether teardown started.
func setupCheckedSwitch(t *testing.T, targetServer string, connected bool) (targetPath string, tornDown *bool) {
	t.Helper()
	ResetTestState()
	t.Cleanup(ResetTestState)

	dir := t.TempDir()
	current := writeServerKubeconfig(t, dir, "current.yaml", "current", "https://current.invalid")
	targetPath = writeServerKubeconfig(t, dir, "target.yaml", "target", targetServer)
	registry, configs, mtimes := loadFixture(t, []string{current, targetPath})

	clientMu.Lock()
	prevRegistry, prevConfigs, prevMtimes := contextRegistry, perFileConfigs, perFileMtimes
	prevPaths, prevMode, prevStarted, prevName := kubeconfigPaths, kubeconfigMode, initializationStarted, contextName
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{current, targetPath}
	kubeconfigMode = "multi-dir"
	initializationStarted = true
	contextName = "current"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry, perFileConfigs, perFileMtimes = prevRegistry, prevConfigs, prevMtimes
		kubeconfigPaths, kubeconfigMode, initializationStarted, contextName = prevPaths, prevMode, prevStarted, prevName
		clientMu.Unlock()
	})

	state := StateDisconnected
	if connected {
		state = StateConnected
	}
	SetConnectionStatus(ConnectionStatus{State: state, Context: "current"})

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })
	return targetPath, &stopped
}

func unreachableServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

func TestCheckedSwitchRefusesUnreachableTargetBeforeTeardown(t *testing.T) {
	_, tornDown := setupCheckedSwitch(t, unreachableServer(t), true)

	err := PerformCheckedContextSwitch(context.Background(), "target")
	if !errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
	}
	var probeErr *ContextProbeError
	if !errors.As(err, &probeErr) || probeErr.Context != "target" {
		t.Fatalf("expected a ContextProbeError for target, got %v", err)
	}
	if got := ClassifyError(probeErr.Err); got != "network" {
		t.Fatalf("errorType = %q, want network", got)
	}
	if *tornDown {
		t.Fatal("sessions were stopped for a switch whose target was unreachable")
	}
	if got := GetContextName(); got != "current" {
		t.Fatalf("active context = %q, want current", got)
	}
	if got := GetConnectionStatus().State; got != StateConnected {
		t.Fatalf("connection state = %q, want connected", got)
	}
}

func TestCheckedSwitchClassifiesRejectedCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","code":401}`))
	}))
	t.Cleanup(server.Close)
	_, tornDown := setupCheckedSwitch(t, server.URL, true)

	err := PerformCheckedContextSwitch(context.Background(), "target")
	var probeErr *ContextProbeError
	if !errors.As(err, &probeErr) {
		t.Fatalf("expected a ContextProbeError, got %v", err)
	}
	if got := ClassifyError(probeErr.Err); got != "auth-rejected" {
		t.Fatalf("errorType = %q, want auth-rejected", got)
	}
	if *tornDown {
		t.Fatal("sessions were stopped for a switch whose credentials were rejected")
	}
}

// With no working cluster to protect, a switch goes straight to the existing
// path: no extra check, so recovery and retry behave exactly as before.
func TestCheckedSwitchSkipsCheckWhenDisconnected(t *testing.T) {
	_, tornDown := setupCheckedSwitch(t, unreachableServer(t), false)

	err := PerformCheckedContextSwitch(context.Background(), "target")
	if err == nil {
		t.Fatal("expected the switch to an unreachable target to fail")
	}
	if errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("disconnected switch was refused by the pre-teardown check: %v", err)
	}
	if !*tornDown {
		t.Fatal("expected the existing switch path, which tears down before connecting")
	}
}

func TestCheckedSwitchAbandonsWhenTargetSourceChanged(t *testing.T) {
	targetPath, tornDown := setupCheckedSwitch(t, "https://target.invalid", true)
	prepared, err := prepareContext("target")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// The name now resolves to a different server than the one that was checked.
	writeServerKubeconfig(t, filepath.Dir(targetPath), filepath.Base(targetPath), "target", "https://elsewhere.invalid")

	err = performContextSwitch("target", 0, false, &checkedSwitch{ctx: context.Background(), prepared: prepared})
	if !errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
	}
	if *tornDown {
		t.Fatal("sessions were stopped although the checked target changed")
	}
}

func TestCheckedSwitchAbandonsWhenRequestCancelled(t *testing.T) {
	_, tornDown := setupCheckedSwitch(t, "https://target.invalid", true)
	prepared, err := prepareContext("target")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = performContextSwitch("target", 0, false, &checkedSwitch{ctx: ctx, prepared: prepared})
	if !errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
	}
	if *tornDown {
		t.Fatal("sessions were stopped after the person stopped waiting")
	}
}

func TestContextSourceFingerprintFollowsServerAndCredentials(t *testing.T) {
	dir := t.TempDir()
	path := writeServerKubeconfig(t, dir, "k.yaml", "ctx", "https://one.invalid")
	fingerprint := func() string {
		t.Helper()
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
		raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: "ctx"}).RawConfig()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return contextSourceFingerprint(path, "ctx", raw, raw.Contexts["ctx"])
	}
	first := fingerprint()
	if again := fingerprint(); again != first {
		t.Fatal("fingerprint changed without a kubeconfig change")
	}
	writeServerKubeconfig(t, dir, "k.yaml", "ctx", "https://two.invalid")
	if fingerprint() == first {
		t.Fatal("fingerprint did not change when the server changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
}
