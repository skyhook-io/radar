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
	// The next-run estimate is anchored on status.history, which a source,
	// build or dependency failure doesn't update while Flux retries on another
	// timer. Offer the interval only while the latest reconcile succeeded.
	if fluxReady(root) {
		s.Interval, _, _ = unstructured.NestedString(root.Object, "spec", "interval")
	}
	// No copy: an inventory can list thousands of entries and only its length is read.
	raw, _, _ := unstructured.NestedFieldNoCopy(root.Object, "status", "inventory", "entries")
	if entries, ok := raw.([]any); ok {
		s.Objects = len(entries)
	}

	var primary *gitops.FluxSourceRef
	for _, ref := range gitops.FluxSourceReferences(root) {
		if ref.Role == "source" || ref.Role == "chart reference" {
			primary = &ref
			break
		}
	}

	// chartSwitched: the inline chart now names another chart than the
	// deployed release, so equal versions don't mean it was applied.
	chartSwitched := false
	switch root.GetKind() {
	case "Kustomization":
		s.AppliedRevision, _, _ = unstructured.NestedString(root.Object, "status", "lastAppliedRevision")
	case "HelmRelease":
		if deployed := gitops.FluxDeployedRelease(root); deployed != nil {
			// A HelmChart's artifact revision is a chart version; every other
			// chart source is an OCI artifact compared by digest.
			if primary != nil && primary.Kind == "HelmChart" {
				s.AppliedRevision = deployed.ChartVersion
				// Only from a HelmRepository is spec.chart a chart name; from a
				// GitRepository or Bucket it is a path, which says nothing about it.
				desired, _, _ := unstructured.NestedString(root.Object, "spec", "chart", "spec", "chart")
				from, _, _ := unstructured.NestedString(root.Object, "spec", "chart", "spec", "sourceRef", "kind")
				chartSwitched = from == "HelmRepository" && desired != "" && deployed.ChartName != "" && desired != deployed.ChartName
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
		if primary.Kind == "HelmChart" {
			// A chart pulled from an OCI repository records "<version>@<digest>";
			// the release records the version alone, so the version is the fact.
			s.SourceRevision, _, _ = strings.Cut(s.SourceRevision, "@")
		}
		s.SourceFetchedAt = gitops.StringValue(node.Data["artifactUpdatedAt"])
		break
	}
	if s.SourceRevision != "" && s.AppliedRevision != "" {
		pending := chartSwitched || !fluxRevisionsMatch(s.SourceRevision, s.AppliedRevision)
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

func fluxReady(root *unstructured.Unstructured) bool {
	for _, c := range conditions(root) {
		if c.typ == "Ready" {
			return c.status == "True"
		}
	}
	return false
}
