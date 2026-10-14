package k8s

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestServiceAccountInventoryStateReachesSecretContext(t *testing.T) {
	for _, state := range []string{"pending", "failed", "ready_empty", "ready_match"} {
		t.Run(state, func(t *testing.T) {
			enabled := map[string]bool{
				k8score.Pods: true, k8score.Deployments: true, k8score.StatefulSets: true,
				k8score.DaemonSets: true, k8score.ReplicaSets: true, k8score.Jobs: true, k8score.CronJobs: true,
				k8score.Ingresses: true, k8score.ServiceAccounts: true,
			}
			release := make(chan struct{})
			cache := newAdapterCache(t, enabled, map[string]bool{k8score.ServiceAccounts: state == "pending" || state == "failed"}, 100*time.Millisecond, func(c *fake.Clientset) {
				switch state {
				case "pending":
					c.PrependReactor("list", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
						<-release
						return true, &corev1.ServiceAccountList{}, nil
					})
				case "failed":
					c.PrependReactor("list", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, nil, fmt.Errorf("fixture source failed")
					})
				case "ready_match":
					_, err := c.CoreV1().ServiceAccounts("team").Create(context.Background(), &corev1.ServiceAccount{
						ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "team"},
						Secrets:    []corev1.ObjectReference{{Name: "credentials"}},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Fatal(err)
					}
				}
			})
			t.Cleanup(func() { close(release) })
			if state == "pending" && cache.ServiceAccounts() == nil {
				t.Fatal("pending fixture must expose a non-nil unsynced lister")
			}
			if state == "failed" {
				deadline := time.Now().Add(2 * time.Second)
				for cache.KindReadinessFor(k8score.ServiceAccounts) != k8score.KindFailed && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if cache.KindReadinessFor(k8score.ServiceAccounts) != k8score.KindFailed {
					t.Fatal("fixture did not reach failed inventory state")
				}
			}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "team"}}
			got := resourcecontext.Build(context.Background(), secret, resourcecontext.Options{Provider: NewTopologyResourceProvider(cache)})
			want := resourcecontext.OmittedReason("")
			if state == "pending" {
				want = resourcecontext.OmittedCacheCold
			} else if state == "failed" {
				want = resourcecontext.OmittedUnavailable
			}
			if want != "" {
				if got.ReferencedBy != nil || !slices.Contains(got.Omitted, resourcecontext.OmittedField{Field: "referencedBy", Reason: want}) {
					t.Fatalf("inventory state lost at context boundary: %+v", got)
				}
			} else {
				if slices.ContainsFunc(got.Omitted, func(field resourcecontext.OmittedField) bool { return field.Field == "referencedBy" }) {
					t.Fatalf("ready inventory reported incomplete: %+v", got.Omitted)
				}
				if state == "ready_empty" && got.ReferencedBy != nil {
					t.Fatalf("empty inventory invented a consumer: %+v", got.ReferencedBy)
				}
				if state == "ready_match" && (got.ReferencedBy == nil || got.ReferencedBy.Total != 1 || got.ReferencedBy.Items[0].Kind != "ServiceAccount") {
					t.Fatalf("ready declaring account missing: %+v", got.ReferencedBy)
				}
			}
		})
	}
}
