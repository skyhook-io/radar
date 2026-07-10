package timeline

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A relist re-emits the current state; the id must be identical whether the
// arrival is labeled add or update, so replays de-dupe.
func TestInformerEventID_StableAcrossAddUpdate(t *testing.T) {
	add := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", "100", EventTypeAdd, HealthHealthy, nil, nil, nil, nil)
	update := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", "100", EventTypeUpdate, HealthHealthy, nil, nil, nil, nil)
	if add.ID != update.ID {
		t.Fatalf("add/update of identical state must share an id: %q vs %q", add.ID, update.ID)
	}
	if add.ID == "" {
		t.Fatal("id must not be empty")
	}
}

// A new resourceVersion is a new state and must mint a new id.
func TestInformerEventID_ChangesWithResourceVersion(t *testing.T) {
	v1 := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", "100", EventTypeUpdate, HealthHealthy, nil, nil, nil, nil)
	v2 := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", "101", EventTypeUpdate, HealthHealthy, nil, nil, nil, nil)
	if v1.ID == v2.ID {
		t.Fatalf("distinct resourceVersions must yield distinct ids, got %q for both", v1.ID)
	}
}

// A delete is a distinct state from the add/update of the same version.
func TestInformerEventID_DeleteIsDistinct(t *testing.T) {
	add := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", "100", EventTypeAdd, HealthHealthy, nil, nil, nil, nil)
	del := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", "100", EventTypeDelete, HealthUnknown, nil, nil, nil, nil)
	if add.ID == del.ID {
		t.Fatalf("delete id must differ from add id, got %q for both", add.ID)
	}
}

// The uid disambiguates clusters for informer ids (uids are globally unique),
// so two clusters observing distinct resources never collide.
func TestInformerEventID_DistinctUIDsDistinctIDs(t *testing.T) {
	a := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-a", "100", EventTypeAdd, HealthHealthy, nil, nil, nil, nil)
	b := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-b", "100", EventTypeAdd, HealthHealthy, nil, nil, nil, nil)
	if a.ID == b.ID {
		t.Fatalf("distinct uids must yield distinct ids, got %q for both", a.ID)
	}
}

// Historical ids carry no uid, so two clusters with a same-named resource at
// the same instant would collide in one persistent store without the cluster
// qualifier.
func TestHistoricalEventID_ClusterQualified(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	a := NewHistoricalEvent("cluster-a", "Deployment", "apps/v1", "team-a", "web", ts, "created", "", HealthUnknown, nil, nil)
	b := NewHistoricalEvent("cluster-b", "Deployment", "apps/v1", "team-a", "web", ts, "created", "", HealthUnknown, nil, nil)
	if a.ID == b.ID {
		t.Fatalf("historical ids must differ across clusters, got %q for both", a.ID)
	}

	// Same cluster + same content stays deterministic (no duplicates on restart).
	again := NewHistoricalEvent("cluster-a", "Deployment", "apps/v1", "team-a", "web", ts, "created", "", HealthUnknown, nil, nil)
	if a.ID != again.ID {
		t.Fatalf("historical id must be deterministic within a cluster: %q vs %q", a.ID, again.ID)
	}
}

// The GitOps identity labels must survive the grouping filter — the server's
// argo:/helm: app matchKeys join deleted members' events by exactly these.
func TestExtractLabels_KeepsGitOpsIdentityLabels(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shop-worker",
			Namespace: "team-a",
			Labels: map[string]string{
				"argocd.argoproj.io/instance": "shop",
				"helm.toolkit.fluxcd.io/name": "shop",
				"app.kubernetes.io/part-of":   "shop-suite",
				"pod-template-hash":           "abc123",
			},
		},
	}
	got := ExtractLabels(pod)
	for _, key := range []string{"argocd.argoproj.io/instance", "helm.toolkit.fluxcd.io/name", "app.kubernetes.io/part-of"} {
		if got[key] == "" {
			t.Errorf("grouping label %s was filtered out", key)
		}
	}
	if _, ok := got["pod-template-hash"]; ok {
		t.Errorf("non-grouping label leaked through the filter")
	}
}
