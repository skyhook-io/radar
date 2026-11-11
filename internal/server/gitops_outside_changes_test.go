package server

import (
	"reflect"
	"testing"
	"time"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"github.com/skyhook-io/radar/pkg/topology"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var helmOwner = &topology.ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "prod", Name: "api"}

func fluxHelmOwner(drift map[string]any) *unstructured.Unstructured {
	spec := map[string]any{"interval": "10m"}
	if drift != nil {
		spec["driftDetection"] = drift
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "prod", "name": "api"}, "spec": spec,
	}}
}

func withTime(e metav1.ManagedFieldsEntry, at string) metav1.ManagedFieldsEntry {
	parsed, _ := time.Parse(time.RFC3339, at)
	e.Time = &metav1.Time{Time: parsed}
	return e
}

func TestClassifyOutsideObjectSortsWriters(t *testing.T) {
	scale := fieldsEntry(t, "kubectl", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}})
	scale.Subresource = "scale"
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "helm-controller", metav1.ManagedFieldsOperationApply, map[string]any{"f:spec": map[string]any{"f:template": map[string]any{}}}),
		withTime(fieldsEntry(t, "kubectl-patch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}}), "2026-11-09T10:00:00Z"),
		fieldsEntry(t, "kubectl-rollout", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:template": map[string]any{"f:metadata": map[string]any{"f:annotations": map[string]any{"f:kubectl.kubernetes.io/restartedAt": map[string]any{}}}}}}),
		fieldsEntry(t, "node-fetch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:metadata": map[string]any{"f:labels": map[string]any{"f:k8slens-edit-resource-version": map[string]any{}}}}),
		fieldsEntry(t, "kube-controller-manager", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:metadata": map[string]any{"f:annotations": map[string]any{"f:deployment.kubernetes.io/revision": map[string]any{}}}}),
		scale,
	})
	out, harmless, other := classifyOutsideObject(obj, helmOwner, fluxHelmOwner(map[string]any{"mode": "warn"}))
	want := []outsideChange{{Manager: "kubectl-patch", Tool: "kubectl patch", Operation: "Update", Time: "2026-11-09T10:00:00Z", Paths: []string{"spec.replicas"}, Values: map[string]string{"spec.replicas": "3"}, Outcome: "reported"}}
	if !reflect.DeepEqual(out.Changes, want) {
		t.Fatalf("changes = %+v", out.Changes)
	}
	if out.Ref != (outsideRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api"}) {
		t.Fatalf("ref = %+v", out.Ref)
	}
	if !reflect.DeepEqual(harmless, []string{"kubectl rollout restart", "Lens edit marker"}) || !reflect.DeepEqual(other, []string{"kube-controller-manager"}) {
		t.Fatalf("harmless = %q, other = %q", harmless, other)
	}
}

func TestClassifyOutsideObjectCapsPaths(t *testing.T) {
	labels := map[string]any{}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n"} {
		labels["f:"+k] = map[string]any{}
	}
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{fieldsEntry(t, "kubectl-label", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:metadata": map[string]any{"f:labels": labels}})})
	out, _, _ := classifyOutsideObject(obj, helmOwner, fluxHelmOwner(nil))
	if len(out.Changes) != 1 || len(out.Changes[0].Paths) != 12 || out.Changes[0].MorePaths != 2 || out.Changes[0].Outcome != "stays" {
		t.Fatalf("changes = %+v", out.Changes)
	}
}

func TestClassifyOutsideObjectIgnoredByDriftRule(t *testing.T) {
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{fieldsEntry(t, "kubectl-scale", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}})})
	owner := fluxHelmOwner(map[string]any{"mode": "enabled", "ignore": []any{map[string]any{"paths": []any{"/spec/replicas"}}}})
	out, _, _ := classifyOutsideObject(obj, helmOwner, owner)
	if len(out.Changes) != 1 || out.Changes[0].Outcome != "ignored" {
		t.Fatalf("changes = %+v", out.Changes)
	}
}

func TestOutsideValueAllowlist(t *testing.T) {
	obj := map[string]any{"spec": map[string]any{"replicas": int64(3), "template": map[string]any{"spec": map[string]any{"containers": []any{
		map[string]any{"name": "engine", "image": "engine:2", "env": []any{map[string]any{"name": "TOKEN", "value": "secret"}},
			"resources": map[string]any{"limits": map[string]any{"memory": "1Gi"}}},
	}}}}, "metadata": map[string]any{"annotations": map[string]any{"x": "y"}}}
	for path, want := range map[string]string{
		"spec.replicas": "3",
		"spec.template.spec.containers[name=engine].image":                   "engine:2",
		"spec.template.spec.containers[name=engine].resources.limits.memory": "1Gi",
	} {
		if got, ok := outsideValue(obj, path); !ok || got != want {
			t.Errorf("outsideValue(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	for _, path := range []string{"spec.template.spec.containers[name=engine].env[name=TOKEN].value", `metadata.annotations["x"]`, "spec.template.spec.containers[name=engine]"} {
		if got, ok := outsideValue(obj, path); ok {
			t.Errorf("outsideValue(%q) leaked %q", path, got)
		}
	}
}

func TestOutsideOutcome(t *testing.T) {
	yes := true
	no := false
	for _, tc := range []struct {
		policy *gitOpsWritePolicy
		ignore string
		want   string
	}{
		{nil, "", "unknown"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &yes, DriftDetection: "enabled"}, "", "suspended"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &no, DriftDetection: "enabled", ObjectReconcile: "ignore"}, "", "ignored"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &no, DriftDetection: "enabled"}, "effective", "ignored"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &no}, "", "may-overwrite"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &no, DriftDetection: "enabled"}, "", "reverted"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &no, DriftDetection: "warn"}, "", "reported"},
		{&gitOpsWritePolicy{Tool: "fluxcd", Suspended: &no, DriftDetection: "disabled"}, "", "stays"},
	} {
		if got := outsideOutcome(tc.policy, tc.ignore); got != tc.want {
			t.Errorf("outsideOutcome(%+v, %q) = %q, want %q", tc.policy, tc.ignore, got, tc.want)
		}
	}
}

func treeNode(group, kind, ns, name string) gitopstree.Node {
	return gitopstree.Node{Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Group: group, Kind: kind, Namespace: ns, Name: name}}
}

func fakeResolve(group, kind string) (schema.GroupVersionResource, bool) {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets"}[kind]}, true
	case "Service", "ConfigMap", "ServiceAccount", "PersistentVolumeClaim", "Secret":
		return schema.GroupVersionResource{Version: "v1", Resource: map[string]string{"Service": "services", "ConfigMap": "configmaps", "ServiceAccount": "serviceaccounts", "PersistentVolumeClaim": "persistentvolumeclaims", "Secret": "secrets"}[kind]}, true
	case "Ingress":
		return schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, true
	case "CronJob", "Job":
		return schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: map[string]string{"CronJob": "cronjobs", "Job": "jobs"}[kind]}, true
	case "Role", "RoleBinding":
		return schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: map[string]string{"Role": "roles", "RoleBinding": "rolebindings"}[kind]}, true
	}
	if len(kind) > 4 && kind[:4] == "Kind" {
		return schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: kind}, true
	}
	return schema.GroupVersionResource{}, false
}

func kustomizationRoot() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "flux-system", "name": "apps"},
	}}
}

func TestOutsideListGroupsFromTree(t *testing.T) {
	source := treeNode("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "repo")
	source.Dependency = true
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{
		{Role: gitopstree.RoleRoot, Ref: gitopstree.ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}},
		treeNode("apps", "Deployment", "team", "a"), treeNode("apps", "Deployment", "team", "b"), treeNode("apps", "Deployment", "other", "c"),
		treeNode("", "Secret", "team", "creds"), treeNode("", "Service", "team", "svc"), treeNode("unknown.io", "Widget", "team", "w"),
		source,
	}}
	groups, cov := outsideListGroups(tree, kustomizationRoot(), fakeResolve)
	var got []string
	for _, g := range groups {
		got = append(got, g.GVR.Resource+"/"+g.Namespace)
	}
	if want := []string{"deployments/other", "deployments/team", "services/team"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(cov.Skipped, []string{"secrets"}) || !reflect.DeepEqual(cov.NotChecked, []string{"Widget"}) || cov.LabelScan || cov.Truncated {
		t.Fatalf("coverage = %+v", cov)
	}
}

func TestOutsideListGroupsScansHelmReleaseWithoutInventory(t *testing.T) {
	root := fluxHelmOwner(nil)
	root.Object["spec"].(map[string]any)["targetNamespace"] = "apps"
	groups, cov := outsideListGroups(&gitopstree.ResourceTree{}, root, fakeResolve)
	if !cov.LabelScan || len(groups) != 12 {
		t.Fatalf("groups = %d, coverage = %+v", len(groups), cov)
	}
	for _, g := range groups {
		if g.Namespace != "apps" || g.GVR.Resource == "secrets" {
			t.Fatalf("group = %+v", g)
		}
	}
}

func TestOutsideListGroupsTruncates(t *testing.T) {
	tree := &gitopstree.ResourceTree{}
	for i := 0; i < 45; i++ {
		tree.Nodes = append(tree.Nodes, treeNode("example.io", "Kind"+string(rune('A'+i/26))+string(rune('a'+i%26)), "team", "x"))
	}
	groups, cov := outsideListGroups(tree, kustomizationRoot(), fakeResolve)
	if len(groups) != 40 || !cov.Truncated {
		t.Fatalf("groups = %d, coverage = %+v", len(groups), cov)
	}
}

func TestFluxMemberSelector(t *testing.T) {
	if got := fluxMemberSelector(fluxHelmOwner(nil)); got != "helm.toolkit.fluxcd.io/name=api,helm.toolkit.fluxcd.io/namespace=prod" {
		t.Fatalf("helm selector = %q", got)
	}
	if got := fluxMemberSelector(kustomizationRoot()); got != "kustomize.toolkit.fluxcd.io/name=apps,kustomize.toolkit.fluxcd.io/namespace=flux-system" {
		t.Fatalf("kustomize selector = %q", got)
	}
}

func TestClassifyOutsideObjectSkipsOwnedEmptyContainers(t *testing.T) {
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "node-fetch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:metadata": map[string]any{"f:annotations": map[string]any{}}}),
		fieldsEntry(t, "kubectl-patch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{`k:{"name":"app"}`: map[string]any{".": map[string]any{}}}}}}}),
	})
	out, _, _ := classifyOutsideObject(obj, helmOwner, fluxHelmOwner(nil))
	if len(out.Changes) != 1 || out.Changes[0].Tool != "kubectl patch" || out.Changes[0].Paths[0] != "spec.template.spec.containers[name=app]" {
		t.Fatalf("changes = %+v", out.Changes)
	}
}
