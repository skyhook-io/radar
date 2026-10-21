package topology

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

func replicaSetChild(name string) ResourceRef {
	return ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: name}
}

// A Deployment's or Rollout's old revisions, which run no Pods, are kept apart
// from the ReplicaSets that do, and from its current revision even at zero
// replicas.
func TestSplitOlderRevisions(t *testing.T) {
	const deploymentRevision = "deployment.kubernetes.io/revision"
	deployment := resourceid.GroupKind{Group: "apps", Kind: "Deployment"}
	rollout := resourceid.GroupKind{Group: "argoproj.io", Kind: "Rollout"}
	type cached struct {
		rs       *appsv1.ReplicaSet
		runsPods bool
	}
	rs := func(name string, desired, running int32, annotations map[string]string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, Annotations: annotations},
			Spec:       appsv1.ReplicaSetSpec{Replicas: &desired},
			Status:     appsv1.ReplicaSetStatus{Replicas: running},
		}
	}
	revision := func(a, r string) map[string]string { return map[string]string{a: r} }
	terminating := rs("web-terminating", 0, 0, revision(deploymentRevision, "2"))
	terminatingPods := int32(1)
	terminating.Status.TerminatingReplicas = &terminatingPods
	caches := map[string]cached{
		"web-live":        {rs("web-live", 3, 3, revision(deploymentRevision, "5")), true},
		"web-draining":    {rs("web-draining", 0, 2, revision(deploymentRevision, "4")), true},
		"web-old":         {rs("web-old", 0, 0, revision(deploymentRevision, "3")), false},
		"web-current":     {rs("web-current", 0, 0, revision(deploymentRevision, "5")), false},
		"web-terminating": {terminating, false},
		// A cluster that doesn't report terminatingReplicas still holds the
		// Pod naming it.
		"web-pod-left": {rs("web-pod-left", 0, 0, revision(deploymentRevision, "1")), true},
		"roll-old":     {rs("roll-old", 0, 0, revision(rollouts.RevisionAnnotation, "1")), false},
		"roll-current": {rs("roll-current", 0, 0, revision(rollouts.RevisionAnnotation, "2")), false},
	}
	read := func(namespace, name string) (*appsv1.ReplicaSet, bool, bool) {
		c, ok := caches[name]
		return c.rs, c.runsPods, ok && namespace == "shop"
	}
	pod := ResourceRef{Kind: "Pod", Namespace: "shop", Name: "web-live-a"}
	for _, tc := range []struct {
		name             string
		owner            resourceid.GroupKind
		annotations      map[string]string
		children         []string
		active, inactive []string
	}{
		{"a Deployment rolled out", deployment, revision(deploymentRevision, "5"),
			[]string{"web-live", "web-draining", "web-old"}, []string{"web-live", "web-draining"}, []string{"web-old"}},
		{"a Deployment scaled to zero keeps its current revision", deployment, revision(deploymentRevision, "5"),
			[]string{"web-current", "web-old"}, []string{"web-current"}, []string{"web-old"}},
		{"a Rollout scaled to zero keeps its current revision", rollout, revision(rollouts.RevisionAnnotation, "2"),
			[]string{"roll-current", "roll-old"}, []string{"roll-current"}, []string{"roll-old"}},
		{"a Rollout reads its own revision, not a Deployment's", rollout,
			map[string]string{deploymentRevision: "7", rollouts.RevisionAnnotation: "2"},
			[]string{"roll-current", "roll-old"}, []string{"roll-current"}, []string{"roll-old"}},
		{"another owner kind moves nothing, whatever it records", resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, revision(deploymentRevision, "5"),
			[]string{"web-current", "web-old"}, []string{"web-current", "web-old"}, nil},
		{"an owner that records no revision moves nothing", deployment, nil,
			[]string{"web-current", "web-old"}, []string{"web-current", "web-old"}, nil},
		{"a revision whose Pods are still terminating stays", deployment, revision(deploymentRevision, "5"),
			[]string{"web-terminating", "web-pod-left", "web-old"}, []string{"web-terminating", "web-pod-left"}, []string{"web-old"}},
		{"a ReplicaSet the caches can't read stays", deployment, revision(deploymentRevision, "5"),
			[]string{"web-gone", "web-old"}, []string{"web-gone"}, []string{"web-old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := &Relationships{Children: []ResourceRef{pod}}
			for _, c := range tc.children {
				rel.Children = append(rel.Children, replicaSetChild(c))
			}
			SplitOlderRevisions(rel, tc.owner, tc.annotations, read)
			want := []ResourceRef{pod}
			for _, c := range tc.active {
				want = append(want, replicaSetChild(c))
			}
			var wantInactive []ResourceRef
			for _, c := range tc.inactive {
				wantInactive = append(wantInactive, replicaSetChild(c))
			}
			if !slices.Equal(rel.Children, want) || !slices.Equal(rel.OlderRevisions, wantInactive) {
				t.Errorf("children %v inactive %v; want %v and %v", rel.Children, rel.OlderRevisions, want, wantInactive)
			}
		})
	}
}
