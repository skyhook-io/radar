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
