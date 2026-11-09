package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// The timeline is empty until the cluster has connected, so get_changes must
// refuse rather than answer "nothing changed" — with the code and message the
// REST API uses for the same state.
func TestGetChangesRefusesUntilConnected(t *testing.T) {
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "edit", Timestamp: time.Now().Add(-time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "web", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
			Path: "spec.template.spec.containers[web].image", OldValue: "web:1", NewValue: "web:2",
		}}},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	for _, tc := range []struct {
		status k8s.ConnectionStatus
		want   string
	}{
		{k8s.ConnectionStatus{State: k8s.StateConnecting}, "cluster_connecting: Cluster is still connecting, please retry shortly"},
		{k8s.ConnectionStatus{State: k8s.StateDisconnected, Error: "dial tcp: i/o timeout"}, "cluster_disconnected: not connected to cluster: dial tcp: i/o timeout. " + restoreConnection},
	} {
		k8s.SetConnectionStatus(tc.status)
		result, _, err := handleGetChanges(context.Background(), nil, getChangesInput{})
		if err == nil || err.Error() != tc.want {
			t.Fatalf("%s: result=%v err=%v, want error %q", tc.status.State, result, err, tc.want)
		}
	}

	setConnectedForTest(t)
	_, text := getChangesForTest(t, context.Background(), getChangesInput{})
	if !strings.Contains(text, `"web"`) {
		t.Fatalf("connected get_changes lost the stored change: %s", text)
	}
}

// Radar sets up its Helm client once the cluster has connected. Before then,
// the Helm tools must not answer "no releases found".
func TestHelmToolsWhileConnecting(t *testing.T) {
	helm.ResetClient()
	prev := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })

	for _, tc := range []struct {
		state k8s.ConnectionState
		want  string
	}{
		{k8s.StateConnecting, "cluster_connecting: Cluster is still connecting, please retry shortly"},
		{k8s.StateConnected, "helm is not available (no releases found or helm not installed)"},
	} {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{State: tc.state})
		if _, _, err := handleListHelmReleases(context.Background(), nil, listHelmReleasesInput{}); err == nil || err.Error() != tc.want {
			t.Errorf("list_helm_releases while %s: err = %v, want %q", tc.state, err, tc.want)
		}
		if _, _, err := handleGetHelmRelease(context.Background(), nil, getHelmReleaseInput{Namespace: "default", Name: "web"}); err == nil || err.Error() != tc.want {
			t.Errorf("get_helm_release while %s: err = %v, want %q", tc.state, err, tc.want)
		}
	}
}
