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
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAdoptionPreflight_DryRunsExactMutationSetAndStorage(t *testing.T) {
	currentManifest := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: radar-config
data:
  mode: oss
---
apiVersion: v1
kind: Service
metadata:
  name: radar-old
spec:
  ports:
  - port: 80
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]
`
	targetManifest := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: radar-config
data:
  mode: cloud
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: radar-upgrader
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: radar
rules:
- apiGroups: [""]
  resources: ["pods", "secrets"]
  verbs: ["get"]
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

	dc := adoptionDynamicClient(
		adoptionObject("v1", "ConfigMap", "radar", "radar-config", map[string]any{"data": map[string]any{"mode": "oss"}}),
		adoptionObject("v1", "Service", "radar", "radar-old", map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80)}}}}),
		adoptionObject("rbac.authorization.k8s.io/v1", "ClusterRole", "", "radar", map[string]any{
			"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}}},
		}),
	)
	var dynamicMutations []string
	dc.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch action.GetVerb() {
		case "create":
			opts := action.(k8stesting.CreateActionImpl).GetCreateOptions()
			assertDryRunAll(t, opts.DryRun, action.GetVerb(), action.GetResource().Resource)
			if action.GetResource().Resource == "clusterrolebindings" {
				binding := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
				roleName, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
				if roleName != "admin" {
					t.Errorf("dry-run binding roleRef.name=%q, want exact target admin", roleName)
				}
			}
			dynamicMutations = append(dynamicMutations, "create "+action.GetResource().Resource)
			return true, action.(k8stesting.CreateAction).GetObject(), nil
		case "patch":
			opts := action.(k8stesting.PatchActionImpl).GetPatchOptions()
			assertDryRunAll(t, opts.DryRun, action.GetVerb(), action.GetResource().Resource)
			if action.GetResource().Resource == "clusterroles" && !strings.Contains(string(action.(k8stesting.PatchAction).GetPatch()), "secrets") {
				t.Errorf("ClusterRole dry-run patch did not contain exact target rules: %s", action.(k8stesting.PatchAction).GetPatch())
			}
			if action.GetResource().Resource == "configmaps" && !strings.Contains(string(action.(k8stesting.PatchAction).GetPatch()), "meta.helm.sh/release-name") {
				t.Errorf("ConfigMap dry-run patch did not contain Helm tracking metadata: %s", action.(k8stesting.PatchAction).GetPatch())
			}
			dynamicMutations = append(dynamicMutations, "patch "+action.GetResource().Resource)
			return true, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": action.(k8stesting.PatchAction).GetName(), "namespace": action.GetNamespace()},
			}}, nil
		case "delete":
			opts := action.(k8stesting.DeleteAction).GetDeleteOptions()
			assertDryRunAll(t, opts.DryRun, action.GetVerb(), action.GetResource().Resource)
			dynamicMutations = append(dynamicMutations, "delete "+action.GetResource().Resource)
			return true, nil, nil
		default:
			return false, nil, nil
		}
	})

	var reviews []authv1.ResourceAttributes
	kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
		reviews = append(reviews, attrs)
		return true
	}, currentHelmSecret("radar", "radar", 3))

	result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", CurrentRevision: 3,
		CurrentManifest: currentManifest, TargetManifest: targetManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || len(result.Advisory) != 0 {
		t.Fatalf("expected clean preflight, got %+v", result)
	}
	for _, want := range []string{
		"patch configmaps", "create serviceaccounts", "delete services",
		"patch clusterroles", "create clusterrolebindings",
	} {
		if !containsString(dynamicMutations, want) {
			t.Errorf("missing dynamic mutation %q in %v", want, dynamicMutations)
		}
	}
	assertSecretDryRuns(t, kc.Actions())
	assertReviewed(t, reviews, "delete", "secrets", CloudTokenSecretName)
	assertReviewed(t, reviews, "update", "secrets", helmReleaseSecretName("radar", 4))
	assertReviewed(t, reviews, "update", "secrets", helmReleaseSecretName("radar", 5))
	assertReviewed(t, reviews, "delete", "secrets", "")
	assertReviewed(t, reviews, "list", "pods", "")
}

func TestAdoptionPreflight_RBACAdmissionDenialsAreBlocking(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		target      string
		live        []*unstructured.Unstructured
		verb        string
		resource    string
		wantMessage string
	}{
		{
			name: "escalate role patch",
			current: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: radar}
rules: []`,
			target: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: radar}
rules:
- apiGroups: [""]
  resources: ["users"]
  verbs: ["impersonate"]`,
			live: []*unstructured.Unstructured{
				adoptionObject("rbac.authorization.k8s.io/v1", "ClusterRole", "", "radar", map[string]any{"rules": []any{}}),
			},
			verb: "patch", resource: "clusterroles", wantMessage: "patch ClusterRole",
		},
		{
			name: "bind cluster role create",
			current: `apiVersion: v1
kind: ConfigMap
metadata: {name: radar-config}`,
			target: `apiVersion: v1
kind: ConfigMap
metadata: {name: radar-config}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: radar-cloud-owner}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: admin
subjects:
- kind: Group
  name: cloud:owner
  apiGroup: rbac.authorization.k8s.io`,
			live: []*unstructured.Unstructured{
				adoptionObject("v1", "ConfigMap", "radar", "radar-config", nil),
			},
			verb: "create", resource: "clusterrolebindings", wantMessage: "create ClusterRoleBinding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := adoptionDynamicClient(tt.live...)
			dc.PrependReactor(tt.verb, tt.resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(
					schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource},
					"radar", errors.New("RBAC admission denied bind/escalate"))
			})
			kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }, currentHelmSecret("radar", "radar", 2))
			result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
				Namespace: "radar", ReleaseName: "radar", CurrentRevision: 2,
				CurrentManifest: tt.current, TargetManifest: tt.target,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.OK() || !listContainsSubstring(result.Blocking, tt.wantMessage) {
				t.Fatalf("expected actionable %q blocker, got %+v", tt.wantMessage, result)
			}
		})
	}
}

func TestAdoptionPreflight_RequiresAtomicRollbackInversePermissions(t *testing.T) {
	base := `apiVersion: v1
kind: ConfigMap
metadata: {name: radar-config}
`
	t.Run("delete newly created resource", func(t *testing.T) {
		target := base + `---
apiVersion: v1
kind: ServiceAccount
metadata: {name: radar-new}
`
		kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
			return !(attrs.Verb == "delete" && attrs.Resource == "serviceaccounts" && attrs.Name == "radar-new")
		}, currentHelmSecret("radar", "radar", 3))
		result, err := AdoptionPreflight(context.Background(), kc, adoptionDynamicClient(
			adoptionObject("v1", "ConfigMap", "radar", "radar-config", nil),
		), adoptionDiscovery(), AdoptionPreflightOptions{
			Namespace: "radar", ReleaseName: "radar", CurrentRevision: 3,
			CurrentManifest: base, TargetManifest: target,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.OK() || !listContainsSubstring(result.Blocking, "during Helm's atomic rollback") ||
			!listContainsSubstring(result.Blocking, "delete ServiceAccount") {
			t.Fatalf("missing rollback delete blocker: %+v", result)
		}
	})

	t.Run("recreate removed resource", func(t *testing.T) {
		current := base + `---
apiVersion: v1
kind: Service
metadata: {name: radar-old}
spec:
  ports: [{port: 80}]
`
		kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
			return !(attrs.Verb == "create" && attrs.Resource == "services" && attrs.Name == "")
		}, currentHelmSecret("radar", "radar", 3))
		result, err := AdoptionPreflight(context.Background(), kc, adoptionDynamicClient(
			adoptionObject("v1", "ConfigMap", "radar", "radar-config", nil),
			adoptionObject("v1", "Service", "radar", "radar-old", map[string]any{
				"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80)}}},
			}),
		), adoptionDiscovery(), AdoptionPreflightOptions{
			Namespace: "radar", ReleaseName: "radar", CurrentRevision: 3,
			CurrentManifest: current, TargetManifest: base,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.OK() || !listContainsSubstring(result.Blocking, "during Helm's atomic rollback") ||
			!listContainsSubstring(result.Blocking, "recreate Service") {
			t.Fatalf("missing rollback create blocker: %+v", result)
		}
	})

	t.Run("recreate resource already absent from live cluster", func(t *testing.T) {
		current := base + `---
apiVersion: v1
kind: Service
metadata: {name: radar-drifted-away}
spec:
  ports: [{port: 80}]
`
		kc := adoptionKubeClient(t, func(attrs authv1.ResourceAttributes) bool {
			return !(attrs.Verb == "create" && attrs.Resource == "services" && attrs.Name == "")
		}, currentHelmSecret("radar", "radar", 3))
		result, err := AdoptionPreflight(context.Background(), kc, adoptionDynamicClient(
			adoptionObject("v1", "ConfigMap", "radar", "radar-config", nil),
		), adoptionDiscovery(), AdoptionPreflightOptions{
			Namespace: "radar", ReleaseName: "radar", CurrentRevision: 3,
			CurrentManifest: current, TargetManifest: base,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.OK() || !listContainsSubstring(result.Blocking, "during Helm's atomic rollback") ||
			!listContainsSubstring(result.Blocking, "recreate Service") {
			t.Fatalf("missing rollback create blocker for drift-absent resource: %+v", result)
		}
	})
}

func TestAdoptionPreflight_GeneratedRoleBindingUsesProvenRoleCreate(t *testing.T) {
	current := `apiVersion: v1
kind: ConfigMap
metadata: {name: radar-config}`
	target := current + `
---
apiVersion: rbac.authorization.k8s.io/v1
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
	liveConfig := adoptionObject("v1", "ConfigMap", "radar", "radar-config", nil)
	liveConfig.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
	liveConfig.SetAnnotations(map[string]string{
		"meta.helm.sh/release-name": "radar", "meta.helm.sh/release-namespace": "radar",
	})
	dc := adoptionDynamicClient(liveConfig)
	dc.PrependReactor("create", "clusterrolebindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(
			schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
			"radar-generated")
	})
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }, currentHelmSecret("radar", "radar", 2))

	result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", CurrentRevision: 2,
		CurrentManifest: current, TargetManifest: target,
	})
	if err != nil || !result.OK() {
		t.Fatalf("a binding to a successfully dry-run generated role should pass, result=%+v err=%v", result, err)
	}
	if !listContainsSubstring(result.Advisory, "exists only for the duration of its successful dry-run") {
		t.Fatalf("missing generated-role dry-run limitation note: %+v", result.Advisory)
	}
}

func TestAdoptionPreflight_TransientMutationFailureIsInfrastructureError(t *testing.T) {
	manifest := `apiVersion: v1
kind: ConfigMap
metadata: {name: radar-config}
data: {mode: oss}`
	target := strings.Replace(manifest, "mode: oss", "mode: cloud", 1)
	dc := adoptionDynamicClient(adoptionObject("v1", "ConfigMap", "radar", "radar-config", map[string]any{
		"data": map[string]any{"mode": "oss"},
	}))
	want := errors.New("connection reset")
	dc.PrependReactor("patch", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, want
	})
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }, currentHelmSecret("radar", "radar", 1))

	result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", CurrentRevision: 1,
		CurrentManifest: manifest, TargetManifest: target,
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected transient error to remain infrastructure error, got result=%+v err=%v", result, err)
	}
}

func TestAdoptionPreflight_SecretAndWaitPermissionsBlockBeforeEnrollment(t *testing.T) {
	manifest := `apiVersion: v1
kind: ConfigMap
metadata: {name: radar-config}`
	tests := []struct {
		name        string
		configure   func(*kubernetesfake.Clientset)
		allow       func(authv1.ResourceAttributes) bool
		wantMessage string
	}{
		{
			name: "token secret create admission",
			configure: func(kc *kubernetesfake.Clientset) {
				kc.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
					secret := action.(k8stesting.CreateAction).GetObject().(*corev1.Secret)
					if secret.Name != CloudTokenSecretName {
						return false, nil, nil
					}
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, secret.Name, errors.New("denied"))
				})
			},
			allow:       func(authv1.ResourceAttributes) bool { return true },
			wantMessage: "create Cloud token Secret",
		},
		{
			name:      "atomic wait pod list",
			configure: func(*kubernetesfake.Clientset) {},
			allow: func(attrs authv1.ResourceAttributes) bool {
				return !(attrs.Verb == "list" && attrs.Resource == "pods")
			},
			wantMessage: "list Pods",
		},
		{
			name:      "atomic Helm secret delete",
			configure: func(*kubernetesfake.Clientset) {},
			allow: func(attrs authv1.ResourceAttributes) bool {
				return !(attrs.Verb == "delete" && attrs.Resource == "secrets" && attrs.Name == "")
			},
			wantMessage: "delete Helm release Secrets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := adoptionDynamicClient(adoptionObject("v1", "ConfigMap", "radar", "radar-config", nil))
			kc := adoptionKubeClient(t, tt.allow, currentHelmSecret("radar", "radar", 1))
			tt.configure(kc)
			result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
				Namespace: "radar", ReleaseName: "radar", CurrentRevision: 1,
				CurrentManifest: manifest, TargetManifest: manifest,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.OK() || !listContainsSubstring(result.Blocking, tt.wantMessage) {
				t.Fatalf("expected %q blocker, got %+v", tt.wantMessage, result)
			}
		})
	}
}

func TestAdoptionPreflight_ExistingUnownedTargetIsBlocking(t *testing.T) {
	current := `apiVersion: v1
kind: Service
metadata: {name: radar}`
	target := current + `
---
apiVersion: v1
kind: ConfigMap
metadata: {name: collision}`
	dc := adoptionDynamicClient(
		adoptionObject("v1", "Service", "radar", "radar", nil),
		adoptionObject("v1", "ConfigMap", "radar", "collision", nil),
	)
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }, currentHelmSecret("radar", "radar", 1))

	result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", CurrentRevision: 1,
		CurrentManifest: current, TargetManifest: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || !listContainsSubstring(result.Blocking, "not owned by the current Helm release") {
		t.Fatalf("expected collision blocker, got %+v", result)
	}
}

func TestAdoptionPreflight_LiveKeepPolicySkipsDelete(t *testing.T) {
	current := `apiVersion: v1
kind: ConfigMap
metadata: {name: retained}`
	target := `apiVersion: v1
kind: ServiceAccount
metadata: {name: radar}`
	retained := adoptionObject("v1", "ConfigMap", "radar", "retained", nil)
	retained.SetAnnotations(map[string]string{"helm.sh/resource-policy": "keep"})
	dc := adoptionDynamicClient(retained)
	kc := adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }, currentHelmSecret("radar", "radar", 1))

	result, err := AdoptionPreflight(context.Background(), kc, dc, adoptionDiscovery(), AdoptionPreflightOptions{
		Namespace: "radar", ReleaseName: "radar", CurrentRevision: 1,
		CurrentManifest: current, TargetManifest: target,
	})
	if err != nil || !result.OK() {
		t.Fatalf("expected live keep policy to pass, result=%+v err=%v", result, err)
	}
	for _, action := range dc.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "configmaps" {
			t.Fatal("live helm.sh/resource-policy=keep must suppress delete preflight")
		}
	}
}

func TestAdoptionPreflight_HiddenChartSecretFailsClosed(t *testing.T) {
	result, err := AdoptionPreflight(context.Background(),
		adoptionKubeClient(t, func(authv1.ResourceAttributes) bool { return true }),
		adoptionDynamicClient(), adoptionDiscovery(), AdoptionPreflightOptions{
			Namespace: "radar", ReleaseName: "radar", CurrentRevision: 1,
			CurrentManifest: "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: radar}",
			TargetManifest:  "---\n# HIDDEN: The Secret output has been suppressed\n",
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || !listContainsSubstring(result.Blocking, "hid at least one Secret") {
		t.Fatalf("expected hidden Secret blocker, got %+v", result)
	}
}

func adoptionKubeClient(t *testing.T, allow func(authv1.ResourceAttributes) bool, objects ...runtime.Object) *kubernetesfake.Clientset {
	t.Helper()
	kc := kubernetesfake.NewSimpleClientset(objects...)
	kc.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		out := ssar.DeepCopy()
		out.Status.Allowed = allow(*ssar.Spec.ResourceAttributes)
		if !out.Status.Allowed {
			out.Status.Reason = "test policy denied this action"
		}
		return true, out, nil
	})
	return kc
}

func adoptionDynamicClient(objects ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	runtimeObjects := make([]runtime.Object, len(objects))
	for i, object := range objects {
		runtimeObjects[i] = object
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), runtimeObjects...)
	// The dynamic fake's object tracker cannot apply a native strategic-merge
	// patch to Unstructured, while a real apiserver can. Accept otherwise
	// unhandled patch dry-runs here; test-specific reactors are prepended later
	// and therefore still observe or reject the exact patch first.
	client.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patch := action.(k8stesting.PatchAction)
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": action.GetResource().Version,
			"kind":       "PatchedObject",
			"metadata": map[string]any{
				"name":      patch.GetName(),
				"namespace": action.GetNamespace(),
			},
		}}, nil
	})
	return client
}

func adoptionDiscovery() *fakediscovery.FakeDiscovery {
	d := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	d.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
				{Name: "pods", Kind: "Pod", Namespaced: true},
				{Name: "secrets", Kind: "Secret", Namespaced: true},
				{Name: "serviceaccounts", Kind: "ServiceAccount", Namespaced: true},
				{Name: "services", Kind: "Service", Namespaced: true},
			},
		},
		{
			GroupVersion: "rbac.authorization.k8s.io/v1",
			APIResources: []metav1.APIResource{
				{Name: "clusterroles", Kind: "ClusterRole"},
				{Name: "clusterrolebindings", Kind: "ClusterRoleBinding"},
				{Name: "roles", Kind: "Role", Namespaced: true},
				{Name: "rolebindings", Kind: "RoleBinding", Namespaced: true},
			},
		},
	}
	return d
}

func adoptionObject(apiVersion, kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name": name,
		},
	}
	if namespace != "" {
		object["metadata"].(map[string]any)["namespace"] = namespace
	}
	for key, value := range fields {
		object[key] = value
	}
	return &unstructured.Unstructured{Object: object}
}

func currentHelmSecret(namespace, releaseName string, revision int) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: helmReleaseSecretName(releaseName, revision), Namespace: namespace,
			ResourceVersion: "1",
		},
		Type: corev1.SecretType("helm.sh/release.v1"),
		Data: map[string][]byte{"release": []byte("existing")},
	}
}

func assertDryRunAll(t *testing.T, dryRun []string, verb, resource string) {
	t.Helper()
	if len(dryRun) != 1 || dryRun[0] != metav1.DryRunAll {
		t.Errorf("%s %s DryRun=%v, want [%s]", verb, resource, dryRun, metav1.DryRunAll)
	}
}

func assertSecretDryRuns(t *testing.T, actions []k8stesting.Action) {
	t.Helper()
	created, updated := map[string]bool{}, map[string]bool{}
	for _, action := range actions {
		if action.GetResource().Resource != "secrets" {
			continue
		}
		switch action.GetVerb() {
		case "create":
			create := action.(k8stesting.CreateActionImpl)
			assertDryRunAll(t, create.GetCreateOptions().DryRun, "create", "secrets")
			created[create.GetObject().(*corev1.Secret).Name] = true
		case "update":
			update := action.(k8stesting.UpdateActionImpl)
			assertDryRunAll(t, update.GetUpdateOptions().DryRun, "update", "secrets")
			updated[update.GetObject().(*corev1.Secret).Name] = true
		}
	}
	for _, name := range []string{CloudTokenSecretName, helmReleaseSecretName("radar", 4), helmReleaseSecretName("radar", 5)} {
		if !created[name] {
			t.Errorf("missing dry-run create for Secret %q; got %v", name, created)
		}
	}
	if !updated[helmReleaseSecretName("radar", 3)] {
		t.Errorf("missing dry-run update for current Helm Secret; got %v", updated)
	}
}

func assertReviewed(t *testing.T, reviews []authv1.ResourceAttributes, verb, resource, name string) {
	t.Helper()
	for _, attrs := range reviews {
		if attrs.Verb == verb && attrs.Resource == resource && attrs.Name == name {
			return
		}
	}
	t.Errorf("missing SSAR %s %s name=%q in %+v", verb, resource, name, reviews)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func listContainsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
