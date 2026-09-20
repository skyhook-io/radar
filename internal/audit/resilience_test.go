package audit

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHAPlacementUsesCollectedReplicaSetScope(t *testing.T) {
	for _, scope := range []string{"", "other"} {
		t.Run(scope, func(t *testing.T) {
			controller := true
			replicas := int32(2)
			d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app", UID: "deployment"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "app", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: &controller}}}}
			objs := []runtime.Object{d, rs}
			for _, name := range []string{"one", "two"} {
				objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
			}
			err := k8s.InitScopedTestResourceCache(fake.NewClientset(objs...), map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true}, k8score.Deployments: {Enabled: true}, k8score.ReplicaSets: {Enabled: true, Namespace: scope}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			input := CollectTypedInput(k8s.GetResourceCache(), []string{"app"})
			if input.ReplicaSets == nil {
				t.Fatal("enabled scope must be empty nonnil, not unavailable")
			}
			result := bp.RunChecks(input)
			if scope == "" {
				if result.CheckCounts["podHARisk"].Evaluated != 1 || result.CheckCounts["podHARisk"].Passed != 0 {
					t.Fatalf("lost real colocation: %+v", result.CheckCounts)
				}
			} else {
				if result.CheckCounts["podHARisk"].Evaluated != 0 || !slices.Contains(result.MissingInputs, "replicaset-ownership") {
					t.Fatalf("partial cache fabricated passing coverage: %+v", result)
				}
			}
		})
	}
}
