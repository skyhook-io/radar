package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// A workload's stale Secret-backed env reaches a caller who can't read the
// Secret in a degraded form, without the Secret, its keys or when it changed.
// A missing TLS Secret names only what the Ingress names, so it needs only
// namespace access.
func TestDashboardSecretDerivedProblemsDegrade(t *testing.T) {
	ns := "shop-tls"
	now := time.Now().UTC().Truncate(time.Second)
	started := metav1.NewTime(now.Add(-2 * time.Minute))
	controller := true
	client := fake.NewClientset(
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "front", Namespace: ns, CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
			Spec:       networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "shop-tls"}}},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: ns, UID: "dep", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
			Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}}},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "worker-abc", Namespace: ns, UID: "rs",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "worker", UID: "dep", Controller: &controller}},
		}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-abc-1", Namespace: ns, CreationTimestamp: started,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "worker-abc", UID: "rs", Controller: &controller}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{
				Name: "DB_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}, Key: "password",
				}},
			}}}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
				}},
			},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: ns}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(timeline.ResetStore)
	if err := timeline.RecordEvent(context.Background(), timeline.TimelineEvent{
		ID: "db-conn-change", Timestamp: now.Add(-time.Minute), Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), Kind: "Secret", Namespace: ns, Name: "db-conn",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"password"}}}},
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	ctx := withTestUserPerms(t, "tls-user", nil, []string{ns})
	perms := getPermCache().Get("tls-user", nil)
	perms.SetCanI("get", "", "secrets", ns, false)

	problems := func() (stale string, tls bool) {
		for _, p := range buildDashboard(ctx, k8s.GetResourceCache(), ns, false, false).Problems {
			switch {
			case p.Name == "front" && p.Reason == "Missing TLS Secret":
				tls = true
			case p.Name == "worker" && p.Reason == "StaleSecretEnv":
				stale = p.Message
			}
		}
		return stale, tls
	}
	stale, tls := problems()
	if !tls {
		t.Fatal("a caller with namespace access lost the missing-TLS problem")
	}
	if !strings.Contains(stale, "may hold stale Secret-backed environment values") || strings.Contains(stale, "db-conn") {
		t.Fatalf("stale-env problem without Secret access = %q, want the degraded form", stale)
	}
	perms.SetCanI("get", "", "secrets", ns, true)
	if stale, _ := problems(); !strings.Contains(stale, "before Radar observed") {
		t.Fatalf("stale-env problem with Secret access = %q, want the full form", stale)
	}
}

// A cluster-scoped problem needs list on its kind, even for a caller who can
// read Pods everywhere.
func TestDashboardClusterScopedProblemsNeedListOnTheKind(t *testing.T) {
	client := fake.NewClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "dangling"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "gone"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "someone"}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	ctx := withTestUserPerms(t, "pod-reader", nil, nil)
	perms := getPermCache().Get("pod-reader", nil)
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", false)

	dangling := func() bool {
		for _, p := range buildDashboard(ctx, k8s.GetResourceCache(), "", false, false).Problems {
			if p.Kind == "ClusterRoleBinding" && p.Name == "dangling" {
				return true
			}
		}
		return false
	}
	if dangling() {
		t.Fatal("a caller who can't list ClusterRoleBindings saw one's problem")
	}
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", true)
	if !dangling() {
		t.Fatal("a caller who can list ClusterRoleBindings lost the problem")
	}
}
