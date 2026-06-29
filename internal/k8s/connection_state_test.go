package k8s

import (
	"context"
	"errors"
	"testing"
)

func TestMarkDisconnectedIfClusterUnreachable(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})

	message := `failed to list helm releases: Kubernetes cluster unreachable: Get "https://127.0.0.1:64287/version": dial tcp 127.0.0.1:64287: connect: connection refused`
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false")
	}

	got := GetConnectionStatus()
	if got.State != StateDisconnected {
		t.Fatalf("state = %q, want %q", got.State, StateDisconnected)
	}
	if got.Context != "kind-demo" {
		t.Fatalf("context = %q, want kind-demo", got.Context)
	}
	if got.ClusterName != "demo" {
		t.Fatalf("clusterName = %q, want demo", got.ClusterName)
	}
	if got.Error != message {
		t.Fatalf("error = %q, want original message", got.Error)
	}
	if got.ErrorType != "network" {
		t.Fatalf("errorType = %q, want network", got.ErrorType)
	}

	callbacks := 0
	probeCalls := 0
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error {
		probeCalls++
		return nil
	}
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})
	OnConnectionChange(func(ConnectionStatus) {
		callbacks++
	})
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false for existing disconnected state")
	}
	if probeCalls != 0 {
		t.Fatalf("probeCalls = %d, want 0 for unchanged disconnected state", probeCalls)
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want 0 for unchanged disconnected state", callbacks)
	}
}

func TestMarkDisconnectedIfClusterUnreachableIgnoresRecoveredCluster(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error { return nil }
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})

	message := `failed to list helm releases: Kubernetes cluster unreachable: Get "https://127.0.0.1:64287/version": dial tcp 127.0.0.1:64287: connect: connection refused`
	if MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned true after liveness probe recovered")
	}

	got := GetConnectionStatus()
	if got.State != StateConnected {
		t.Fatalf("state = %q, want %q", got.State, StateConnected)
	}
}

func TestMarkDisconnectedIfClusterUnreachableHandlesRawHelmTransportError(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{
		State:       StateConnected,
		Context:     "kind-demo",
		ClusterName: "demo",
	})
	previousProbe := clusterLivenessProbe
	clusterLivenessProbe = func(context.Context) error { return errors.New("still unreachable") }
	t.Cleanup(func() {
		clusterLivenessProbe = previousProbe
	})

	message := `failed to list helm releases: Get "https://127.0.0.1:64287/api/v1/secrets": dial tcp 127.0.0.1:64287: connect: connection refused`
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false for raw Helm transport error")
	}

	got := GetConnectionStatus()
	if got.State != StateDisconnected {
		t.Fatalf("state = %q, want %q", got.State, StateDisconnected)
	}
	if got.Error != message {
		t.Fatalf("error = %q, want original message", got.Error)
	}
}

func TestMarkDisconnectedIfClusterUnreachableIgnoresNonClusterNetworkErrors(t *testing.T) {
	ResetTestState()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "kind-demo"})

	if MarkDisconnectedIfClusterUnreachable(`failed to update chart repository: Get "https://charts.example.test/index.yaml": dial tcp: no such host`) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned true for chart repository network error")
	}

	got := GetConnectionStatus()
	if got.State != StateConnected {
		t.Fatalf("state = %q, want %q", got.State, StateConnected)
	}
}

func withContextExecAuth(t *testing.T, enabled bool) {
	t.Helper()
	clientMu.Lock()
	prev := contextUsesExec
	contextUsesExec = enabled
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		contextUsesExec = prev
		clientMu.Unlock()
	})
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      string
		execAuth bool
		want     string
	}{
		{
			name: "rbac forbidden",
			err:  `deployments.apps is forbidden: User "alice" cannot list resource`,
			want: "rbac",
		},
		{
			name: "aws sso expired",
			err:  "SSOProviderInvalidToken: the SSO session has expired or is invalid; run aws sso login",
			want: "auth",
		},
		{
			name: "client-go exec credential failure is auth",
			err:  `failed to connect to cluster: Get "https://example.eks.amazonaws.com/version": getting credentials: exec: executable aws failed with exit code 255`,
			want: "auth",
		},
		{
			name: "plain context deadline is timeout without exec auth",
			err:  "failed to connect to cluster: context deadline exceeded",
			want: "timeout",
		},
		{
			name: "cluster unreachable context deadline is timeout without exec auth",
			err:  "failed to connect to cluster: cluster unreachable: context deadline exceeded",
			want: "timeout",
		},
		{
			name:     "exec auth context deadline is auth",
			err:      "failed to connect to cluster: context deadline exceeded",
			execAuth: true,
			want:     "auth",
		},
		{
			name:     "exec auth plugin timeout is auth",
			err:      "failed to connect to cluster: auth plugin timeout: context deadline exceeded",
			execAuth: true,
			want:     "auth",
		},
		{
			name:     "exec auth cluster unreachable context deadline stays timeout",
			err:      "failed to connect to cluster: cluster unreachable: context deadline exceeded",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth cluster unreachable io timeout stays timeout",
			err:      "failed to connect to cluster: cluster unreachable: i/o timeout",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth read io timeout stays timeout",
			err:      "read tcp 10.0.0.1:443: i/o timeout",
			execAuth: true,
			want:     "timeout",
		},
		{
			name:     "exec auth connection refused is network",
			err:      "failed to connect to cluster: dial tcp 127.0.0.1:6443: connect: connection refused",
			execAuth: true,
			want:     "network",
		},
		{
			name: "unknown",
			err:  "something novel happened",
			want: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withContextExecAuth(t, tt.execAuth)
			if got := ClassifyError(errors.New(tt.err)); got != tt.want {
				t.Fatalf("ClassifyError(%q) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyErrorNil(t *testing.T) {
	if got := ClassifyError(nil); got != "" {
		t.Fatalf("ClassifyError(nil) = %q, want empty", got)
	}
}
