package insights

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/skyhook-io/radar/pkg/gitops"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var fluxQuotedReference = regexp.MustCompile("(?:([A-Z][A-Za-z0-9]*)[ \t]+)?['\"`]([^'\"`]+)['\"`]")

// IsFluxBlockerCondition reports a Flux root condition saying an upstream
// object blocks it. Its message is the root's own; only the linked ref and the
// cause quoted from that object depend on reading the object.
func IsFluxBlockerCondition(issue Issue) bool {
	if issue.Scope != ScopeCondition {
		return false
	}
	switch issue.Reason {
	case "SourceNotReady", "ArtifactFailed", "DependencyNotReady":
		return true
	}
	return false
}

// fluxFailureDetail is the dependency's own explanation of its failure.
func fluxFailureDetail(node gitopstree.Node) string {
	if fetchFailed, _ := node.Data["fetchFailed"].(bool); fetchFailed {
		if detail := stringData(node.Data, "fetchFailedMessage"); detail != "" {
			return detail
		}
	}
	if stringData(node.Data, "readyStatus") == "False" {
		if detail := stringData(node.Data, "readyMessage"); detail != "" {
			return detail
		}
	}
	if stalled, _ := node.Data["stalled"].(bool); stalled {
		return stringData(node.Data, "stalledMessage")
	}
	return ""
}

func attachFluxBlocker(issue Issue, root *unstructured.Unstructured, tree *gitopstree.ResourceTree) Issue {
	if tree == nil {
		return issue
	}
	switch issue.Reason {
	case "SourceNotReady", "ArtifactFailed", "DependencyNotReady":
	default:
		return issue
	}
	direct := map[Ref]bool{}
	for _, source := range gitops.FluxSourceReferences(root) {
		if issue.Reason == "DependencyNotReady" {
			break
		}
		direct[Ref{Group: source.Group, Kind: source.Kind, Namespace: source.Namespace, Name: source.Name}] = true
	}
	// Inventory ownership can replace a dependsOn edge without erasing the declaration.
	deps, _, _ := unstructured.NestedSlice(root.Object, "spec", "dependsOn")
	for _, raw := range deps {
		dep, ok := raw.(map[string]any)
		if issue.Reason != "DependencyNotReady" || !ok || gitops.StringValue(dep["name"]) == "" {
			continue
		}
		direct[Ref{Group: tree.Root.Ref.Group, Kind: tree.Root.Ref.Kind, Namespace: firstNonEmpty(gitops.StringValue(dep["namespace"]), root.GetNamespace()), Name: gitops.StringValue(dep["name"])}] = true
	}
	quoted := fluxQuotedReference.FindAllStringSubmatch(issue.Message, -1)
	sourceMessage := strings.ToLower(issue.Message)
	missingArtifact := strings.HasPrefix(sourceMessage, "source artifact not found") || strings.HasPrefix(sourceMessage, "source is not ready, artifact not found")
	var notReadySources []Ref
	for _, node := range tree.Nodes {
		ref := refFromTree(node.Ref)
		if !direct[ref] || node.Role == gitopstree.RoleRoot || node.Remote {
			continue
		}
		for _, match := range quoted {
			if match[1] != "" && match[1] != "Source" && match[1] != ref.Kind {
				continue
			}
			bareName := match[2] == ref.Name && (match[1] == ref.Kind || match[1] == "Source" || issue.Reason == "DependencyNotReady")
			if bareName || match[2] == ref.Namespace+"/"+ref.Name || match[2] == ref.Kind+"/"+ref.Namespace+"/"+ref.Name {
				issue.Refs = append(issue.Refs, ref)
				break
			}
		}
		stalled, _ := node.Data["stalled"].(bool)
		if artifact, observed := node.Data["artifact"].(bool); observed && ref.Group == "source.toolkit.fluxcd.io" && (stringData(node.Data, "readyStatus") == "False" || stalled || missingArtifact && !artifact) {
			notReadySources = append(notReadySources, ref)
		}
	}
	// ArtifactFailed also covers local build-path errors; the reason alone does
	// not implicate a source:
	// https://github.com/fluxcd/kustomize-controller/blob/main/internal/controller/kustomization_controller.go
	if len(issue.Refs) == 0 && len(notReadySources) == 1 && issue.Reason != "DependencyNotReady" &&
		(issue.Reason == "SourceNotReady" || strings.HasPrefix(sourceMessage, "source ") || strings.HasPrefix(sourceMessage, "could not get source object:")) {
		issue.Refs = notReadySources
	}
	// The root's message often stops at "artifact not found"; the linked
	// object's own error is the actual reason, so it becomes the cause.
	if len(issue.Refs) == 1 {
		for _, node := range tree.Nodes {
			if refFromTree(node.Ref) != issue.Refs[0] {
				continue
			}
			if detail := fluxFailureDetail(node); detail != "" && !strings.Contains(issue.Message, detail) {
				issue.Cause = fmt.Sprintf("%s %s: %s", node.Ref.Kind, node.Ref.Name, detail)
			}
			break
		}
	}
	if len(issue.Refs) > 0 {
		if issue.Reason == "DependencyNotReady" {
			issue.Action = "Open the referenced dependency and fix the prerequisite reported by the controller."
		} else {
			issue.Action = "Open the referenced source or chart and fix the reported fetch or artifact failure."
		}
	}
	return issue
}

func fluxSourceWarnings(tree *gitopstree.ResourceTree, issues []Issue) []Issue {
	if tree == nil {
		return nil
	}
	referenced := map[Ref]bool{}
	for _, issue := range issues {
		if issue.Scope == ScopeCondition {
			for _, ref := range issue.Refs {
				referenced[ref] = true
			}
		}
	}
	var out []Issue
	for _, node := range tree.Nodes {
		ref := refFromTree(node.Ref)
		relationship := stringData(node.Data, "relationship")
		if ref.Group != "source.toolkit.fluxcd.io" || referenced[ref] || (relationship != "source" && relationship != "chart source" && relationship != "chart reference") || node.Remote {
			continue
		}
		artifact, observed := node.Data["artifact"].(bool)
		stalled, _ := node.Data["stalled"].(bool)
		fetchFailed, _ := node.Data["fetchFailed"].(bool)
		if !observed || (stringData(node.Data, "readyStatus") != "False" && !stalled && !fetchFailed) {
			continue
		}
		// The consequence leads: the error is long and the band truncates.
		message := fmt.Sprintf("%s %s/%s can't fetch updates and has no artifact yet.", ref.Kind, ref.Namespace, ref.Name)
		if artifact {
			revision := stringData(node.Data, "revision")
			if revision == "" {
				revision = stringData(node.Data, "artifactRevision")
			}
			message = fmt.Sprintf("%s %s/%s can't fetch updates. Flux can keep applying the last fetched revision (%s), but new changes won't land until the fetch succeeds.", ref.Kind, ref.Namespace, ref.Name, revision)
		}
		out = append(out, Issue{Severity: SeverityWarning, Scope: ScopeTree, Reason: "SourceNotReady", Message: message, Cause: fluxFailureDetail(node), Refs: []Ref{ref}, Action: "Open the source or chart and fix the reported fetch failure."})
	}
	return out
}

func unexplainedFluxDegraded(tree *gitopstree.ResourceTree, issues []Issue) int {
	referenced := map[Ref]bool{}
	for _, issue := range issues {
		if issue.Scope == ScopeCondition && issue.Severity == SeverityCritical || issue.Scope == ScopeTree && issue.Reason == "SourceNotReady" {
			for _, ref := range issue.Refs {
				referenced[ref] = true
			}
		}
	}
	count := tree.Summary.Degraded
	for _, node := range tree.Nodes {
		if !node.Dependency && (node.Role == gitopstree.RoleDeclared || node.Role == gitopstree.RoleGenerated) &&
			(node.Health == "Degraded" || node.Health == "Missing") && referenced[refFromTree(node.Ref)] {
			count--
		}
	}
	return count
}
