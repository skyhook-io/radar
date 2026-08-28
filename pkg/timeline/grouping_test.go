package timeline

import (
	"testing"
	"time"
)

// TestGroupByApp_ClusterScopedMergesWithApp verifies that a cluster-scoped
// resource (Namespace == "") carrying an app label is merged into the existing
// namespaced group for that same app, instead of landing in its own isolated
// group. Cluster-scoped resources with an app label merge into the matching
// namespaced app group.
func TestGroupByApp_ClusterScopedMergesWithApp(t *testing.T) {
	events := []TimelineEvent{
		{ID: "1", Kind: "Deployment", Namespace: "myns", Name: "my-app",
			Labels: map[string]string{"app.kubernetes.io/name": "my-app"}, Timestamp: time.Now()},
		{ID: "2", Kind: "ClusterSecretStore", Namespace: "", Name: "my-app-store",
			Labels: map[string]string{"app.kubernetes.io/name": "my-app"}, Timestamp: time.Now()},
	}

	groups := GroupEvents(events, GroupByApp)

	if len(groups) != 1 {
		t.Fatalf("Expected 1 group (cluster-scoped merged into namespaced app), got %d: %+v", len(groups), groups)
	}

	g := findGroupByName(groups, "my-app")
	if g == nil {
		t.Fatal("Expected group for 'my-app'")
	}
	if g.EventCount != 2 {
		t.Errorf("Expected 2 events in 'my-app' group, got %d", g.EventCount)
	}
	if g.Namespace != "myns" {
		t.Errorf("Expected group namespace 'myns', got %q", g.Namespace)
	}
}

// TestGroupByApp_ClusterScopedNoMatchGetsOwnBucket verifies that a cluster-scoped
// event whose app label matches no namespaced group still gets its own
// appLabel-only bucket (no regression).
func TestGroupByApp_ClusterScopedNoMatchGetsOwnBucket(t *testing.T) {
	events := []TimelineEvent{
		{ID: "1", Kind: "Deployment", Namespace: "myns", Name: "my-app",
			Labels: map[string]string{"app.kubernetes.io/name": "my-app"}, Timestamp: time.Now()},
		{ID: "2", Kind: "ClusterIssuer", Namespace: "", Name: "lonely",
			Labels: map[string]string{"app.kubernetes.io/name": "other-app"}, Timestamp: time.Now()},
	}

	groups := GroupEvents(events, GroupByApp)

	if len(groups) != 2 {
		t.Fatalf("Expected 2 groups, got %d: %+v", len(groups), groups)
	}

	other := findGroupByName(groups, "other-app")
	if other == nil {
		t.Fatal("Expected own bucket for 'other-app'")
	}
	if other.EventCount != 1 {
		t.Errorf("Expected 1 event in 'other-app' group, got %d", other.EventCount)
	}
}

// TestGroupByApp_LabellessUnaffected verifies that label-less events (namespaced
// and cluster-scoped) keep their prior grouping behavior.
func TestGroupByApp_LabellessUnaffected(t *testing.T) {
	events := []TimelineEvent{
		{ID: "1", Kind: "ConfigMap", Namespace: "myns", Name: "cfg", Timestamp: time.Now()},
		{ID: "2", Kind: "ClusterRole", Namespace: "", Name: "role", Timestamp: time.Now()},
	}

	groups := GroupEvents(events, GroupByApp)

	// One __ungrouped__ bucket per distinct namespace ("myns" and "") — same as
	// pre-fix behavior for label-less events.
	if len(groups) != 2 {
		t.Fatalf("Expected 2 ungrouped groups, got %d: %+v", len(groups), groups)
	}
	for _, g := range groups {
		if g.Name != "__ungrouped__" {
			t.Errorf("Expected label-less events in '__ungrouped__', got group name %q", g.Name)
		}
	}
}
