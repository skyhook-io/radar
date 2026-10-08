package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestGitOpsWriteEvidenceDiscoveryUnavailable(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	k8s.ResetResourceDiscovery()
	r := httptest.NewRequest(http.MethodPost, "/api/gitops/write-evidence", strings.NewReader(`{"kind":"Deployment","group":"apps","namespace":"prod","name":"api","paths":["spec.replicas"]}`))
	w := httptest.NewRecorder()
	(&Server{}).handleGitOpsWriteEvidence(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "{\"error\":\"resource discovery not available\"}\n" {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

// The body is bounded before it is read and validated before any cluster read.
func TestGitOpsWriteEvidenceRejectsBadRequests(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	manyPaths := make([]string, maxWriteEvidencePaths+1)
	for i := range manyPaths {
		manyPaths[i] = "spec.replicas"
	}
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"too large", `{"kind":"Deployment","name":"api","paths":["` + strings.Repeat("a", maxWriteEvidenceRequestBytes) + `"]}`, http.StatusRequestEntityTooLarge},
		{"unknown field", `{"kind":"Deployment","name":"api","paths":[],"value":"x"}`, http.StatusBadRequest},
		{"missing kind", `{"name":"api","paths":["spec.replicas"]}`, http.StatusBadRequest},
		{"too many paths", mustJSON(t, map[string]any{"kind": "Deployment", "group": "apps", "namespace": "prod", "name": "api", "paths": manyPaths}), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/gitops/write-evidence", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			(&Server{}).handleGitOpsWriteEvidence(w, r)
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.code, w.Body.String())
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func evidenceTarget(t *testing.T, lastApplied map[string]any, managed []metav1.ManagedFieldsEntry) *unstructured.Unstructured {
	t.Helper()
	annotations := map[string]any{"cnpg.io/hibernation": "off", "team": "db"}
	if lastApplied != nil {
		annotations[lastAppliedAnnotationKey] = mustJSON(t, lastApplied)
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":            "api",
			"namespace":       "prod",
			"uid":             "uid-1",
			"resourceVersion": "42",
			"annotations":     annotations,
			"labels":          map[string]any{"app": "api"},
		},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{"spec": map[string]any{
				"containers": []any{
					map[string]any{"name": "sidecar", "image": "envoy:1"},
					map[string]any{"name": "app", "image": "api:1"},
				},
			}},
		},
	}}
	u.SetManagedFields(managed)
	return u
}

func fieldsEntry(t *testing.T, manager string, op metav1.ManagedFieldsOperationType, fields map[string]any) metav1.ManagedFieldsEntry {
	t.Helper()
	return metav1.ManagedFieldsEntry{
		Manager:   manager,
		Operation: op,
		FieldsV1:  &metav1.FieldsV1{Raw: []byte(mustJSON(t, fields))},
	}
}

var deploymentRef = writeEvidenceRef{Kind: "Deployment", Group: "apps", Namespace: "prod", Name: "api"}

func evidenceArgoApp(syncPolicy map[string]any, ignore []any) *unstructured.Unstructured {
	spec := map[string]any{}
	if syncPolicy != nil {
		spec["syncPolicy"] = syncPolicy
	}
	if ignore != nil {
		spec["ignoreDifferences"] = ignore
	}
	return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
}

var argoOwner = &topology.ResourceRef{Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "api"}

func pathEvidence(t *testing.T, resp gitOpsWriteEvidenceResponse, path string) gitOpsPathEvidence {
	t.Helper()
	for _, p := range resp.Paths {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("no evidence for %s", path)
	return gitOpsPathEvidence{}
}

func TestParseWriteFieldPath(t *testing.T) {
	segs, err := parseWriteFieldPath(`metadata.annotations["cnpg.io/hibernation"]`)
	if err != nil || len(segs) != 3 || segs[2].key != "cnpg.io/hibernation" {
		t.Fatalf("annotation path: %+v %v", segs, err)
	}
	segs, err = parseWriteFieldPath(`spec.template.spec.containers[name="app"].image`)
	if err != nil || segs[4].typ != segMatch || segs[4].key != "name" || segs[4].value != "app" {
		t.Fatalf("selector path: %+v %v", segs, err)
	}
	segs, err = parseWriteFieldPath(`spec.containers[*].image`)
	if err != nil || segs[2].typ != segAny {
		t.Fatalf("wildcard path: %+v %v", segs, err)
	}
	if _, err := parseWriteFieldPath(`spec.containers[0`); err == nil {
		t.Fatal("expected unterminated bracket error")
	}
}

func TestFieldsV1Owns(t *testing.T) {
	fields := map[string]any{
		"f:metadata": map[string]any{
			"f:annotations": map[string]any{".": map[string]any{}, "f:cnpg.io/hibernation": map[string]any{}},
			"f:labels":      map[string]any{"f:app": map[string]any{}},
		},
		"f:spec": map[string]any{
			"f:replicas": map[string]any{},
			"f:template": map[string]any{"f:spec": map[string]any{
				"f:containers": map[string]any{
					`k:{"name":"app"}`: map[string]any{".": map[string]any{}, "f:image": map[string]any{}},
				},
				"f:tolerations": map[string]any{},
			}},
		},
	}
	cases := []struct {
		path        string
		owned, appr bool
	}{
		{`metadata.annotations["cnpg.io/hibernation"]`, true, false},
		{`metadata.annotations.team`, false, false},
		{`metadata.labels.app`, true, false},
		{`spec.replicas`, true, false},
		{`spec.template.spec.containers[name=app].image`, true, false},
		{`spec.template.spec.containers[name=sidecar].image`, false, false},
		{`spec.template.spec.containers[*].image`, true, true},
		{`spec.template.spec.tolerations[0].key`, true, true},
		{`spec.paused`, false, false},
	}
	for _, tc := range cases {
		segs, err := parseWriteFieldPath(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		owned, approximate := fieldsV1Owns(fields, segs)
		if owned != tc.owned || approximate != tc.appr {
			t.Errorf("%s: owned=%v approximate=%v, want %v %v", tc.path, owned, approximate, tc.owned, tc.appr)
		}
	}
}

func TestBuildGitOpsWriteEvidence(t *testing.T) {
	lastApplied := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"team": "db"}},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": "api:1"}},
		}}},
	}
	managed := []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "argocd-controller", metav1.ManagedFieldsOperationApply, map[string]any{
			"f:spec": map[string]any{"f:replicas": map[string]any{}},
		}),
		fieldsEntry(t, "kubectl-edit", metav1.ManagedFieldsOperationUpdate, map[string]any{
			"f:metadata": map[string]any{"f:annotations": map[string]any{"f:cnpg.io/hibernation": map[string]any{}}},
		}),
	}
	target := evidenceTarget(t, lastApplied, managed)
	app := evidenceArgoApp(map[string]any{"automated": map[string]any{"selfHeal": true, "prune": true}}, nil)
	paths := []string{
		`metadata.annotations.team`,
		`metadata.annotations["cnpg.io/hibernation"]`,
		`spec.replicas`,
		`spec.template.spec.containers[name=app].image`,
		`spec[`,
	}
	resp := buildGitOpsWriteEvidence(target, deploymentRef, paths, argoOwner, app, nil)

	if resp.UID != "uid-1" || resp.ResourceVersion != "42" {
		t.Fatalf("identity: %+v", resp)
	}
	if resp.Policy == nil || !*resp.Policy.Auto || !*resp.Policy.SelfHeal || !*resp.Policy.Prune {
		t.Fatalf("policy: %+v", resp.Policy)
	}
	if got := pathEvidence(t, resp, `metadata.annotations.team`); got.LastApplied != "present" || got.OwnedByGitOps {
		t.Errorf("team: %+v", got)
	}
	hib := pathEvidence(t, resp, `metadata.annotations["cnpg.io/hibernation"]`)
	if hib.LastApplied != "absent" || hib.OwnedByGitOps || len(hib.OwnedBy) != 1 || hib.OwnedBy[0].Manager != "kubectl-edit" || hib.OwnedBy[0].Tool != "" {
		t.Errorf("hibernation: %+v", hib)
	}
	rep := pathEvidence(t, resp, `spec.replicas`)
	if rep.LastApplied != "absent" || !rep.OwnedByGitOps || rep.OwnedBy[0].Tool != "argocd" || rep.OwnedBy[0].Operation != "Apply" {
		t.Errorf("replicas: %+v", rep)
	}
	if got := pathEvidence(t, resp, `spec.template.spec.containers[name=app].image`); got.LastApplied != "present" {
		t.Errorf("image: %+v", got)
	}
	if got := pathEvidence(t, resp, `spec[`); got.Error == "" {
		t.Errorf("malformed path should carry an error: %+v", got)
	}

	// The response must never carry the raw annotation or managedFields.
	raw := mustJSON(t, resp)
	for _, leak := range []string{"last-applied", "f:spec", "fieldsV1"} {
		if strings.Contains(raw, leak) {
			t.Errorf("response leaks %q: %s", leak, raw)
		}
	}
}

func TestWriteEvidenceNoLastApplied(t *testing.T) {
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, argoOwner, evidenceArgoApp(nil, nil), nil)
	if got := resp.Paths[0]; got.LastApplied != "no-annotation" || got.OwnedByGitOps {
		t.Errorf("%+v", got)
	}
	if resp.Policy == nil || *resp.Policy.Auto || *resp.Policy.SelfHeal {
		t.Errorf("manual sync policy: %+v", resp.Policy)
	}
}

func TestWriteEvidenceArgoIgnoreDifferences(t *testing.T) {
	ignore := []any{map[string]any{"group": "apps", "kind": "Deployment", "jsonPointers": []any{"/spec/replicas", "/spec/template/spec/containers/1/image"}}}
	paths := []string{"spec.replicas", "spec.template.spec.containers[name=app].image", "spec.template.spec.containers[name=sidecar].image"}

	off := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, paths, argoOwner,
		evidenceArgoApp(map[string]any{"automated": map[string]any{"selfHeal": true}}, ignore), nil)
	if got := off.Paths[0]; got.Ignored != "comparison-only" || got.IgnoredBy != "spec.ignoreDifferences" {
		t.Errorf("without RespectIgnoreDifferences: %+v", got)
	}

	on := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, paths, argoOwner,
		evidenceArgoApp(map[string]any{
			"automated":   map[string]any{"selfHeal": true},
			"syncOptions": []any{"RespectIgnoreDifferences=true"},
		}, ignore), nil)
	if !on.Policy.RespectIgnoreDifferences {
		t.Fatalf("policy: %+v", on.Policy)
	}
	if on.Paths[0].Ignored != "effective" || on.Paths[1].Ignored != "effective" {
		t.Errorf("with RespectIgnoreDifferences: %+v", on.Paths)
	}
	if on.Paths[2].Ignored != "" {
		t.Errorf("sidecar (index 0) must not be covered: %+v", on.Paths[2])
	}

	// A rule for another kind, or one whose group is omitted (core), doesn't match apps/Deployment.
	other := []any{map[string]any{"kind": "Deployment", "jsonPointers": []any{"/spec/replicas"}}}
	miss := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, paths[:1], argoOwner, evidenceArgoApp(nil, other), nil)
	if miss.Paths[0].Ignored != "" {
		t.Errorf("core-group rule matched apps/Deployment: %+v", miss.Paths[0])
	}
}

func TestWriteEvidenceIgnoreRulesMatchAResourceNameRequest(t *testing.T) {
	ignore := []any{map[string]any{"group": "apps", "kind": "Deployment", "jsonPointers": []any{"/spec/replicas"}}}
	target := evidenceTarget(t, nil, nil)
	ref := canonicalWriteEvidenceRef(writeEvidenceRef{Kind: "deployments", Group: "apps", Namespace: "prod", Name: "api"}, target)
	if ref != deploymentRef {
		t.Fatalf("canonical ref = %+v", ref)
	}
	resp := buildGitOpsWriteEvidence(target, ref, []string{"spec.replicas"}, argoOwner,
		evidenceArgoApp(map[string]any{
			"automated":   map[string]any{"selfHeal": true},
			"syncOptions": []any{"RespectIgnoreDifferences=true"},
		}, ignore), nil)
	if resp.Paths[0].Ignored != "effective" {
		t.Errorf("rule for Deployment missed a request sent as deployments: %+v", resp.Paths[0])
	}
}

// Radar doesn't evaluate jq, so a matching jq rule leaves coverage open
// rather than reading as "not ignored".
func TestWriteEvidenceArgoJQRuleIsUnevaluated(t *testing.T) {
	ignore := []any{map[string]any{"group": "apps", "kind": "Deployment", "jqPathExpressions": []any{".spec.replicas"}}}
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, argoOwner,
		evidenceArgoApp(map[string]any{"syncOptions": []any{"RespectIgnoreDifferences=true"}}, ignore), nil)
	if got := resp.Paths[0]; got.Ignored != "unevaluated" {
		t.Errorf("jq rule: %+v", got)
	}
	both := []any{map[string]any{"group": "apps", "kind": "Deployment", "jsonPointers": []any{"/spec/replicas"}, "jqPathExpressions": []any{".spec.x"}}}
	resp = buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, argoOwner,
		evidenceArgoApp(map[string]any{"syncOptions": []any{"RespectIgnoreDifferences=true"}}, both), nil)
	if got := resp.Paths[0]; got.Ignored != "effective" {
		t.Errorf("a pointer that covers the path wins over an unevaluated jq rule: %+v", got)
	}
}

// A manager that owns one container's image doesn't prove an
// ignoreDifferences managedFieldsManagers rule covers every container's.
func TestWriteEvidenceArgoManagerRulePartialOwnershipIsUnevaluated(t *testing.T) {
	managed := []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "image-updater", metav1.ManagedFieldsOperationUpdate, map[string]any{
			"f:spec": map[string]any{"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{
				`k:{"name":"sidecar"}`: map[string]any{".": map[string]any{}, "f:image": map[string]any{}},
			}}}},
		}),
		fieldsEntry(t, "image-updater", metav1.ManagedFieldsOperationUpdate, map[string]any{
			"f:spec": map[string]any{"f:replicas": map[string]any{}},
		}),
	}
	ignore := []any{map[string]any{"group": "apps", "kind": "Deployment", "managedFieldsManagers": []any{"image-updater"}}}
	app := evidenceArgoApp(map[string]any{
		"automated":   map[string]any{"selfHeal": true},
		"syncOptions": []any{"RespectIgnoreDifferences=true"},
	}, ignore)
	paths := []string{"spec.template.spec.containers[*].image", "spec.replicas"}
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, managed), deploymentRef, paths, argoOwner, app, nil)

	wild := pathEvidence(t, resp, paths[0])
	if wild.Ignored != "unevaluated" || !strings.Contains(wild.IgnoredBy, "managedFieldsManagers") {
		t.Errorf("wildcard path owned by the manager for one container only: %+v", wild)
	}
	if got := pathEvidence(t, resp, paths[1]); got.Ignored != "effective" {
		t.Errorf("a path the manager owns exactly stays covered: %+v", got)
	}
}

func TestWriteEvidenceArgoDisabledAutomation(t *testing.T) {
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, nil, argoOwner,
		evidenceArgoApp(map[string]any{"automated": map[string]any{"enabled": false, "selfHeal": true}}, nil), nil)
	if *resp.Policy.Auto || *resp.Policy.SelfHeal {
		t.Errorf("automated.enabled=false must disable auto-sync: %+v", resp.Policy)
	}
}

func TestWriteEvidenceHelmReleaseDriftDetection(t *testing.T) {
	owner := &topology.ResourceRef{Kind: "HelmRelease", Group: "helm.toolkit.fluxcd.io", Namespace: "flux-system", Name: "api"}
	hr := func(drift map[string]any) *unstructured.Unstructured {
		spec := map[string]any{"interval": "5m"}
		if drift != nil {
			spec["driftDetection"] = drift
		}
		return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	}
	for _, tc := range []struct {
		drift    map[string]any
		mode     string
		selfHeal bool
	}{
		{nil, "disabled", false},
		{map[string]any{"mode": "warn"}, "warn", false},
		{map[string]any{"mode": "enabled"}, "enabled", true},
	} {
		resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, owner, hr(tc.drift), nil)
		if resp.Policy.DriftDetection != tc.mode || *resp.Policy.SelfHeal != tc.selfHeal || resp.Policy.Interval != "5m" {
			t.Errorf("mode %s: %+v", tc.mode, resp.Policy)
		}
	}

	ignored := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas", "spec.paused"}, owner,
		hr(map[string]any{"mode": "enabled", "ignore": []any{map[string]any{"paths": []any{"/spec/replicas"}, "target": map[string]any{"kind": "Deployment"}}}}), nil)
	if ignored.Paths[0].Ignored != "effective" || ignored.Paths[0].IgnoredBy != "spec.driftDetection.ignore" || ignored.Paths[1].Ignored != "" {
		t.Errorf("drift ignore: %+v", ignored.Paths)
	}
}

func TestWriteEvidenceFluxKustomization(t *testing.T) {
	owner := &topology.ResourceRef{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "flux-system", Name: "apps"}
	ks := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"suspend": true, "interval": "10m", "prune": true}}}
	managed := []metav1.ManagedFieldsEntry{fieldsEntry(t, "kustomize-controller", metav1.ManagedFieldsOperationApply, map[string]any{
		"f:spec": map[string]any{"f:replicas": map[string]any{}},
	})}
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, managed), deploymentRef, []string{"spec.replicas"}, owner, ks, nil)
	if !*resp.Policy.Suspended || *resp.Policy.SelfHeal || *resp.Policy.Auto || !*resp.Policy.Prune || resp.Policy.Interval != "10m" {
		t.Errorf("suspended kustomization: %+v", resp.Policy)
	}
	if !resp.Paths[0].OwnedByGitOps {
		t.Errorf("kustomize-controller ownership: %+v", resp.Paths[0])
	}

	target := evidenceTarget(t, nil, nil)
	annotations := target.GetAnnotations()
	annotations["kustomize.toolkit.fluxcd.io/ssa"] = "IfNotPresent"
	target.SetAnnotations(annotations)
	ifNotPresent := buildGitOpsWriteEvidence(target, deploymentRef, []string{"spec.replicas"}, owner,
		&unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{}}}, nil)
	if ifNotPresent.Policy.ObjectReconcile != "if-not-present" || ifNotPresent.Paths[0].Ignored != "effective" {
		t.Errorf("ssa IfNotPresent: %+v %+v", ifNotPresent.Policy, ifNotPresent.Paths[0])
	}
}

func TestWriteEvidenceOwnerUnreadable(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "api", errors.New("denied"))
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, argoOwner, nil, forbidden)
	if resp.Policy != nil || resp.PolicyError == "" {
		t.Errorf("unreadable owner must leave policy unknown with a reason: %+v", resp)
	}
}

func TestWriteEvidenceControllerOwnerAndMetadataOwner(t *testing.T) {
	target := evidenceTarget(t, nil, nil)
	controller := true
	target.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg", Controller: &controller}})
	target.SetAnnotations(map[string]string{"argocd.argoproj.io/tracking-id": "argocd_api:apps/Deployment:prod/api"})
	owner := resolveWriteEvidenceOwner(gitOpsWriteEvidenceRequest{writeEvidenceRef: deploymentRef}, target)
	if owner == nil || owner.Kind != "Application" || owner.Name != "api" || owner.Namespace != "argocd" {
		t.Fatalf("owner from tracking metadata: %+v", owner)
	}
	resp := buildGitOpsWriteEvidence(target, deploymentRef, nil, owner, nil, nil)
	if resp.ControllerOwner == nil || resp.ControllerOwner.Kind != "Cluster" || resp.ControllerOwner.Name != "pg" {
		t.Errorf("controller owner: %+v", resp.ControllerOwner)
	}
}

// Set image asks about every container at once, so the verdict is only
// certain when the rule (or the source) covers each container.
func TestWriteEvidenceWildcardPathIsJudgedPerContainer(t *testing.T) {
	const images = "spec.template.spec.containers[*].image"
	respect := map[string]any{"automated": map[string]any{"selfHeal": true}, "syncOptions": []any{"RespectIgnoreDifferences=true"}}
	rule := func(pointers ...any) []any {
		return []any{map[string]any{"group": "apps", "kind": "Deployment", "jsonPointers": pointers}}
	}
	evidence := func(target *unstructured.Unstructured, ignore []any) gitOpsPathEvidence {
		return buildGitOpsWriteEvidence(target, deploymentRef, []string{images}, argoOwner, evidenceArgoApp(respect, ignore), nil).Paths[0]
	}

	single := evidenceTarget(t, nil, nil)
	if err := unstructured.SetNestedSlice(single.Object, []any{map[string]any{"name": "app", "image": "api:1"}}, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	if got := evidence(single, rule("/spec/template/spec/containers/0/image")); got.Ignored != "effective" {
		t.Errorf("a rule on the only container covers the wildcard: %+v", got)
	}
	if got := evidence(evidenceTarget(t, nil, nil), rule("/spec/template/spec/containers/0/image", "/spec/template/spec/containers/1/image")); got.Ignored != "effective" {
		t.Errorf("rules covering every container: %+v", got)
	}
	if got := evidence(evidenceTarget(t, nil, nil), rule("/spec/template/spec/containers")); got.Ignored != "effective" {
		t.Errorf("an ancestor pointer covers every container: %+v", got)
	}
	if got := evidence(evidenceTarget(t, nil, nil), rule("/spec/template/spec/containers/1/image")); got.Ignored != "unevaluated" || got.IgnoredBy != "spec.ignoreDifferences jsonPointers" {
		t.Errorf("a rule covering one of two containers is unconfirmed: %+v", got)
	}

	declares := func(names ...string) map[string]any {
		containers := []any{}
		for _, n := range names {
			containers = append(containers, map[string]any{"name": n, "image": n + ":1"})
		}
		return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": containers}}}}
	}
	if got := evidence(evidenceTarget(t, declares("sidecar", "app"), nil), nil); got.LastApplied != "present" || got.Partial {
		t.Errorf("source declares every container: %+v", got)
	}
	if got := evidence(evidenceTarget(t, declares("app"), nil), nil); got.LastApplied != "present" || !got.Partial {
		t.Errorf("source declares one of two containers: %+v", got)
	}

	owns := func(names ...string) []metav1.ManagedFieldsEntry {
		containers := map[string]any{}
		for _, n := range names {
			containers[`k:{"name":"`+n+`"}`] = map[string]any{"f:image": map[string]any{}}
		}
		return []metav1.ManagedFieldsEntry{fieldsEntry(t, "argocd-controller", metav1.ManagedFieldsOperationUpdate, map[string]any{
			"f:spec": map[string]any{"f:template": map[string]any{"f:spec": map[string]any{"f:containers": containers}}},
		})}
	}
	if got := evidence(evidenceTarget(t, nil, owns("sidecar", "app")), nil); !got.OwnedByGitOps || got.Approximate || got.Partial {
		t.Errorf("controller owns every container's image: %+v", got)
	}
	if got := evidence(evidenceTarget(t, nil, owns("app")), nil); !got.OwnedByGitOps || !got.Approximate || !got.Partial {
		t.Errorf("controller owns one of two images: %+v", got)
	}

	init := buildGitOpsWriteEvidence(evidenceTarget(t, declares("sidecar", "app"), owns("sidecar", "app")), deploymentRef,
		[]string{"spec.template.spec.initContainers[*].image"}, argoOwner, evidenceArgoApp(respect, nil), nil).Paths[0]
	if !init.Empty || init.OwnedByGitOps || init.LastApplied == "present" {
		t.Errorf("a Deployment without init containers has nothing for the write to touch: %+v", init)
	}
}

// Argo CD 3 omits the Application's namespace from tracking ids in its own
// namespace; the Application's status confirms which one manages the object.
func TestWriteEvidenceArgoOwnerTracksTarget(t *testing.T) {
	app := func(resources ...any) *unstructured.Unstructured {
		u := evidenceArgoApp(nil, nil)
		u.Object["status"] = map[string]any{"resources": resources}
		return u
	}
	listed := map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "api"}
	other := map[string]any{"kind": "Service", "namespace": "prod", "name": "api"}
	if resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, nil, argoOwner, app(other, listed), nil); !resp.OwnerTracksTarget {
		t.Error("status lists the Deployment")
	}
	if resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, nil, argoOwner, app(other), nil); resp.OwnerTracksTarget {
		t.Error("status doesn't list the Deployment")
	}
}

func TestWriteEvidenceArgoWildcardIgnoreEntry(t *testing.T) {
	ignore := []any{map[string]any{"group": "*", "kind": "*", "jsonPointers": []any{"/spec/replicas"}}}
	resp := buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, argoOwner,
		evidenceArgoApp(map[string]any{"syncOptions": []any{"RespectIgnoreDifferences=true"}}, ignore), nil)
	if resp.Paths[0].Ignored != "effective" {
		t.Errorf(`group "*" and kind "*" match any object: %+v`, resp.Paths[0])
	}
}

func TestWriteEvidenceFluxIgnoreTargetIsARegex(t *testing.T) {
	owner := &topology.ResourceRef{Kind: "HelmRelease", Group: "helm.toolkit.fluxcd.io", Namespace: "flux-system", Name: "api"}
	ignored := func(target map[string]any) string {
		hr := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"driftDetection": map[string]any{"mode": "enabled", "ignore": []any{map[string]any{"paths": []any{"/spec/replicas"}, "target": target}}},
		}}}
		return buildGitOpsWriteEvidence(evidenceTarget(t, nil, nil), deploymentRef, []string{"spec.replicas"}, owner, hr, nil).Paths[0].Ignored
	}
	for _, tc := range []struct {
		target map[string]any
		want   string
	}{
		{map[string]any{"kind": "Deploy.*", "name": "a.i", "version": "v1"}, "effective"},
		{map[string]any{"name": "ap"}, ""},
		{map[string]any{"version": "v2"}, ""},
		{map[string]any{"name": "["}, ""},
	} {
		if got := ignored(tc.target); got != tc.want {
			t.Errorf("target %v: ignored %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestPickTrackingArgoApplication(t *testing.T) {
	app := func(namespace string, lists bool) *unstructured.Unstructured {
		resources := []any{map[string]any{"kind": "Service", "namespace": "prod", "name": "api"}}
		if lists {
			resources = append(resources, map[string]any{"group": "apps", "kind": "Deployment", "namespace": "prod", "name": "api"})
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "api", "namespace": namespace},
			"status":     map[string]any{"resources": resources},
		}}
	}
	pick := func(readable []*unstructured.Unstructured, candidates ...*unstructured.Unstructured) string {
		objs := make([]runtime.Object, 0, len(readable))
		for _, u := range readable {
			objs = append(objs, u)
		}
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
		return pickTrackingArgoApplication(context.Background(), dyn, candidates, "api", deploymentRef)
	}
	argocd, team := app("argocd", true), app("team", false)
	if got := pick([]*unstructured.Unstructured{argocd, team}, argocd, team); got != "argocd" {
		t.Errorf("the Application that lists the Deployment: got %q", got)
	}
	if got := pick([]*unstructured.Unstructured{team}, argocd, team); got != "" {
		t.Errorf("an Application the caller can't read is never named: got %q", got)
	}
	both := app("team", true)
	if got := pick([]*unstructured.Unstructured{argocd, both}, argocd, both); got != "" {
		t.Errorf("two Applications listing the object is ambiguous: got %q", got)
	}
}
