package issues

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// A rollout whose surge pod cannot start keeps every desired replica serving,
// so the Deployment raises no capacity row; the failure must still reach the
// queue through the pod's own row.
func TestCompose_FailingSurgePodSurfacesWithoutCapacityRollup(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		podAge time.Duration
		spec   corev1.PodSpec
		status corev1.PodStatus
		want   issuesapi.Category
	}{
		{
			name:   "image pull",
			podAge: 2 * time.Minute,
			spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "checkout", Image: "registry.example/checkout:missing"}}},
			status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "checkout",
					Image: "registry.example/checkout:missing",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: `Back-off pulling image "registry.example/checkout:missing"`,
					}},
				}},
			},
			want: issuesapi.CategoryImagePullFailed,
		},
		{
			name:   "readiness gate past its grace",
			podAge: 10 * time.Minute,
			spec: corev1.PodSpec{
				Containers:     []corev1.Container{{Name: "checkout", Image: "registry.example/checkout:v2"}},
				ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "target-health.elbv2.k8s.aws/checkout"}},
			},
			status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady", LastTransitionTime: metav1.NewTime(now.Add(-9 * time.Minute))},
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(now.Add(-9 * time.Minute))},
				},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "checkout", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
			},
			want: issuesapi.CategoryReadinessFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer k8s.ResetTestState()

			controller := true
			replicas := int32(2)
			client := fake.NewClientset(
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour)), Generation: 2},
					Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
					Status: appsv1.DeploymentStatus{
						ObservedGeneration:  2,
						Replicas:            3,
						UpdatedReplicas:     1,
						ReadyReplicas:       2,
						AvailableReplicas:   2,
						UnavailableReplicas: 1,
						Conditions: []appsv1.DeploymentCondition{
							{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
							{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"},
						},
					},
				},
				&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
					Name:      "checkout-new",
					Namespace: "shop",
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "checkout",
						Controller: &controller,
					}},
				}},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "checkout-new-x1",
						Namespace:         "shop",
						CreationTimestamp: metav1.NewTime(now.Add(-tc.podAge)),
						OwnerReferences: []metav1.OwnerReference{{
							APIVersion: "apps/v1",
							Kind:       "ReplicaSet",
							Name:       "checkout-new",
							Controller: &controller,
						}},
					},
					Spec:   tc.spec,
					Status: tc.status,
				},
			)
			if err := k8s.InitTestResourceCache(client); err != nil {
				t.Fatalf("InitTestResourceCache: %v", err)
			}
			provider := &CacheProvider{cache: k8s.GetResourceCache()}

			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if len(provider.DetectProblems([]string{"shop"})) > 0 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}

			got := Compose(provider, Filters{Namespaces: []string{"shop"}, Limit: NoLimit})
			var found bool
			for _, i := range got {
				if i.Category == issuesapi.CategoryWorkloadDegraded {
					t.Fatalf("issues = %+v, want no workload_degraded row while every desired replica is available", got)
				}
				if i.Category == tc.want && i.Name == "checkout-new-x1" {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %+v, want the surge pod's %s row", got, tc.want)
			}
		})
	}
}
