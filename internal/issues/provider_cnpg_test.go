package issues

import (
	"errors"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/cnpg"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCNPGPodInventoryUsesCacheScopeAndOwnership(t *testing.T) {
	cluster := cnpgCluster(nil, nil)
	cluster.SetUID("current")
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg-main-1", Namespace: "pg", Labels: map[string]string{"cnpg.io/cluster": "pg-main"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: cnpg.Group + "/v1", Kind: "Cluster", Name: "pg-main", UID: "current", Controller: &controller}}}}
	stale := pod.DeepCopy()
	stale.Name = "old"
	stale.OwnerReferences[0].UID = "old"
	job := pod.DeepCopy()
	job.Name = "backup-job"
	job.OwnerReferences[0].Kind = "Job"
	for _, scoped := range []bool{false, true} {
		t.Run(map[bool]string{false: "default cluster scope", true: "namespace scope"}[scoped], func(t *testing.T) {
			k8s.ResetResourceCache()
			t.Cleanup(k8s.ResetResourceCache)
			client := fake.NewClientset(pod, stale, job)
			var err error
			if scoped {
				err = k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "pg"}})
			} else {
				err = k8s.InitTestResourceCache(client)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := &CacheProvider{cache: k8s.GetResourceCache()}
			instances, known := p.cnpgInstancePods(cluster)
			if !known || len(instances) != 1 || instances[0].Name != pod.Name {
				t.Fatalf("inventory=%v known=%v", instances, known)
			}
			other := cluster.DeepCopy()
			other.SetNamespace("other")
			_, known = p.cnpgInstancePods(other)
			if known == scoped {
				t.Fatalf("other namespace known=%v, scoped=%v", known, scoped)
			}
		})
	}
	client := fake.NewClientset()
	release := make(chan struct{})
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case <-release:
			return false, nil, nil
		default:
			return true, nil, errors.New("initial list pending")
		}
	})
	startupKnown := false
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: client, ResourceTypes: map[string]bool{"pods": true},
		OnInformersStarted: func(core *k8score.ResourceCache) {
			p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: core}}
			_, startupKnown = p.cnpgInstancePods(cluster)
			close(release)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	if startupKnown {
		t.Fatal("unsynced cache claimed a known inventory")
	}
}
