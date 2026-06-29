package k8s

import "testing"

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
	OnConnectionChange(func(ConnectionStatus) {
		callbacks++
	})
	if !MarkDisconnectedIfClusterUnreachable(message) {
		t.Fatal("MarkDisconnectedIfClusterUnreachable returned false for existing disconnected state")
	}
	if callbacks != 0 {
		t.Fatalf("callbacks = %d, want 0 for unchanged disconnected state", callbacks)
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
