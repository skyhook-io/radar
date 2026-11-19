package k8s

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// A Deployment's uses lists what its template declares whether or not Radar
// holds the declared objects, as the Helm chart's default collects no
// Secrets: each list is complete, so none is marked, whether Secrets aren't
// collected, are still loading or are held.
func TestDeploymentContextUsesWithoutCollectingSecrets(t *testing.T) {
	for _, state := range []string{"not_collected", "pending", "collected"} {
		t.Run(state, func(t *testing.T) {
			enabled := map[string]bool{
				k8score.Pods: true, k8score.Deployments: true, k8score.ReplicaSets: true, k8score.ConfigMaps: true,
				k8score.PersistentVolumeClaims: true, k8score.ServiceAccounts: true, k8score.Secrets: state != "not_collected",
			}
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			deployment := &appsv1.Deployment{
				TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team", UID: "web-uid"},
				Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					ServiceAccountName: "runner",
					ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "registry"}},
					Containers:         []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}},
					Volumes:            []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}},
				}}},
			}
			cache := newAdapterCache(t, enabled, map[string]bool{k8score.Secrets: state == "pending"}, 100*time.Millisecond, func(c *fake.Clientset) {
				ctx := context.Background()
				for _, create := range []func() error{
					func() error {
						_, err := c.AppsV1().Deployments("team").Create(ctx, deployment, metav1.CreateOptions{})
						return err
					},
					func() error {
						_, err := c.CoreV1().ConfigMaps("team").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "team"}}, metav1.CreateOptions{})
						return err
					},
					func() error {
						_, err := c.CoreV1().PersistentVolumeClaims("team").Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team"}}, metav1.CreateOptions{})
						return err
					},
					func() error {
						_, err := c.CoreV1().ServiceAccounts("team").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runner", Namespace: "team"}}, metav1.CreateOptions{})
						return err
					},
					func() error {
						_, err := c.CoreV1().Secrets("team").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "team"}}, metav1.CreateOptions{})
						return err
					},
				} {
					if err := create(); err != nil {
						t.Fatal(err)
					}
				}
				if state == "pending" {
					c.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
						<-release
						return true, &corev1.SecretList{}, nil
					})
				}
			})
			discovery := buildFakeDiscoveryCore(t)
			kinds := slices.Clone(relations.ConfigReferences.Targets)
			for _, source := range relations.ConfigReferences.Sources {
				kinds = append(kinds, source.Kind)
			}
			for _, kind := range kinds {
				builtin, ok := resourceid.BuiltinForKind(kind.Kind)
				if !ok || builtin.GroupKind() != kind {
					continue
				}
				discovery.AddAPIResource(k8score.APIResource{
					Group: builtin.Group, Version: builtin.Version, Kind: builtin.Kind, Name: builtin.Resource,
					Namespaced: builtin.Namespaced, Verbs: metav1.Verbs{"get", "list", "watch", "delete"},
				})
			}
			previous := ownershipSource
			ownershipSource = cachesource.New(func() cachesource.Caches {
				return cachesource.Caches{Typed: cache.ResourceCache, Discovery: discovery}
			})
			t.Cleanup(func() { ownershipSource = previous })

			access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
			rel, _, extras := AnswerResourceContextRelationships(cache, deployment, access, access)
			if extras == nil {
				t.Fatal("no relationship answer")
			}
			rc := resourcecontext.Build(context.Background(), deployment, resourcecontext.Options{Relationships: rel, RelationshipExtras: extras})
			for _, o := range rc.Omitted {
				if strings.HasPrefix(o.Field, "uses") {
					t.Errorf("complete list marked: %+v", o)
				}
			}
			names := func(refs []resourcecontext.ContextRef) []string {
				var out []string
				for _, r := range refs {
					out = append(out, r.Name)
				}
				return out
			}
			if u := rc.Uses; u == nil || !slices.Equal(names(u.ConfigMaps), []string{"settings"}) || !slices.Equal(names(u.Secrets), []string{"registry"}) || !slices.Equal(names(u.PVCs), []string{"data"}) || u.ServiceAccount == nil || u.ServiceAccount.Name != "runner" {
				t.Fatalf("uses = %+v", rc.Uses)
			}
		})
	}
}
