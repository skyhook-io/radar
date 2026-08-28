package k8s

import (
	"errors"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

func TestGetWorkloadSelectorResolvesRolloutWorkloadRef(t *testing.T) {
	deploymentSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	replicaSetSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api-source", Namespace: "prod"}, Spec: appsv1.DeploymentSpec{Selector: deploymentSelector}},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "worker-source", Namespace: "prod"}, Spec: appsv1.ReplicaSetSpec{Selector: replicaSetSelector}},
		),
		ResourceTypes: map[string]bool{k8score.Deployments: true, k8score.ReplicaSets: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}

	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	newRollout := func(name, refAPIVersion, refKind, refName string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Rollout",
			"metadata":   map[string]any{"name": name, "namespace": "prod"},
			"spec": map[string]any{
				"workloadRef": map[string]any{"apiVersion": refAPIVersion, "kind": refKind, "name": refName},
			},
		}}
	}
	directRollout := newRollout("direct", "apps/v1", "Deployment", "missing")
	directRollout.Object["spec"].(map[string]any)["selector"] = map[string]any{"matchLabels": map[string]any{"app": "direct"}}
	nullRollout := newRollout("null", "apps/v1", "Deployment", "api-source")
	nullRollout.Object["spec"].(map[string]any)["selector"] = nil
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"},
		newRollout("api", "apps/v1", "Deployment", "api-source"),
		newRollout("worker", "apps/v1", "ReplicaSet", "worker-source"),
		directRollout,
		nullRollout,
		newRollout("template", "v1", "PodTemplate", "api-template"),
		newRollout("missing", "apps/v1", "Deployment", "missing"),
		newRollout("wrong-group", "example.io/v1", "Deployment", "api-source"),
	)
	if err := InitTestDynamicResourceCache(dynamicClient, []APIResource{{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Namespaced: true, Verbs: []string{"list", "watch"},
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(ResetTestDynamicState)

	rolloutSelector := func(selector *metav1.LabelSelector) *metav1.LabelSelector {
		selector = selector.DeepCopy()
		selector.MatchExpressions = []metav1.LabelSelectorRequirement{{
			Key: rollouts.PodTemplateHashLabel, Operator: metav1.LabelSelectorOpExists,
		}}
		return selector
	}
	for _, tc := range []struct {
		name    string
		want    *metav1.LabelSelector
		wantErr func(error) bool
	}{
		{name: "api", want: rolloutSelector(deploymentSelector)},
		{name: "worker", want: rolloutSelector(replicaSetSelector)},
		{name: "direct", want: rolloutSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"app": "direct"}})},
		{name: "null", want: rolloutSelector(deploymentSelector)},
		{name: "template", wantErr: func(err error) bool { return errors.Is(err, ErrWorkloadSelectorUnavailable) }},
		{name: "missing", wantErr: apierrors.IsNotFound},
		{name: "wrong-group", wantErr: func(err error) bool { return errors.Is(err, rollouts.ErrWorkloadRefUnsupported) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GetWorkloadSelector(cache, "rollout", "prod", tc.name)
			if tc.wantErr != nil {
				if !tc.wantErr(err) {
					t.Fatalf("GetWorkloadSelector error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetWorkloadSelector: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selector = %+v, want %+v", got, tc.want)
			}
		})
	}

	if _, err := ResolveRolloutSelector(&ResourceCache{}, newRollout("denied", "apps/v1", "Deployment", "api-source")); !errors.Is(err, ErrWorkloadAccessDenied) {
		t.Fatalf("access-denied error = %v", err)
	}
}
