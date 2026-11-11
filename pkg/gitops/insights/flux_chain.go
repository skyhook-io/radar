package insights

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/gitops"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// applyFluxChain fills what the source has fetched against what Flux applied.
// The tree is already filtered for the caller, so a source they can't read
// has no node and leaves Pending nil.
func applyFluxChain(s *Summary, root *unstructured.Unstructured, tree *gitopstree.ResourceTree) {
	s.Interval, _, _ = unstructured.NestedString(root.Object, "spec", "interval")
	if entries, present, _ := unstructured.NestedSlice(root.Object, "status", "inventory", "entries"); present {
		s.Objects = len(entries)
	} else if tree != nil {
		s.Objects = tree.Summary.Declared
	}

	var primary *gitops.FluxSourceRef
	for _, ref := range gitops.FluxSourceReferences(root) {
		if ref.Role == "source" || ref.Role == "chart reference" {
			primary = &ref
			break
		}
	}

	switch root.GetKind() {
	case "Kustomization":
		s.AppliedRevision, _, _ = unstructured.NestedString(root.Object, "status", "lastAppliedRevision")
	case "HelmRelease":
		if deployed := gitops.FluxDeployedRelease(root); deployed != nil {
			// A HelmChart's artifact revision is a chart version; every other
			// chart source is an OCI artifact compared by digest.
			if primary != nil && primary.Kind == "HelmChart" {
				s.AppliedRevision = deployed.ChartVersion
			} else {
				s.AppliedRevision = deployed.OCIDigest
			}
		}
	}

	if primary == nil || tree == nil {
		return
	}
	for _, node := range tree.Nodes {
		if node.Remote || node.Ref.Group != primary.Group || node.Ref.Kind != primary.Kind || node.Ref.Namespace != primary.Namespace || node.Ref.Name != primary.Name {
			continue
		}
		s.SourceRevision = gitops.StringValue(node.Data["artifactRevision"])
		s.SourceFetchedAt = gitops.StringValue(node.Data["artifactUpdatedAt"])
		break
	}
	if s.SourceRevision != "" && s.AppliedRevision != "" {
		source := s.SourceRevision
		if primary.Kind == "HelmChart" {
			// A chart pulled from an OCI repository records "<version>@<digest>";
			// the release records the version alone.
			source, _, _ = strings.Cut(source, "@")
		}
		pending := !fluxRevisionsMatch(source, s.AppliedRevision)
		s.Pending = &pending
	}
}

// fluxRevisionsMatch compares digests by value: OCI sources write
// "<tag>@sha256:…" while a HelmRelease may record the bare digest.
func fluxRevisionsMatch(a, b string) bool {
	if strings.Contains(a, "sha256:") && strings.Contains(b, "sha256:") {
		return a[strings.LastIndex(a, "@")+1:] == b[strings.LastIndex(b, "@")+1:]
	}
	return a == b
}
