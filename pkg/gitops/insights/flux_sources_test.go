package insights

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fluxTreeGetter map[gitopstree.ResourceRef]*unstructured.Unstructured

func (g fluxTreeGetter) GetDynamicWithGroup(_ context.Context, kind, ns, name, group string) (*unstructured.Unstructured, error) {
	return g[gitopstree.ResourceRef{Group: group, Kind: kind, Namespace: ns, Name: name}], nil
}
func fluxIssueObject(group, kind, ns, name string, spec map[string]any, ready, reason, message string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": kind, "metadata": map[string]any{"namespace": ns, "name": name, "uid": name + "-uid"}, "spec": spec, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready, "reason": reason, "message": message}}}}}
}
func fluxIssueTree(t *testing.T, root *unstructured.Unstructured, sources []*unstructured.Unstructured, denied string) *gitopstree.ResourceTree {
	t.Helper()
	g := fluxTreeGetter{}
	for _, obj := range append(sources, root) {
		parts := strings.SplitN(obj.GetAPIVersion(), "/", 2)
		g[gitopstree.ResourceRef{Group: parts[0], Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}] = obj
	}
	tree, _, err := gitopstree.NewBuilder(g, nil).WithReadCheck(func(ref gitopstree.ResourceRef) bool { return ref.Kind != denied }).Build(context.Background(), root.GetKind(), root.GetNamespace(), root.GetName(), strings.SplitN(root.GetAPIVersion(), "/", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
func TestFluxInlineChartBlockerUsesControllerMessage(t *testing.T) {
	message := "HelmChart 'shared/chart' is not ready: invalid chart reference: no version matching '>=99.0.0' found"
	root := fluxIssueObject("helm.toolkit.fluxcd.io", "HelmRelease", "team", "app", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo", "namespace": "shared"}}}}, "False", "SourceNotReady", message)
	root.Object["status"].(map[string]any)["helmChart"] = "shared/chart"
	chart := fluxIssueObject("source.toolkit.fluxcd.io", "HelmChart", "shared", "chart", map[string]any{"chart": "podinfo", "version": ">=99.0.0", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}, "False", "InvalidChartReference", "no version matches")
	chart.Object["status"].(map[string]any)["conditions"] = append(chart.Object["status"].(map[string]any)["conditions"].([]any), map[string]any{"type": "Stalled", "status": "True", "reason": "InvalidChartReference", "message": "no version matches"})
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "HelmRepository", "shared", "repo", nil, "True", "Succeeded", "fetched index")
	tree := fluxIssueTree(t, root, []*unstructured.Unstructured{chart, repo}, "")
	issues := buildIssues(root, tree, "fluxcd", nil)
	want := Ref{Group: "source.toolkit.fluxcd.io", Kind: "HelmChart", Namespace: "shared", Name: "chart"}
	if len(issues) != 1 || issues[0].Message != message || !reflect.DeepEqual(issues[0].Refs, []Ref{want}) || !strings.Contains(issues[0].Action, "Open") || tree.Summary.Degraded != 0 || tree.Summary.Dependencies != 2 || tree.Summary.Declared != 0 {
		t.Fatalf("chart attribution: summary=%+v issues=%+v", tree.Summary, issues)
	}
	denied := fluxIssueTree(t, root, []*unstructured.Unstructured{chart, repo}, "HelmChart")
	hiddenIssues := buildIssues(root, denied, "fluxcd", nil)
	if len(hiddenIssues) != 1 || len(hiddenIssues[0].Refs) != 0 || hiddenIssues[0].Message != message {
		t.Fatalf("denied chart borrowed status: %+v", hiddenIssues)
	}
}
func TestFluxSourceFetchWarning(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		for _, denied := range []string{"", "GitRepository"} {
			t.Run(fmt.Sprintf("artifact=%t denied=%s", artifact, denied), func(t *testing.T) {
				sourceSpec := map[string]any{"kind": "GitRepository", "name": "repo", "namespace": "shared"}
				root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": sourceSpec}, "True", "ReconciliationSucceeded", "applied old artifact")
				repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "shared", "repo", map[string]any{"url": "https://example.org/repo"}, "False", "FetchFailed", "private fetch failure")
				if artifact {
					repo.Object["status"].(map[string]any)["artifact"] = map[string]any{"revision": "main@sha1:abcdef0123456789"}
				}
				tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, denied)
				issues := buildIssues(root, tree, "fluxcd", nil)
				if denied != "" {
					if len(issues) != 0 {
						t.Fatalf("hidden status leaked: %+v", issues)
					}
					return
				}
				if len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Reason != "SourceNotReady" || issues[0].Scope != ScopeTree || len(issues[0].Refs) != 1 || issues[0].Refs[0].Name != "repo" || !strings.HasPrefix(issues[0].Message, "GitRepository shared/repo can't fetch updates") || issues[0].Cause != "private fetch failure" || strings.Contains(issues[0].Message, "blocked") {
					t.Fatalf("fetch warning: %+v", issues)
				}
				want := "has no artifact yet."
				if artifact {
					want = "Flux can keep applying the last fetched revision (main@abcdef0)"
				}
				if !strings.Contains(issues[0].Message, want) {
					t.Fatalf("artifact statement: %s", issues[0].Message)
				}
				if tree.Summary.Degraded != 0 || tree.Summary.OutOfSync != 0 || tree.Summary.Declared != 0 || tree.Summary.Dependencies != 1 {
					t.Fatalf("source counted as managed: %+v", tree.Summary)
				}
			})
		}
	}
}
func TestFluxRootBlockerMatchesOnlyDirectDeclarations(t *testing.T) {
	for _, tc := range []struct {
		reason, message string
		wantName        string
	}{
		{"ArtifactFailed", "Source artifact not found, retrying in 30s", "repo"},
		{"SourceNotReady", "Source is not ready", "repo"},
		{"DependencyNotReady", "dependency 'team/base' is not ready", "base"},
		{"DependencyNotReady", "dependency 'base' is not ready according to readyExpr eval", "base"},
		{"ArtifactFailed", "kustomization path not found: /tmp/source/missing", ""},
		{"DependencyNotReady", "dependencies do not meet ready condition", ""},
	} {
		t.Run(tc.message, func(t *testing.T) {
			root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}, "dependsOn": []any{map[string]any{"name": "base"}}}, "False", tc.reason, tc.message)
			repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "team", "repo", nil, "False", "FetchFailed", "fetch failure")
			base := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "base", nil, "True", "ReconciliationSucceeded", "applied older revision")
			tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo, base}, "")
			issues := buildIssues(root, tree, "fluxcd", nil)
			if issues[0].Message != tc.message {
				t.Fatalf("rewrote controller: %+v", issues)
			}
			if tc.wantName == "" {
				if len(issues[0].Refs) != 0 {
					t.Fatalf("invented blocker: %+v", issues)
				}
			} else if len(issues[0].Refs) != 1 || issues[0].Refs[0].Name != tc.wantName {
				t.Fatalf("wrong blocker: %+v", issues)
			}
			wantCount := 2
			if tc.wantName == "repo" {
				wantCount = 1
			}
			if len(issues) != wantCount {
				t.Fatalf("independent warning lost/duplicate: %+v", issues)
			}
		})
	}
}
func TestFluxAmbiguousSourceAndUnobservedSource(t *testing.T) {
	root := fluxIssueObject("helm.toolkit.fluxcd.io", "HelmRelease", "team", "app", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}}}, "False", "SourceNotReady", "source not ready")
	root.Object["status"].(map[string]any)["helmChart"] = "team/chart"
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "HelmRepository", "team", "repo", nil, "False", "FetchFailed", "repository failure")
	chart := fluxIssueObject("source.toolkit.fluxcd.io", "HelmChart", "team", "chart", nil, "False", "FetchFailed", "chart failure")
	tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo, chart}, "")
	issues := buildIssues(root, tree, "fluxcd", nil)
	if len(issues) != 3 || len(issues[0].Refs) != 0 {
		t.Fatalf("ambiguous source attributed: %+v", issues)
	}
	tree = fluxIssueTree(t, root, nil, "")
	issues = buildIssues(root, tree, "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 0 {
		t.Fatalf("unobserved source warned: %+v", issues)
	}
}
func TestFluxStalledSourceWarnsWithoutReadyFalse(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}, "True", "ReconciliationSucceeded", "applied")
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "team", "repo", nil, "Unknown", "Progressing", "building artifact")
	repo.Object["status"].(map[string]any)["conditions"] = append(repo.Object["status"].(map[string]any)["conditions"].([]any), map[string]any{"type": "Stalled", "status": "True", "reason": "FetchFailed", "message": "stalled"})
	tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, "")
	issues := buildIssues(root, tree, "fluxcd", nil)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Cause != "stalled" || strings.Contains(issues[0].Message+issues[0].Cause, "building artifact") {
		t.Fatalf("stalled source ignored: %+v", issues)
	}
}

func TestFluxDualRoleDependenciesKeepManagedChangesAndBlockerRefs(t *testing.T) {
	for _, kind := range []string{"GitRepository", "Kustomization"} {
		group := "source.toolkit.fluxcd.io"
		spec := map[string]any{"sourceRef": map[string]any{"kind": kind, "name": "base"}}
		reason := "SourceNotReady"
		if kind == "Kustomization" {
			group = "kustomize.toolkit.fluxcd.io"
			reason = "DependencyNotReady"
			spec = map[string]any{"dependsOn": []any{map[string]any{"name": "base"}}}
		}
		root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", spec, "False", reason, kind+" 'team/base' is not ready")
		root.Object["status"].(map[string]any)["inventory"] = map[string]any{"entries": []any{map[string]any{"id": "team_base_" + group + "_" + kind, "v": "v1"}}}
		dep := fluxIssueObject(group, kind, "team", "base", nil, "False", "FetchFailed", "own failure")
		tree := fluxIssueTree(t, root, []*unstructured.Unstructured{dep}, "")
		if tree.Summary.Declared != 1 || tree.Summary.Dependencies != 0 || tree.Summary.Degraded != 1 || len(tree.Edges) != 1 || tree.Edges[0].Type != gitopstree.EdgeOwns {
			t.Fatalf("dual-role membership: %+v", tree)
		}
		changes := buildChanges(root, tree, "fluxcd", nil)
		if len(changes) != 1 || changes[0].Dependency || !strings.Contains(changes[0].PartialNote, "inventory confirms") {
			t.Fatalf("managed change misclassified: %+v", changes)
		}
		issues := buildIssues(root, tree, "fluxcd", nil)
		if len(issues) != 1 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Kind != kind {
			t.Fatalf("owned dependency unlinked: %+v", issues)
		}
	}
}

func TestFluxQuotedKindDisambiguatesSameNameSource(t *testing.T) {
	root := fluxIssueObject("helm.toolkit.fluxcd.io", "HelmRelease", "team", "app", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "podinfo"}}}}, "False", "SourceNotReady", "HelmChart 'team/podinfo' is not ready")
	root.Object["status"].(map[string]any)["helmChart"] = "team/podinfo"
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "HelmRepository", "team", "podinfo", nil, "True", "Succeeded", "fetched")
	chart := fluxIssueObject("source.toolkit.fluxcd.io", "HelmChart", "team", "podinfo", nil, "False", "FetchFailed", "fetch failed")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{repo, chart}, ""), "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Kind != "HelmChart" {
		t.Fatalf("same-name source misattributed: %+v", issues)
	}
}

func TestFluxRootArtifactAbsenceLinksObservedUnknownSource(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "ExternalArtifact", "name": "artifact"}}, "False", "ArtifactFailed", "Source artifact not found")
	artifact := fluxIssueObject("source.toolkit.fluxcd.io", "ExternalArtifact", "team", "artifact", nil, "", "", "")
	delete(artifact.Object["status"].(map[string]any), "conditions")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{artifact}, ""), "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Name != "artifact" {
		t.Fatalf("controller artifact absence not linked: %+v", issues)
	}
}

func TestFluxBlockerDoesNotHideUnrelatedManagedFailure(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}, "False", "SourceNotReady", "GitRepository 'team/repo' is not ready")
	root.Object["status"].(map[string]any)["inventory"] = map[string]any{"entries": []any{map[string]any{"id": "team_repo_source.toolkit.fluxcd.io_GitRepository", "v": "v1"}}}
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "team", "repo", nil, "False", "FetchFailed", "fetch failed")
	tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, "")
	tree.Nodes = append(tree.Nodes, gitopstree.Node{ID: "workload", Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "team", Name: "crashing"}, Health: "Degraded"})
	tree.Summary = gitopstree.Summarize(tree.Nodes)
	issues := buildIssues(root, tree, "fluxcd", nil)
	if len(issues) != 2 || issues[1].Reason != "DegradedResources" || issues[1].Message != "1 managed resource is degraded" || tree.Summary.Degraded != 2 {
		t.Fatalf("unrelated managed failure hidden: %+v", issues)
	}
}

func TestFluxHealthySourceDependencyDoesNotClaimSync(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}, "True", "ReconciliationSucceeded", "applied")
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "team", "repo", nil, "True", "Succeeded", "fetched")
	changes := buildChanges(root, fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, ""), "fluxcd", nil)
	if len(changes) != 1 || !changes[0].Dependency || changes[0].Sync != "" || changes[0].Category != CategoryUnknown || changes[0].Health != "Healthy" || !strings.Contains(changes[0].PartialNote, "source dependency") {
		t.Fatalf("source falsely synced: %+v", changes)
	}
}

func TestFluxVerbatimChartMessageDoesNotBlameChartNameRepository(t *testing.T) {
	message := "HelmChart 'flux-system/flux-system-podinfo-broken' is not ready: invalid chart reference: failed to get chart version for remote reference: no 'podinfo' chart with version matching '>=99.0.0' found"
	root := fluxIssueObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "podinfo-broken", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "podinfo"}}}}, "False", "SourceNotReady", message)
	root.Object["status"].(map[string]any)["helmChart"] = "flux-system/flux-system-podinfo-broken"
	chart := fluxIssueObject("source.toolkit.fluxcd.io", "HelmChart", "flux-system", "flux-system-podinfo-broken", map[string]any{"chart": "podinfo", "version": ">=99.0.0", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "podinfo"}}, "False", "InvalidChartReference", "invalid chart reference: failed to get chart version for remote reference: no 'podinfo' chart with version matching '>=99.0.0' found")
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "HelmRepository", "flux-system", "podinfo", nil, "True", "Succeeded", "stored artifact")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{chart, repo}, ""), "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Kind != "HelmChart" || issues[0].Message != message {
		t.Fatalf("chart package name blamed repository: %+v", issues)
	}
}

func TestFluxDependsOnDoesNotBlameSameNameSource(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "podinfo"}, "dependsOn": []any{map[string]any{"name": "podinfo"}}}, "False", "DependencyNotReady", "dependency 'flux-system/podinfo' is not ready")
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "podinfo", nil, "False", "GitOperationFailed", "authentication required")
	dep := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "podinfo", nil, "True", "Succeeded", "applied older revision")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{repo, dep}, ""), "fluxcd", nil)
	if len(issues) != 2 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Kind != "Kustomization" || issues[1].Refs[0].Kind != "GitRepository" {
		t.Fatalf("source mistaken for prerequisite or warning lost: %+v", issues)
	}
}

func TestFluxFetchFailedWarnsOnManagedAndDependencySources(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, artifact := range []bool{false, true} {
			root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "flux-system", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"}}, "True", "ReconciliationSucceeded", "applied previous artifact")
			if owned {
				root.Object["status"].(map[string]any)["inventory"] = map[string]any{"entries": []any{map[string]any{"id": "flux-system_flux-system_source.toolkit.fluxcd.io_GitRepository", "v": "v1"}}}
			}
			repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "flux-system", nil, "Unknown", "ProgressingWithRetry", "building artifact")
			repo.Object["status"].(map[string]any)["conditions"] = append(repo.Object["status"].(map[string]any)["conditions"].([]any), map[string]any{"type": "FetchFailed", "status": "True", "reason": "GitOperationFailed", "message": "authentication required."})
			if artifact {
				repo.Object["status"].(map[string]any)["artifact"] = map[string]any{"revision": "main@sha1:abcdef0123456789"}
			}
			tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, "")
			issues := buildIssues(root, tree, "fluxcd", nil)
			if len(issues) != 1 || issues[0].Severity != SeverityWarning || !strings.Contains(issues[0].Message, "can't fetch updates") || issues[0].Cause != "authentication required." || strings.Contains(issues[0].Message+issues[0].Cause, "building artifact") {
				t.Fatalf("owned=%t artifact=%t: %+v", owned, artifact, issues)
			}
			if artifact != strings.Contains(issues[0].Message, "last fetched revision (main@abcdef0)") {
				t.Fatalf("artifact claim: %+v", issues)
			}
			if owned {
				if tree.Summary.Declared != 1 || tree.Summary.Dependencies != 0 {
					t.Fatalf("warning changed ownership: %+v", tree.Summary)
				}
			}

			ready := repo.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)
			ready["status"], ready["reason"], ready["message"] = "False", "GitOperationFailed", "authentication required."
			settledTree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, "")
			settledIssues := buildIssues(root, settledTree, "fluxcd", nil)
			if len(settledIssues) != 1 || settledIssues[0].Scope != ScopeTree || owned && settledTree.Summary.Degraded != 1 || !owned && settledTree.Summary.Degraded != 0 {
				t.Fatalf("settled source duplicated aggregate: owned=%t summary=%+v issues=%+v", owned, settledTree.Summary, settledIssues)
			}
			if denied := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, "GitRepository"), "fluxcd", nil); len(denied) != 0 {
				t.Fatalf("denied fetch failure leaked: %+v", denied)
			}
		}
	}
}

func TestFluxUnknownWithoutFailureDoesNotWarnOrImplyBlocker(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}, "False", "SourceNotReady", "source not ready")
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "team", "repo", nil, "Unknown", "Progressing", "building artifact")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, ""), "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 0 {
		t.Fatalf("unknown failure invented: %+v", issues)
	}
}

func TestFluxBlockerDoesNotReferenceRemoteInventoryOverlap(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "fleet", map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "spoke"}}, "sourceRef": map[string]any{"kind": "GitRepository", "name": "flux-system"}}, "False", "SourceNotReady", "GitRepository 'flux-system/flux-system' is not ready")
	root.Object["status"].(map[string]any)["inventory"] = map[string]any{"entries": []any{map[string]any{"id": "flux-system_flux-system_source.toolkit.fluxcd.io_GitRepository", "v": "v1"}}}
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "flux-system", nil, "False", "GitOperationFailed", "local source failure")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, ""), "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 0 || strings.Contains(issues[0].Message, "local source failure") {
		t.Fatalf("local blocker attributed to remote declaration: %+v", issues)
	}
}

func TestFluxArtifactFailedCarriesTheSourceError(t *testing.T) {
	// Live shape: the root only says the artifact is missing; the source says why.
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "app", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}, "False", "ArtifactFailed", "Source artifact not found, retrying in 30s")
	repo := fluxIssueObject("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "repo", map[string]any{"url": "https://example.org/missing"}, "False", "GitOperationFailed", "failed to checkout and determine revision: repository not found")
	tree := fluxIssueTree(t, root, []*unstructured.Unstructured{repo}, "")
	issues := buildIssues(root, tree, "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Name != "repo" || issues[0].Cause != "GitRepository repo: failed to checkout and determine revision: repository not found" {
		t.Fatalf("source error not carried: %+v", issues)
	}
}

func TestFluxDependencyBlockerCarriesThePrerequisiteError(t *testing.T) {
	root := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "app", map[string]any{"dependsOn": []any{map[string]any{"name": "base"}}}, "False", "DependencyNotReady", "dependency 'flux-system/base' is not ready")
	base := fluxIssueObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "base", nil, "False", "BuildFailed", "kustomization path not found: ./missing")
	issues := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{base}, ""), "fluxcd", nil)
	if len(issues) != 1 || len(issues[0].Refs) != 1 || issues[0].Refs[0].Name != "base" || issues[0].Cause != "Kustomization base: kustomization path not found: ./missing" {
		t.Fatalf("prerequisite error not carried: %+v", issues)
	}
	denied := buildIssues(root, fluxIssueTree(t, root, []*unstructured.Unstructured{base}, "Kustomization"), "fluxcd", nil)
	if len(denied) != 1 || denied[0].Cause != "" || len(denied[0].Refs) != 0 {
		t.Fatalf("unreadable prerequisite quoted: %+v", denied)
	}
}
