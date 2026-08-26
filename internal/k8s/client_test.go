package k8s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestSourceContextBinding(t *testing.T) {
	got := sourceContextBinding("/configs/team-a.yaml", "prod")
	if got == "" || got != sourceContextBinding("/configs/team-a.yaml", "prod") {
		t.Fatalf("source binding is empty or unstable: %q", got)
	}
	if !strings.HasPrefix(got, "kcb1_") || strings.Contains(got, "team-a") || strings.Contains(got, "prod") {
		t.Fatalf("source binding is not opaque: %q", got)
	}
	if got == sourceContextBinding("/configs/team-b.yaml", "prod") {
		t.Fatal("different source files must not share a binding")
	}
	if got == sourceContextBinding("/configs/team-a.yaml", "staging") {
		t.Fatal("different in-file contexts must not share a binding")
	}
	if sourceContextBinding("", "prod") != "" || sourceContextBinding("/configs/team-a.yaml", "") != "" {
		t.Fatal("incomplete source identity must not produce a reusable binding")
	}
}

func TestCAPIClusterSafetyBinding(t *testing.T) {
	got := CAPIClusterSafetyBinding("kcb1_management", "clusters", "workload")
	if got == "" || got != CAPIClusterSafetyBinding("kcb1_management", "clusters", "workload") {
		t.Fatalf("CAPI binding is empty or unstable: %q", got)
	}
	if !strings.HasPrefix(got, "kcb1_") || strings.Contains(got, "management") || strings.Contains(got, "workload") {
		t.Fatalf("CAPI binding is not opaque: %q", got)
	}
	for _, different := range []string{
		CAPIClusterSafetyBinding("kcb1_other", "clusters", "workload"),
		CAPIClusterSafetyBinding("kcb1_management", "other", "workload"),
		CAPIClusterSafetyBinding("kcb1_management", "clusters", "other"),
	} {
		if different == got {
			t.Fatal("different CAPI source identity produced the same binding")
		}
	}
	if CAPIClusterSafetyBinding("", "clusters", "workload") != "" ||
		CAPIClusterSafetyBinding("kcb1_management", "", "workload") != "" ||
		CAPIClusterSafetyBinding("kcb1_management", "clusters", "") != "" {
		t.Fatal("incomplete CAPI source identity must not produce a reusable binding")
	}
}

// newExecAuthInfo builds an AuthInfo that uses an exec credential plugin
// with the given command. A helper because clientcmdapi.AuthInfo has a lot
// of fields we don't care about and we want test tables to stay readable.
func newExecAuthInfo(command string) *clientcmdapi.AuthInfo {
	return &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			Command: command,
		},
	}
}

func TestCollectExecPluginCommands(t *testing.T) {
	tests := []struct {
		name         string
		config       *clientcmdapi.Config
		wantCmds     []string
		wantEmptyAIs []string
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name:   "empty config",
			config: clientcmdapi.NewConfig(),
		},
		{
			name: "single context with simple exec plugin",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"prod": {AuthInfo: "prod-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"prod-user": newExecAuthInfo("aws"),
				},
			},
			wantCmds: []string{"aws"},
		},
		{
			name: "full path is reduced to basename",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"gke": {AuthInfo: "gke-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"gke-user": newExecAuthInfo("/usr/local/google-cloud-sdk/bin/gke-gcloud-auth-plugin"),
				},
			},
			wantCmds: []string{"gke-gcloud-auth-plugin"},
		},
		{
			name: "duplicate basenames across contexts are deduped",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"gke-a": {AuthInfo: "gke-user-a"},
					"gke-b": {AuthInfo: "gke-user-b"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"gke-user-a": newExecAuthInfo("/usr/local/google-cloud-sdk/bin/gke-gcloud-auth-plugin"),
					"gke-user-b": newExecAuthInfo("gke-gcloud-auth-plugin"),
				},
			},
			wantCmds: []string{"gke-gcloud-auth-plugin"},
		},
		{
			name: "orphan AuthInfo (no context references it) is skipped",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"prod": {AuthInfo: "prod-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"prod-user":   newExecAuthInfo("aws"),
					"orphan-user": newExecAuthInfo("doctl"), // unused — must not appear in output
				},
			},
			wantCmds: []string{"aws"},
		},
		{
			name: "output is sorted lexicographically",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"one":   {AuthInfo: "u1"},
					"two":   {AuthInfo: "u2"},
					"three": {AuthInfo: "u3"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"u1": newExecAuthInfo("kubelogin"),
					"u2": newExecAuthInfo("aws"),
					"u3": newExecAuthInfo("doctl"),
				},
			},
			wantCmds: []string{"aws", "doctl", "kubelogin"},
		},
		{
			name: "empty exec.Command is reported in emptyCommandAuthInfos",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"broken": {AuthInfo: "broken-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"broken-user": newExecAuthInfo(""),
				},
			},
			wantEmptyAIs: []string{"broken-user"},
		},
		{
			name: "empty command deduped across multiple contexts",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"broken-a": {AuthInfo: "broken-user"},
					"broken-b": {AuthInfo: "broken-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"broken-user": newExecAuthInfo(""),
				},
			},
			wantEmptyAIs: []string{"broken-user"},
		},
		{
			name: "nil Exec block is skipped silently",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"token-auth": {AuthInfo: "token-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"token-user": {Token: "abc"}, // no Exec block
				},
			},
		},
		{
			name: "mixed: valid plugin + empty-command + orphan, all handled",
			config: &clientcmdapi.Config{
				Contexts: map[string]*clientcmdapi.Context{
					"prod":   {AuthInfo: "prod-user"},
					"broken": {AuthInfo: "broken-user"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"prod-user":   newExecAuthInfo("aws"),
					"broken-user": newExecAuthInfo(""),
					"orphan-user": newExecAuthInfo("doctl"),
				},
			},
			wantCmds:     []string{"aws"},
			wantEmptyAIs: []string{"broken-user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmds, emptyAIs := collectExecPluginCommands(tt.config)
			if !stringSlicesEqual(cmds, tt.wantCmds) {
				t.Errorf("cmds = %v, want %v", cmds, tt.wantCmds)
			}
			if !stringSlicesEqual(emptyAIs, tt.wantEmptyAIs) {
				t.Errorf("emptyCommandAuthInfos = %v, want %v", emptyAIs, tt.wantEmptyAIs)
			}
		})
	}
}

func TestNamespaceScopeOverrideClearsBackToStartupFallback(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	prevContextNamespace := contextNamespace
	contextNamespace = "ctx-ns"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextNamespace = prevContextNamespace
		clientMu.Unlock()
	})

	SetFallbackNamespace("cli-ns")
	SetNamespaceScopeOverride("runtime-ns")
	if got := GetNamespaceScopeTarget(); got != "runtime-ns" {
		t.Fatalf("GetNamespaceScopeTarget() = %q, want runtime-ns", got)
	}

	ClearNamespaceScopeOverride()
	if got := GetNamespaceScopeTarget(); got != "cli-ns" {
		t.Fatalf("GetNamespaceScopeTarget() after clearing override = %q, want cli-ns", got)
	}

	SetFallbackNamespace("")
	if got := GetNamespaceScopeTarget(); got != "ctx-ns" {
		t.Fatalf("GetNamespaceScopeTarget() after clearing fallback = %q, want ctx-ns", got)
	}
}

// The startup --namespace is an *initial* filter, so it must only pin the cache
// scope on the context it was set for. After a cross-cluster switch the new
// context's own namespace takes over — the stale startup value must not follow.
func TestStartupFallbackNamespaceDoesNotFollowContextSwitch(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	setContextState := func(name, ns string) {
		clientMu.Lock()
		contextName = name
		contextNamespace = ns
		clientMu.Unlock()
	}
	t.Cleanup(func() { setContextState("", "") })

	// Boot: --namespace=cli-ns on the startup context, which has no context ns.
	setContextState("startup-ctx", "")
	SetFallbackNamespace("cli-ns")
	if got := GetNamespaceScopeTarget(); got != "cli-ns" {
		t.Fatalf("scope target on startup context = %q, want cli-ns", got)
	}

	// Switch to a different cluster whose context carries its own namespace.
	setContextState("other-ctx", "other-ns")
	if got := GetNamespaceScopeTarget(); got != "other-ns" {
		t.Fatalf("scope target after switch = %q, want other-ns (not stale cli-ns)", got)
	}

	// Switch to a cluster with no context namespace and no saved pick: the stale
	// startup value must not leak in — the target is empty so the switch can
	// surface the "pick a namespace" requirement.
	setContextState("bare-ctx", "")
	if got := GetNamespaceScopeTarget(); got != "" {
		t.Fatalf("scope target on bare context = %q, want empty", got)
	}

	// Switching back to the startup context re-activates --namespace.
	setContextState("startup-ctx", "")
	if got := GetNamespaceScopeTarget(); got != "cli-ns" {
		t.Fatalf("scope target back on startup context = %q, want cli-ns", got)
	}
}

func TestProspectiveNamespaceScopeTarget(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	contextName = "startup-ctx"
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = ""
		clientMu.Unlock()
	})

	// A saved pick for the target context wins.
	SetNamespaceScopePreferenceResolver(func(ctx string) (string, bool) {
		if ctx == "picked-ctx" {
			return "saved-ns", true
		}
		return "", false
	})
	if got := ProspectiveNamespaceScopeTarget("picked-ctx"); got != "saved-ns" {
		t.Fatalf("prospective target with saved pick = %q, want saved-ns", got)
	}

	// The startup --namespace only applies to its own context, never to others.
	SetFallbackNamespace("cli-ns") // captures contextName == "startup-ctx"
	if got := ProspectiveNamespaceScopeTarget("startup-ctx"); got != "cli-ns" {
		t.Fatalf("prospective target on startup context = %q, want cli-ns", got)
	}
	// A different context with no pick and no kubeconfig on disk resolves empty —
	// the switch guard treats that as "no usable scope target".
	if got := ProspectiveNamespaceScopeTarget("other-ctx"); got != "" {
		t.Fatalf("prospective target on foreign context = %q, want empty", got)
	}
}

// A --namespace-scope switch to a context with no usable scope target must fail
// at the pre-flight guard *before* tearing down or stopping sessions, so the
// user keeps their current caches and port-forwards / exec terminals.
func TestContextSwitchPreflightLeavesSessionsAlone(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })

	ForceNamespaceScope = true
	t.Cleanup(func() { ForceNamespaceScope = false })

	// No saved pick, no startup --namespace, and the context isn't in any
	// kubeconfig → prospective scope target is empty → the switch must bail before
	// CancelOngoingOperations / ResetAllSubsystems / stopActiveSessions.
	err := PerformContextSwitch("nonexistent-ctx")
	if err == nil {
		t.Fatal("expected pre-flight failure for a context with no scope target")
	}
	// Must be a typed preflight error so the handler keeps the current connection
	// instead of marking the app disconnected (nothing was torn down).
	if !errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
	}
	if stopped {
		t.Fatal("sessions were stopped before a switch that failed pre-flight")
	}
}

func TestContextSwitchMissingSourceLeavesSessionsAlone(t *testing.T) {
	dir := t.TempDir()
	current := writeKubeconfig(t, dir, "current.yaml", "current", []kubeEntry{
		{ctxName: "current", userName: "u1", clusterName: "c1"},
	})
	target := writeKubeconfig(t, dir, "target.yaml", "target", []kubeEntry{
		{ctxName: "target", userName: "u2", clusterName: "c2"},
	})
	registry, configs, mtimes := loadFixture(t, []string{current, target})

	clientMu.Lock()
	prevRegistry := contextRegistry
	prevConfigs := perFileConfigs
	prevMtimes := perFileMtimes
	prevPaths := kubeconfigPaths
	prevMode := kubeconfigMode
	prevStarted := initializationStarted
	contextRegistry = registry
	perFileConfigs = configs
	perFileMtimes = mtimes
	kubeconfigPaths = []string{current, target}
	kubeconfigMode = "multi-dir"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextRegistry = prevRegistry
		perFileConfigs = prevConfigs
		perFileMtimes = prevMtimes
		kubeconfigPaths = prevPaths
		kubeconfigMode = prevMode
		initializationStarted = prevStarted
		clientMu.Unlock()
	})

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target kubeconfig: %v", err)
	}

	err := PerformContextSwitch("target")
	if !errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
	}
	if stopped {
		t.Fatal("sessions were stopped for a context whose source disappeared")
	}
}

func TestContextSwitchInvalidCachedSourceLeavesSessionsAlone(t *testing.T) {
	tests := []struct {
		name    string
		rewrite func(t *testing.T, path string)
	}{
		{
			name: "malformed",
			rewrite: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("contexts: ["), 0o600); err != nil {
					t.Fatalf("write malformed kubeconfig: %v", err)
				}
			},
		},
		{
			name: "invalid context references",
			rewrite: func(t *testing.T, path string) {
				t.Helper()
				cfg := clientcmdapi.NewConfig()
				cfg.CurrentContext = "target"
				cfg.Contexts["target"] = &clientcmdapi.Context{Cluster: "missing", AuthInfo: "missing"}
				if err := clientcmd.WriteToFile(*cfg, path); err != nil {
					t.Fatalf("write invalid kubeconfig: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			current := writeKubeconfig(t, dir, "current.yaml", "current", []kubeEntry{
				{ctxName: "current", userName: "u1", clusterName: "c1"},
			})
			target := writeKubeconfig(t, dir, "target.yaml", "target", []kubeEntry{
				{ctxName: "target", userName: "u2", clusterName: "c2"},
			})
			registry, configs, mtimes := loadFixture(t, []string{current, target})

			clientMu.Lock()
			previousRegistry := contextRegistry
			previousConfigs := perFileConfigs
			previousMtimes := perFileMtimes
			previousPaths := kubeconfigPaths
			previousMode := kubeconfigMode
			previousStarted := initializationStarted
			contextRegistry = registry
			perFileConfigs = configs
			perFileMtimes = mtimes
			kubeconfigPaths = []string{current, target}
			kubeconfigMode = "multi-dir"
			initializationStarted = true
			clientMu.Unlock()
			t.Cleanup(func() {
				clientMu.Lock()
				contextRegistry = previousRegistry
				perFileConfigs = previousConfigs
				perFileMtimes = previousMtimes
				kubeconfigPaths = previousPaths
				kubeconfigMode = previousMode
				initializationStarted = previousStarted
				clientMu.Unlock()
			})

			stopped := false
			SetSessionStopper(func() { stopped = true })
			t.Cleanup(func() { SetSessionStopper(nil) })
			tt.rewrite(t, target)

			err := PerformContextSwitch("target")
			if !errors.Is(err, ErrContextSwitchPreflight) {
				t.Fatalf("expected ErrContextSwitchPreflight, got %v", err)
			}
			if !strings.Contains(err.Error(), "current source:") {
				t.Fatalf("preflight error omits the classified source failure: %v", err)
			}
			if strings.Contains(err.Error(), target) {
				t.Fatalf("preflight error leaked the kubeconfig path: %v", err)
			}
			if stopped {
				t.Fatal("sessions were stopped for an invalid cached context source")
			}
		})
	}
}

func TestLiteralInClusterContextNameDoesNotEnableInClusterMode(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousPath := kubeconfigPath
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	kubeconfigPath = "/tmp/literal-in-cluster-context"
	kubeconfigMode = "single"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		kubeconfigPath = previousPath
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	if IsInCluster() {
		t.Fatal("a kubeconfig context literally named in-cluster enabled in-cluster mode")
	}
}

func TestInClusterRuntimeReconnectReinitializesWithoutSwitchTeardown(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })
	beforeSwitch := false
	OnBeforeContextSwitch(func(string) { beforeSwitch = true })

	err := getRuntimeAuthReconnect()("in-cluster", currentOperationGen())
	if err == nil {
		t.Fatal("runtime reconnect unexpectedly initialized without a Kubernetes client")
	}
	if errors.Is(err, ErrContextSwitchPreflight) {
		t.Fatalf("in-cluster reconnect incorrectly attempted a context switch: %v", err)
	}
	if stopped || beforeSwitch {
		t.Fatalf("in-cluster reconnect ran teardown: stopped=%t beforeSwitch=%t", stopped, beforeSwitch)
	}
}

func TestRetryCurrentConnectionInClusterProbesBeforeReinitialize(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })
	probeCalled := false
	setRuntimeAuthProbe(func(context.Context) error {
		probeCalled = true
		return nil
	})
	reconnectCalled := false
	setRuntimeAuthReconnect(func(name string, generation uint64) error {
		reconnectCalled = true
		if name != "in-cluster" || generation != currentOperationGen() {
			t.Fatalf("reconnect inputs = (%q, %d), current generation %d", name, generation, currentOperationGen())
		}
		return nil
	})

	if err := RetryCurrentConnection(); err != nil {
		t.Fatalf("RetryCurrentConnection: %v", err)
	}
	if !probeCalled || !reconnectCalled {
		t.Fatalf("retry calls = probe %t, reconnect %t", probeCalled, reconnectCalled)
	}
	if stopped {
		t.Fatal("in-cluster retry stopped active sessions")
	}
}

func TestRetryCurrentConnectionInClusterProbeFailureKeepsCaches(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	stopped := false
	SetSessionStopper(func() { stopped = true })
	t.Cleanup(func() { SetSessionStopper(nil) })
	probeErr := errors.New("probe failed")
	setRuntimeAuthProbe(func(context.Context) error { return probeErr })
	reconnectCalled := false
	setRuntimeAuthReconnect(func(string, uint64) error {
		reconnectCalled = true
		return nil
	})

	err := RetryCurrentConnection()
	if !errors.Is(err, probeErr) {
		t.Fatalf("RetryCurrentConnection error = %v, want %v", err, probeErr)
	}
	if reconnectCalled {
		t.Fatal("probe failure reinitialized in-cluster caches")
	}
	if stopped {
		t.Fatal("probe failure stopped active sessions")
	}
}

func TestRetryCurrentConnectionInClusterDoesNotOutliveNewerOperation(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	setRuntimeAuthProbe(func(context.Context) error {
		close(probeStarted)
		<-releaseProbe
		return nil
	})
	reconnectCalled := false
	setRuntimeAuthReconnect(func(string, uint64) error {
		reconnectCalled = true
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- RetryCurrentConnection() }()
	<-probeStarted
	CancelOngoingOperations()
	close(releaseProbe)

	if err := <-done; !errors.Is(err, ErrReconnectSuperseded) {
		t.Fatalf("RetryCurrentConnection error = %v, want ErrReconnectSuperseded", err)
	}
	if reconnectCalled {
		t.Fatal("superseded retry reinitialized in-cluster caches")
	}
}

func TestInClusterRuntimeReconnectPublishesConnectedAfterReinit(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousCluster := clusterName
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	clusterName = "service-account-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		clusterName = previousCluster
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	connectionStatusMu.Lock()
	previousStatus := connectionStatus
	connectionStatus = ConnectionStatus{State: StateDisconnected, Context: "in-cluster", ErrorType: "auth"}
	connectionStatusMu.Unlock()
	t.Cleanup(func() {
		connectionStatusMu.Lock()
		connectionStatus = previousStatus
		connectionStatusMu.Unlock()
	})

	contextSwitchMu.Lock()
	previousCallbacks := contextSwitchCallbacks
	callbackContext := ""
	contextSwitchCallbacks = []ContextSwitchCallback{func(name string) { callbackContext = name }}
	contextSwitchMu.Unlock()
	t.Cleanup(func() {
		contextSwitchMu.Lock()
		contextSwitchCallbacks = previousCallbacks
		contextSwitchMu.Unlock()
	})

	initialized := false
	err := reinitializeCurrentContextIfOperationCurrent(
		"in-cluster",
		currentOperationGen(),
		func(ctx context.Context, progress func(string)) error {
			if ctx.Err() != nil || progress == nil {
				t.Fatalf("invalid subsystem init inputs: ctxErr=%v progressNil=%t", ctx.Err(), progress == nil)
			}
			initialized = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("reinitialize current context: %v", err)
	}
	if !initialized {
		t.Fatal("subsystems were not initialized")
	}
	if callbackContext != "in-cluster" {
		t.Fatalf("completion callback context = %q, want in-cluster", callbackContext)
	}
	status := GetConnectionStatus()
	if status.State != StateConnected || status.Context != "in-cluster" || status.ClusterName != "service-account-cluster" {
		t.Fatalf("connection status = %+v", status)
	}
}

func TestInClusterRuntimeReconnectDoesNotPublishAfterSupersededInit(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	previousName := contextName
	previousMode := kubeconfigMode
	previousStarted := initializationStarted
	contextName = "in-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextName = previousName
		kubeconfigMode = previousMode
		initializationStarted = previousStarted
		clientMu.Unlock()
	})

	connectionStatusMu.Lock()
	previousStatus := connectionStatus
	connectionStatus = ConnectionStatus{State: StateDisconnected, Context: "in-cluster"}
	connectionStatusMu.Unlock()
	t.Cleanup(func() {
		connectionStatusMu.Lock()
		connectionStatus = previousStatus
		connectionStatusMu.Unlock()
	})

	contextSwitchMu.Lock()
	previousCallbacks := contextSwitchCallbacks
	callbackCalled := false
	contextSwitchCallbacks = []ContextSwitchCallback{func(string) { callbackCalled = true }}
	contextSwitchMu.Unlock()
	t.Cleanup(func() {
		contextSwitchMu.Lock()
		contextSwitchCallbacks = previousCallbacks
		contextSwitchMu.Unlock()
	})

	err := reinitializeCurrentContextIfOperationCurrent(
		"in-cluster",
		currentOperationGen(),
		func(context.Context, func(string)) error {
			CancelOngoingOperations()
			return nil
		},
	)
	if !errors.Is(err, ErrReconnectSuperseded) {
		t.Fatalf("reinitialize current context error = %v, want ErrReconnectSuperseded", err)
	}
	if callbackCalled {
		t.Fatal("superseded reinitialize published its context callback")
	}
	if status := GetConnectionStatus(); status.State != StateDisconnected {
		t.Fatalf("superseded reinitialize status = %+v", status)
	}
}

func TestInClusterRuntimeReconnectClassifiesCancelledSupersededInit(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	clientMu.Lock()
	contextName = "in-cluster"
	kubeconfigMode = "in-cluster"
	initializationStarted = true
	clientMu.Unlock()

	err := reinitializeCurrentContextIfOperationCurrent(
		"in-cluster",
		currentOperationGen(),
		func(ctx context.Context, _ func(string)) error {
			CancelOngoingOperations()
			return ctx.Err()
		},
	)
	if !errors.Is(err, ErrReconnectSuperseded) {
		t.Fatalf("reinitialize current context error = %v, want ErrReconnectSuperseded", err)
	}
}

func TestRequireNamespaceScopeTarget(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)

	if err := requireNamespaceScopeTarget("ctx-a"); err != nil {
		t.Fatalf("requireNamespaceScopeTarget without ForceNamespaceScope = %v, want nil", err)
	}

	ForceNamespaceScope = true
	if err := requireNamespaceScopeTarget("ctx-a"); err == nil {
		t.Fatal("requireNamespaceScopeTarget with empty target = nil, want error")
	}

	SetNamespaceScopeOverride("saved-ns")
	if err := requireNamespaceScopeTarget("ctx-a"); err != nil {
		t.Fatalf("requireNamespaceScopeTarget with saved target = %v, want nil", err)
	}
}

func TestScrubPathError(t *testing.T) {
	// Simulate the shape os.ReadDir returns: a *PathError with the path
	// and an underlying syscall error. We want the path stripped and only
	// the Op + underlying cause preserved.
	directPathErr := &os.PathError{
		Op:   "open",
		Path: "/Users/alice/.kube/configs/prod.yaml",
		Err:  os.ErrPermission,
	}
	wrappedPathErr := fmt.Errorf("load kubeconfig: %w", directPathErr)

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error returns empty string",
			err:  nil,
			want: "",
		},
		{
			name: "direct *os.PathError strips path and returns op + cause",
			err:  directPathErr,
			// The "permission denied" text comes from os.ErrPermission.Error().
			// We assert via Contains below to avoid coupling to the exact
			// stdlib phrasing, which varies by platform.
			want: "open: ",
		},
		{
			name: "wrapped via fmt.Errorf(%w, PathError) still unwraps",
			err:  wrappedPathErr,
			want: "open: ",
		},
		{
			name: "non-PathError error collapses to conservative placeholder",
			// errors.New text may itself contain what looks like a path —
			// the helper must not pass it through.
			err:  errors.New("open /Users/alice/secrets/token.key: denied"),
			want: "(unscrubbable error — omitted to avoid leaking paths)",
		},
		{
			name: "*os.PathError with nil inner Err collapses to placeholder",
			err:  &os.PathError{Op: "stat", Path: "/home/bob", Err: nil},
			want: "(unscrubbable error — omitted to avoid leaking paths)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubPathError(tt.err)

			// Case-specific checks:
			// 1. The result must never contain an absolute-looking path from
			//    any of our fixtures. This is the privacy contract.
			for _, leak := range []string{
				"/Users/alice/.kube/configs/prod.yaml",
				"/Users/alice/secrets/token.key",
				"/home/bob",
			} {
				if containsSubstring(got, leak) {
					t.Errorf("scrubPathError leaked path %q in result %q", leak, got)
				}
			}

			// 2. The returned string must contain the expected prefix/exact.
			switch tt.name {
			case "nil error returns empty string",
				"non-PathError error collapses to conservative placeholder",
				"*os.PathError with nil inner Err collapses to placeholder":
				if got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			default:
				if !containsSubstring(got, tt.want) {
					t.Errorf("got %q, want it to contain %q", got, tt.want)
				}
			}
		})
	}
}

// stringSlicesEqual returns true if two slices contain the same elements in
// the same order. Nil and empty slices are treated as equal so test cases
// don't have to distinguish "no output" shapes.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsSubstring(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
