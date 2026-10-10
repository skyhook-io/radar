package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestFluxLabelsOverrideHelmInstanceAndCrossNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name, namespace string
		labels          map[string]string
		want            bool
	}{
		{"crossnamespace", "prod", map[string]string{fluxHelmNameLabel: "app", fluxHelmNSLabel: "flux"}, true},
		{"same", "flux", map[string]string{fluxHelmNameLabel: "app", fluxHelmNSLabel: "flux"}, true},
		{"wrongname", "flux", map[string]string{fluxHelmNameLabel: "other", fluxHelmNSLabel: "flux", "app.kubernetes.io/instance": "app"}, false},
		{"wrongnamespace", "flux", map[string]string{fluxHelmNameLabel: "app", fluxHelmNSLabel: "other", "app.kubernetes.io/instance": "app"}, false},
		{"missingnamespace", "flux", map[string]string{fluxHelmNameLabel: "app", "app.kubernetes.io/instance": "app"}, false},
		{"missingname", "flux", map[string]string{fluxHelmNSLabel: "flux", "app.kubernetes.io/instance": "app"}, false},
		{"kustomize", "flux", map[string]string{fluxKustomizeNameLabel: "other", fluxKustomizeNSLabel: "flux", "app.kubernetes.io/instance": "app"}, false},
		{"fallbacksame", "flux", map[string]string{"app.kubernetes.io/instance": "app"}, true},
		{"fallbackcross", "prod", map[string]string{"app.kubernetes.io/instance": "app"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hrGVR := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
			roGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
			hr := genericIdentityObject(hrGVR, "HelmRelease", "flux", "app")
			hr.Object["spec"] = map[string]any{"targetNamespace": tc.namespace}
			rollout := genericIdentityObject(roGVR, "Rollout", tc.namespace, "rollout")
			rollout.SetLabels(tc.labels)
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{hrGVR, roGVR}, kinds: map[schema.GroupVersionResource]string{hrGVR: "HelmRelease", roGVR: "Rollout"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{hrGVR: {hr}, roGVR: {rollout}}, listCalls: map[schema.GroupVersionResource]int{}}
			meta := func(name string) metav1.ObjectMeta {
				return metav1.ObjectMeta{Namespace: tc.namespace, Name: name, Labels: tc.labels}
			}
			provider := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: meta("deployment")}}, services: []*corev1.Service{{ObjectMeta: meta("service")}}, statefulSets: []*appsv1.StatefulSet{{ObjectMeta: meta("statefulset")}}, daemonSets: []*appsv1.DaemonSet{{ObjectMeta: meta("daemonset")}}, jobs: []*batchv1.Job{{ObjectMeta: meta("job")}}, cronJobs: []*batchv1.CronJob{{ObjectMeta: meta("cronjob")}}}
			topo, err := NewBuilder(provider).WithDynamic(d).Build(DefaultBuildOptions())
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"deployment", "service", "statefulset", "daemonset", "job", "cronjob", "rollout"} {
				id := kind + "/" + tc.namespace + "/" + kind
				if nodeByID(topo.Nodes, id) == nil {
					t.Fatalf("missing fixture %s", id)
				}
				found := false
				for _, e := range topo.Edges {
					found = found || e.Source == "helmrelease/flux/app" && e.Target == id && e.Type == EdgeManages
				}
				if found != tc.want {
					t.Errorf("%s management=%v want %v", id, found, tc.want)
				}
			}
		})
	}
}
