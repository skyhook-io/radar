package tree

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxSourceFacts(t *testing.T) {
	for _, tc := range []struct {
		kind                              string
		spec                              map[string]any
		revision, display, url, requested string
		info                              []InfoItem
	}{
		{"GitRepository", map[string]any{"url": "https://github.com/team/repo", "ref": map[string]any{"branch": "main"}}, "main@sha1:abcdef0123456789", "main@abcdef0", "https://github.com/team/repo", "branch: main", []InfoItem{{Name: "Source", Value: "github.com/team/repo"}}},
		{"OCIRepository", map[string]any{"url": "oci://ghcr.io/team/app", "ref": map[string]any{"tag": "v1.2.3"}}, "v1.2.3@sha256:abcdef0123456789", "v1.2.3@sha256:abcdef0", "oci://ghcr.io/team/app", "tag: v1.2.3", []InfoItem{{Name: "Source", Value: "ghcr.io/team/app"}}},
		{"HelmRepository", map[string]any{"url": "https://charts.example.org"}, "sha256:abcdef0123456789", "", "https://charts.example.org", "", []InfoItem{{Name: "Source", Value: "charts.example.org"}}},
		{"Bucket", map[string]any{"endpoint": "https://s3.example.org/", "bucketName": "manifests"}, "sha256:abcdef0123456789", "sha256:abcdef0", "https://s3.example.org/manifests", "", []InfoItem{{Name: "Source", Value: "s3.example.org/manifests"}}},
		{"HelmChart", map[string]any{"chart": "podinfo", "version": ">=99.0.0"}, "1.2.3-build.abcdef0123456789", "1.2.3-build.abcdef0123456789", "", "", []InfoItem{{Name: "Chart", Value: "podinfo@>=99.0.0"}}},
		{"ExternalArtifact", map[string]any{}, "custom-long-revision-123456789", "custom-long-revision-123456789", "", "", nil},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": tc.kind, "spec": tc.spec, "status": map[string]any{"artifact": map[string]any{"revision": tc.revision}, "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "FetchFailed", "message": "fetch failed"}}}}}
			node := enrichNodeFromObject(Node{Tool: ToolFluxCD, Sync: "Synced"}, obj)
			display, _ := node.Data["revision"].(string)
			if node.Sync != "" || node.Health != "Degraded" || node.HealthSource != HealthSourceController || node.Data["artifactRevision"] != tc.revision || display != tc.display || node.Data["artifact"] != true || node.Data["readyMessage"] != "fetch failed" || node.Data["readyReason"] != "FetchFailed" || node.Data["readyStatus"] != "False" || !reflect.DeepEqual(node.Info, tc.info) {
				t.Fatalf("source facts: %+v", node)
			}
			if got, _ := node.Data["sourceURL"].(string); got != tc.url {
				t.Fatalf("url=%q want %q", got, tc.url)
			}
			if got, _ := node.Data["requestedRef"].(string); got != tc.requested {
				t.Fatalf("requested=%q want %q", got, tc.requested)
			}
			if tc.kind == "HelmChart" && (node.Data["chart"] != "podinfo" || node.Data["requestedVersion"] != ">=99.0.0") {
				t.Fatalf("chart facts: %v", node.Data)
			}
			delete(obj.Object["status"].(map[string]any), "artifact")
			fresh := enrichNodeFromObject(Node{Tool: ToolFluxCD}, obj)
			if fresh.Data["artifact"] != false || fresh.Data["artifactRevision"] != nil || fresh.Data["revision"] != nil {
				t.Fatalf("invented artifact: %v", fresh.Data)
			}
			obj.SetAPIVersion("example.org/v1")
			other := enrichNodeFromObject(Node{Tool: ToolFluxCD, Sync: "Synced"}, obj)
			if other.Sync != "Synced" || other.Data["sourceURL"] != nil || other.Data["artifact"] != nil {
				t.Fatalf("kind collision: %+v", other)
			}
		})
	}
}

func TestFluxRequestedRefs(t *testing.T) {
	for _, kind := range []string{"GitRepository", "OCIRepository"} {
		fields := []string{"branch", "tag", "semver", "commit", "name"}
		if kind == "OCIRepository" {
			fields = []string{"tag", "semver", "digest"}
		}
		for _, field := range fields {
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": kind, "spec": map[string]any{"ref": map[string]any{field: "requested"}}}}
			node := enrichNodeFromObject(Node{Tool: ToolFluxCD}, obj)
			if node.Data["requestedRef"] != field+": requested" {
				t.Fatalf("%s %s: %v", kind, field, node.Data)
			}
		}
	}
}

func TestSummarySeparatesDependencies(t *testing.T) {
	got := Summarize([]Node{{Role: RoleRoot, Health: "Degraded", Sync: "OutOfSync"}, {Role: RoleDeclared, Health: "Degraded", Sync: "OutOfSync", Dependency: true}, {Role: RoleDeclared, Health: "Degraded", Sync: "OutOfSync"}, {Role: RoleGenerated, Health: "Healthy"}, {Role: RoleGroup, Count: 3, Health: "Degraded"}})
	want := Summary{Dependencies: 1, Declared: 1, Generated: 1, Grouped: 3, Degraded: 1, OutOfSync: 1}
	if got != want {
		t.Fatalf("summary=%+v want %+v", got, want)
	}
}

func TestFluxSourceAddressOmitsCredentials(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "spec": map[string]any{"url": "https://user:private-token@example.org/team/repo?token=secret#private"}}}
	node := enrichNodeFromObject(Node{Tool: ToolFluxCD}, obj)
	if node.Data["sourceURL"] != "https://example.org/team/repo" || len(node.Info) != 1 || node.Info[0].Value != "example.org/team/repo" {
		t.Fatalf("credential-bearing source address: %+v", node)
	}
}

func TestFluxSourceFactsPreserveArgoControllerVerdict(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "spec": map[string]any{"url": "https://example.org/repo"}, "status": map[string]any{"artifact": map[string]any{"revision": "main@sha1:abcdef0123456789"}, "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "GitOperationFailed", "message": "fetch failed"}}}}}
	node := enrichNodeFromObject(Node{Tool: ToolArgoCD, Sync: "Synced", Health: "Healthy", HealthSource: HealthSourceController, TopologyStatus: "healthy"}, obj)
	if node.Sync != "Synced" || node.Health != "Healthy" || node.HealthSource != HealthSourceController || node.TopologyStatus != "healthy" || node.Data["artifact"] != nil || node.Data["sourceURL"] != nil || node.Data["artifactRevision"] != nil {
		t.Fatalf("Flux enrichment changed Argo's tree: %+v", node)
	}
}

func TestHelmReleaseRevisionIsTheInstalledChart(t *testing.T) {
	// helm.toolkit.fluxcd.io/v2 has no lastReleaseRevision; comparing the
	// attempted chart version against nothing flagged every healthy release.
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "status": map[string]any{
		"lastAttemptedRevision": "6.15.0",
		"history":               []any{map[string]any{"chartVersion": "6.15.0", "status": "deployed"}, map[string]any{"chartVersion": "6.14.0", "status": "superseded"}},
	}}}
	node := enrichNodeFromObject(Node{Tool: ToolFluxCD}, obj)
	if node.Data["revision"] != "6.15.0" || node.Data["attemptedRevision"] != "6.15.0" {
		t.Fatalf("revision = %v attempted = %v", node.Data["revision"], node.Data["attemptedRevision"])
	}
	// A failed upgrade to 6.16.0 leaves 6.15.0 installed.
	obj.Object["status"] = map[string]any{
		"lastAttemptedRevision": "6.16.0",
		"history":               []any{map[string]any{"chartVersion": "6.16.0", "status": "failed"}, map[string]any{"chartVersion": "6.15.0", "status": "deployed"}},
	}
	node = enrichNodeFromObject(Node{Tool: ToolFluxCD}, obj)
	if node.Data["revision"] != "6.15.0" || node.Data["attemptedRevision"] != "6.16.0" {
		t.Fatalf("failed upgrade: revision = %v attempted = %v", node.Data["revision"], node.Data["attemptedRevision"])
	}
}
