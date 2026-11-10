package audit

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestRunFromCacheUsesServerVersionForResourceDefaults(t *testing.T) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
		Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}}}, {Name: "helper"}},
	}}}}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(d), map[string]k8score.ResourceScope{k8score.Deployments: {Enabled: true}, k8score.LimitRanges: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	var versionCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		versionCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"35","gitVersion":"v1.35.0"}`))
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	k8s.InvalidateServerVersionCache()
	t.Cleanup(func() { k8s.SetTestClient(previous); k8s.InvalidateServerVersionCache() })
	for _, opts := range []*RunOptions{nil, {}, {ClusterVersion: "1.37"}, {ClusterVersion: "1.35"}} {
		result := RunFromCache(k8s.GetResourceCache(), []string{"team"}, opts)
		count := result.CheckCounts["memoryRequestMissing"]
		if opts != nil && opts.ClusterVersion == "1.37" {
			if count.Evaluated != 0 {
				t.Fatalf("explicit version ignored: %+v", count)
			}
		} else if count.Evaluated != 1 || count.Passed != 0 {
			t.Fatalf("discovered legacy version ignored: %+v", count)
		}
	}
	if versionCalls.Load() != 1 {
		t.Fatalf("version discovery calls = %d, want 1", versionCalls.Load())
	}
}
