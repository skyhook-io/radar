package server

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/topology"
)

// withConfigReferences adds a ConfigMap and a Secret, the Deployment and Pod
// that declare both, and an unrelated ConfigMap to the shared fixture until
// the test ends.
func withConfigReferences(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	spec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg-settings"}}}},
			Env: []corev1.EnvVar{{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "cfg-db"}, Key: "password"}}}},
		}},
	}
	labels := map[string]string{"app": "cfg-web"}
	objects := []struct {
		create, remove func() error
	}{
		{func() error {
			_, err := testFakeClient.CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cfg-settings"}}, metav1.CreateOptions{})
			return err
		}, func() error {
			return testFakeClient.CoreV1().ConfigMaps("default").Delete(ctx, "cfg-settings", metav1.DeleteOptions{})
		}},
		{func() error {
			_, err := testFakeClient.CoreV1().Secrets("default").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cfg-db"}}, metav1.CreateOptions{})
			return err
		}, func() error {
			return testFakeClient.CoreV1().Secrets("default").Delete(ctx, "cfg-db", metav1.DeleteOptions{})
		}},
		{func() error {
			_, err := testFakeClient.CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cfg-unused"}}, metav1.CreateOptions{})
			return err
		}, func() error {
			return testFakeClient.CoreV1().ConfigMaps("default").Delete(ctx, "cfg-unused", metav1.DeleteOptions{})
		}},
		{func() error {
			_, err := testFakeClient.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cfg-web"},
				Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec}},
			}, metav1.CreateOptions{})
			return err
		}, func() error {
			return testFakeClient.AppsV1().Deployments("default").Delete(ctx, "cfg-web", metav1.DeleteOptions{})
		}},
		{func() error {
			_, err := testFakeClient.CoreV1().Pods("default").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "cfg-web-1", Labels: labels}, Spec: spec}, metav1.CreateOptions{})
			return err
		}, func() error {
			return testFakeClient.CoreV1().Pods("default").Delete(ctx, "cfg-web-1", metav1.DeleteOptions{})
		}},
	}
	for _, o := range objects {
		if err := o.create(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = o.remove() })
	}
	cache := k8s.GetResourceCache()
	waitFor(t, 5*time.Second, func() bool {
		_, cmErr := cache.ConfigMaps().ConfigMaps("default").Get("cfg-settings")
		_, podErr := cache.Pods().Pods("default").Get("cfg-web-1")
		_, deployErr := cache.Deployments().Deployments("default").Get("cfg-web")
		_, secretErr := cache.Secrets().Secrets("default").Get("cfg-db")
		return cmErr == nil && podErr == nil && deployErr == nil && secretErr == nil
	})
}

func TestRelatedResourcesAnswerConfigReferencesFromTheCache(t *testing.T) {
	withOwnershipDiscovery(t)
	withConfigReferences(t)
	get := func(path string) *topology.Relationships {
		resp, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return relationshipsOf(t, resp)
	}

	settings := get("/api/resources/configmaps/default/cfg-settings")
	consumers := ownershipRefNames(settings.Consumers)
	if !slices.Contains(consumers, "Deployment default/cfg-web") || !slices.Contains(consumers, "Pod default/cfg-web-1") {
		t.Errorf("ConfigMap consumers = %v, want the Deployment and the Pod mounting it", consumers)
	}
	if _, answered := settings.Coverage[topology.ConfigReferenceFamily]; !answered {
		t.Errorf("coverage = %+v, want the config-reference family answered", settings.Coverage)
	}

	if unused := get("/api/resources/configmaps/default/cfg-unused"); len(unused.Consumers) != 0 {
		t.Errorf("an unused ConfigMap has consumers %v", ownershipRefNames(unused.Consumers))
	}

	web := get("/api/resources/deployments/default/cfg-web?group=apps")
	if refs := ownershipRefNames(web.ConfigRefs); !slices.Contains(refs, "ConfigMap default/cfg-settings") || !slices.Contains(refs, "Secret default/cfg-db") {
		t.Errorf("Deployment configRefs = %v, want the ConfigMap and Secret it declares", refs)
	}
	pod := get("/api/resources/pods/default/cfg-web-1")
	if refs := ownershipRefNames(pod.ConfigRefs); !slices.Equal(refs, []string{"ConfigMap default/cfg-settings", "Secret default/cfg-db"}) {
		t.Errorf("Pod configRefs = %v, want the ConfigMap and the Secret, once each", refs)
	}
	if c := web.Coverage[topology.ConfigReferenceFamily]; c.Narrowed || len(c.NotChecked) > 0 {
		t.Errorf("Deployment coverage = %+v; everything is collected and readable", c)
	}
}

// A user who may read the namespace but not list its Secrets sees what a
// workload declares except the Secrets, and the answer says it was narrowed,
// as the per-user graph withholds Secret nodes.
func TestRelatedResourcesConfigReferencesWithholdSecretsAUserMayNotList(t *testing.T) {
	withOwnershipDiscovery(t)
	withConfigReferences(t)
	env := newAuthTestServer(t)
	env.srv.broadcaster.markCachedTopologyDirty()
	env.srv.permCache.Set("carol", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	perms := env.srv.permCache.Get("carol", nil)
	perms.SetCanI("list", "", "secrets", "default", false)
	perms.SetCanI("list", "", "limitranges", "default", true)
	perms.SetCanI("list", "*", "*", "", false)

	rel := relationshipsOf(t, env.authGet(t, "/api/resources/deployments/default/cfg-web?group=apps", "carol", ""))
	if refs := ownershipRefNames(rel.ConfigRefs); !slices.Equal(refs, []string{"ConfigMap default/cfg-settings"}) {
		t.Errorf("configRefs = %v, want the ConfigMap only", refs)
	}
	if !rel.Coverage[topology.ConfigReferenceFamily].Narrowed {
		t.Errorf("coverage = %+v; a Secret was withheld, so narrowed", rel.Coverage)
	}
	consumers := relationshipsOf(t, env.authGet(t, "/api/resources/configmaps/default/cfg-settings", "carol", ""))
	if got := ownershipRefNames(consumers.Consumers); !slices.Contains(got, "Pod default/cfg-web-1") || consumers.Coverage[topology.ConfigReferenceFamily].Narrowed {
		t.Errorf("consumers = %v, coverage %+v; the namespace is readable, so all of them", got, consumers.Coverage)
	}
}
