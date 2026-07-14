package cloudinstall

import (
	"context"
	"errors"
	"strings"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

const freshPreflightManifest = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: radar-config
data:
  mode: cloud
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar-cloud
rules:
- apiGroups: [""]
  resources: ["users"]
  verbs: ["impersonate"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: radar-cloud-owner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: admin
subjects:
- kind: Group
  name: cloud:owner
  apiGroup: rbac.authorization.k8s.io
`

func TestFreshInstallPreflight_ExistingNamespaceDryRunsExactCreates(t *testing.T) {
	var reviews []authv1.ResourceAttributes
	kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
		reviews = append(reviews, attrs)
		return true
	}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}})
	dc := adoptionDynamicClient()
	dc.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if object.GetLabels()["app.kubernetes.io/managed-by"] != "Helm" ||
			object.GetAnnotations()["meta.helm.sh/release-name"] != "radar" ||
			object.GetAnnotations()["meta.helm.sh/release-namespace"] != "radar" {
			t.Errorf("dry-run object is missing Helm tracking metadata: labels=%v annotations=%v", object.GetLabels(), object.GetAnnotations())
		}
		return false, nil, nil
	})

	result, err := FreshInstallPreflight(context.Background(), kc, dc, adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || len(result.Advisory) != 0 {
		t.Fatalf("expected clean exact preflight, got %+v", result)
	}

	created := make(map[string]bool)
	for _, action := range dc.Actions() {
		if action.GetVerb() != "create" {
			continue
		}
		create := action.(k8stesting.CreateActionImpl)
		assertDryRunAll(t, create.GetCreateOptions().DryRun, "create", action.GetResource().Resource)
		created[action.GetResource().Resource] = true
	}
	for _, resource := range []string{"configmaps", "clusterroles", "clusterrolebindings"} {
		if !created[resource] {
			t.Errorf("missing server dry-run create for %s; got %v", resource, created)
		}
	}
	assertFreshSecretDryRunCreates(t, kc.Actions(), CloudTokenSecretName, helmReleaseSecretName("radar", 1))
	assertReviewed(t, reviews, "delete", "secrets", CloudTokenSecretName)
	assertReviewed(t, reviews, "update", "secrets", helmReleaseSecretName("radar", 1))
}

func TestFreshInstallPreflight_DoesNotRepeatPreparedResourceGets(t *testing.T) {
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true },
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}})
	dc := adoptionDynamicClient()
	dc.PrependReactor("get", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource},
			action.(k8stesting.GetAction).GetName(), errors.New("get denied"))
	})

	result, err := FreshInstallPreflight(context.Background(), kc, dc, adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err != nil || !result.OK() {
		t.Fatalf("mutation preflight should not repeat preparation's GETs, result=%+v err=%v", result, err)
	}
	for _, action := range dc.Actions() {
		if action.GetVerb() == "get" {
			t.Fatalf("fresh create preflight performed unexpected GET: %#v", action)
		}
	}
}

func TestFreshInstallPreflight_RBACAdmissionDenialBlocksBeforeEnrollment(t *testing.T) {
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true },
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}})
	dc := adoptionDynamicClient()
	dc.PrependReactor("create", "clusterrolebindings", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource},
			"radar-cloud-owner", errors.New("RBAC admission denied bind"))
	})

	result, err := FreshInstallPreflight(context.Background(), kc, dc, adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || !listContainsSubstring(result.Blocking, "create ClusterRoleBinding") {
		t.Fatalf("expected authoritative bind admission blocker, got %+v", result)
	}
}

func TestFreshInstallPreflight_MissingNamespaceDefersOnlyNamespacedAdmission(t *testing.T) {
	var reviews []authv1.ResourceAttributes
	kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
		reviews = append(reviews, attrs)
		return true
	})
	dc := adoptionDynamicClient()

	result, err := FreshInstallPreflight(context.Background(), kc, dc, adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() {
		t.Fatalf("expected allowed deferred checks, got %+v", result)
	}
	if !listContainsSubstring(result.Advisory, "does not exist") || !listContainsSubstring(result.Advisory, "admission") {
		t.Fatalf("missing honest namespace limitation advisory: %+v", result.Advisory)
	}

	var namespaceDryRun bool
	for _, action := range kc.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "namespaces" {
			create := action.(k8stesting.CreateActionImpl)
			assertDryRunAll(t, create.GetCreateOptions().DryRun, "create", "namespaces")
			namespaceDryRun = true
		}
		if action.GetVerb() == "create" && action.GetResource().Resource == "secrets" {
			t.Fatal("must not attempt a Secret dry-run create in a namespace that does not exist")
		}
	}
	if !namespaceDryRun {
		t.Fatal("missing exact dry-run Namespace create")
	}

	for _, action := range dc.Actions() {
		if action.GetVerb() != "create" {
			continue
		}
		assertDryRunAll(t, action.(k8stesting.CreateActionImpl).GetCreateOptions().DryRun,
			"create", action.GetResource().Resource)
		if action.GetResource().Resource == "configmaps" {
			t.Fatal("namespaced chart resources must use exact SSARs until the namespace exists")
		}
	}
	assertReviewed(t, reviews, "create", "configmaps", "")
	assertReviewed(t, reviews, "create", "secrets", "")
	assertReviewed(t, reviews, "delete", "secrets", CloudTokenSecretName)
	assertReviewed(t, reviews, "update", "secrets", helmReleaseSecretName("radar", 1))
	for _, attrs := range reviews {
		if attrs.Verb == "create" && attrs.Name != "" {
			t.Errorf("CREATE SSAR must model the collection request without resourceName, got %+v", attrs)
		}
	}
}

func TestFreshInstallPreflight_MissingNamespaceExactPermissionDenialBlocks(t *testing.T) {
	kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
		return !(attrs.Verb == "create" && attrs.Resource == "configmaps" && attrs.Name == "")
	})

	result, err := FreshInstallPreflight(context.Background(), kc, adoptionDynamicClient(), adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || !listContainsSubstring(result.Blocking, `create ConfigMap "radar-config"`) {
		t.Fatalf("expected exact ConfigMap create denial, got %+v", result)
	}
}

func TestFreshInstallPreflight_GeneratedRoleBindingUsesProvenRoleCreate(t *testing.T) {
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: radar-generated}
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: radar-generated}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: radar-generated
subjects:
- kind: ServiceAccount
  name: radar
  namespace: radar`
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true },
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}})
	dc := adoptionDynamicClient()
	dc.PrependReactor("create", "clusterrolebindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(
			schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
			"radar-generated")
	})

	result, err := FreshInstallPreflight(context.Background(), kc, dc, adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: manifest,
	})
	if err != nil || !result.OK() {
		t.Fatalf("a binding to a successfully dry-run generated role should pass, result=%+v err=%v", result, err)
	}
	if !listContainsSubstring(result.Advisory, "exists only for the duration of its successful dry-run") {
		t.Fatalf("missing generated-role dry-run limitation note: %+v", result.Advisory)
	}
}

func TestFreshInstallPreflight_RateLimitIsInfrastructureError(t *testing.T) {
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true },
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}})
	dc := adoptionDynamicClient()
	dc.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewTooManyRequests("busy", 1)
	})

	result, err := FreshInstallPreflight(context.Background(), kc, dc, adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err == nil || !apierrors.IsTooManyRequests(err) || len(result.Blocking) != 0 {
		t.Fatalf("429 must remain retryable infrastructure failure, result=%+v err=%v", result, err)
	}
}

func TestFreshInstallPreflight_NamespaceAdmissionDenialIsBlocking(t *testing.T) {
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true })
	kc.PrependReactor("create", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "namespaces"}, "radar", errors.New("denied"))
	})

	result, err := FreshInstallPreflight(context.Background(), kc, adoptionDynamicClient(), adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || !listContainsSubstring(result.Blocking, "create Namespace") {
		t.Fatalf("expected Namespace admission denial blocker, got %+v", result)
	}
}

func assertFreshSecretDryRunCreates(t *testing.T, actions []k8stesting.Action, names ...string) {
	t.Helper()
	created := make(map[string]bool)
	for _, action := range actions {
		if action.GetVerb() != "create" || action.GetResource().Resource != "secrets" {
			continue
		}
		create := action.(k8stesting.CreateActionImpl)
		assertDryRunAll(t, create.GetCreateOptions().DryRun, "create", "secrets")
		created[create.GetObject().(*corev1.Secret).Name] = true
	}
	for _, name := range names {
		if !created[name] {
			t.Errorf("missing dry-run create for Secret %q; got %v", name, created)
		}
	}
}

func TestFreshInstallPreflight_HiddenChartSecretFailsClosed(t *testing.T) {
	result, err := FreshInstallPreflight(context.Background(),
		adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }),
		adoptionDynamicClient(), adoptionDiscovery(), FreshInstallPreflightOptions{
			Namespace: "radar", ReleaseName: "radar",
			TargetManifest: "---\n# HIDDEN: The Secret output has been suppressed\n",
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || !listContainsSubstring(result.Blocking, "exact install mutations") {
		t.Fatalf("expected hidden Secret blocker, got %+v", result)
	}
}

func TestFreshInstallPreflight_TransientNamespaceFailureIsInfrastructureError(t *testing.T) {
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true })
	want := errors.New("connection reset")
	kc.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, want
	})
	result, err := FreshInstallPreflight(context.Background(), kc, adoptionDynamicClient(), adoptionDiscovery(), FreshInstallPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", TargetManifest: freshPreflightManifest,
	})
	if !errors.Is(err, want) || len(result.Blocking) != 0 || len(result.Advisory) != 0 {
		t.Fatalf("expected infrastructure error, got result=%+v err=%v", result, err)
	}
}

func TestFreshInstallPreflight_ValidatesRequiredInputs(t *testing.T) {
	_, err := FreshInstallPreflight(context.Background(), nil, nil, nil, FreshInstallPreflightOptions{})
	if err == nil || !strings.Contains(err.Error(), "nil kubernetes") {
		t.Fatalf("expected nil client validation, got %v", err)
	}
}
