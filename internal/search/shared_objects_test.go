package search

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// Search reads cached objects in place (CacheProvider.ListDynamic returns the
// cache's own objects), so no step may change one: the same objects back
// every other reader. This runs every search mode over every report shape and
// fails if any listed object differs afterwards.
func TestSearch_LeavesListedObjectsUnchanged(t *testing.T) {
	secrets := trivyReport("ExposedSecretReport", "shop", "replicaset-web-secrets", map[string]any{
		"artifact": map[string]any{"repository": "library/php", "tag": "8.3"},
		"secrets": []any{map[string]any{
			"ruleID": "aws-access-key-id", "category": "AWS", "severity": "CRITICAL", "target": "/app/.env",
			"match": "AWS_ACCESS_KEY_ID=AKIAREALSECRETVALUE", "code": map[string]any{"lines": []any{map[string]any{"content": "AKIAREALSECRETVALUE"}}},
		}},
		"summary": map[string]any{"criticalCount": float64(1)},
	})
	sbom := trivyReport("SbomReport", "shop", "replicaset-web-sbom", map[string]any{
		"components": map[string]any{"components": []any{map[string]any{"name": "autoconf", "version": "2.72", "purl": "pkg:deb/debian/autoconf@2.72"}}},
	})
	audit := trivyReport("ConfigAuditReport", "shop", "replicaset-web", map[string]any{
		"checks": []any{map[string]any{"checkID": "KSV012", "title": "Runs as root", "severity": "MEDIUM", "success": false}},
	})
	vulns := vulnReport("shop", "replicaset-web-php", "library/php", "8.3", vuln("CVE-2026-109618", "php", "8.3.10", "8.3.36", "CRITICAL"))
	vulns.Object["report"].(map[string]any)["summary"] = map[string]any{"criticalCount": float64(1), "highCount": int64(0)}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "web", "namespace": "argocd", "annotations": map[string]any{"note": "CVE-2026-109618 tracked"}},
		"spec":     map[string]any{"replicas": float64(3), "source": map[string]any{"repoURL": "https://example.com/web"}},
	}}

	gvr := func(resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: trivyGroup, Version: "v1alpha1", Resource: resource}
	}
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gvr("exposedsecretreports"): {secrets}, gvr("sbomreports"): {sbom}, gvr("configauditreports"): {audit},
			gvr("vulnerabilityreports"): {vulns}, appGVR: {app},
		},
		kinds: map[schema.GroupVersionResource]string{
			gvr("exposedsecretreports"): "ExposedSecretReport", gvr("sbomreports"): "SbomReport", gvr("configauditreports"): "ConfigAuditReport",
			gvr("vulnerabilityreports"): "VulnerabilityReport", appGVR: "Application",
		},
		namespaced: map[schema.GroupVersionResource]bool{
			gvr("exposedsecretreports"): true, gvr("sbomreports"): true, gvr("configauditreports"): true,
			gvr("vulnerabilityreports"): true, appGVR: true,
		},
	}
	listed := []*unstructured.Unstructured{secrets, sbom, audit, vulns, app}
	before := make([]map[string]any, len(listed))
	for i, u := range listed {
		before[i] = runtime.DeepCopyJSON(u.Object)
	}

	reportFilter, err := filter.CompileObjectFilter(`has(report.vulnerabilities) && report.vulnerabilities.exists(v, v.severity == "CRITICAL") || has(spec.replicas) && spec.replicas > 1`)
	if err != nil {
		t.Fatal(err)
	}
	readEverything := func(_ runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
		return resourcecontext.BuildSummary(u, resourcecontext.SummaryOptions{})
	}
	for _, q := range []string{"CVE-2026-109618", "aws-access-key-id", "autoconf", "KSV012", "web", "kind:ExposedSecretReport", "image:php"} {
		for _, mode := range []IncludeMode{IncludeNone, IncludeSummary, IncludeRaw} {
			for _, f := range []*filter.Filter{nil, reportFilter} {
				if _, err := Search(context.Background(), p, Parse(q), Options{Include: mode, Filter: f, SummaryBuilder: readEverything}); err != nil {
					t.Fatalf("q=%s: %v", q, err)
				}
			}
		}
	}
	for i, u := range listed {
		if !reflect.DeepEqual(before[i], u.Object) {
			t.Errorf("%s/%s changed during search", u.GetKind(), u.GetName())
		}
	}
}
