package helm

import (
	"fmt"
	"reflect"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourceid"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
)

// OperationPreflightError means validation/preparation failed before Helm ran.
// Used by MCP fresh install and the shared values upgrade path. Failures inside
// Helm may also change nothing, but are conservatively unclassified. Other writers
// may still change the release independently.
type OperationPreflightError struct{ Err error }

func (e *OperationPreflightError) Error() string { return e.Err.Error() }
func (e *OperationPreflightError) Unwrap() error { return e.Err }

// MCPUpgradeRequest keeps previews and writes on the same caller-scoped path.
// Empty Version retains the stored chart only when history is unambiguous.
type MCPUpgradeRequest struct {
	Namespace, Name, Version, Repository string
	Values                               map[string]any
	ReplaceValues, DryRun                bool
	ExpectedRevision                     int
}

// MCPUpgradeResult contains no rendered manifests or raw values.
type MCPUpgradeResult struct {
	ChartVersion string
	// Revision is the resulting revision, hypothetical during preview.
	Revision         int
	BaselineRevision int
	ValuesDiff       string
	Added            []ResourceRef
	Changed          []ResourceRef
	Removed          []ResourceRef
	ParseErrors      int
}

// UpgradeForMCPAsUser previews or executes an upgrade without round-tripping
// stored credentials. DryRun renders with caller-authorized cluster lookups but
// does not execute hooks, mutate workloads, or prove admission/readiness.
func (c *Client) UpgradeForMCPAsUser(req MCPUpgradeRequest, user *pkgauth.User) (*MCPUpgradeResult, error) {
	cfg, err := c.getActionConfigForUser(req.Namespace, user)
	if err != nil {
		return nil, err
	}
	before, after, err := c.runValuesUpgrade(cfg, req.Name, req.Version, req.Repository, req.Values, valuesUpgradeOptions{
		replace: req.ReplaceValues, dryRun: req.DryRun, allowStoredChart: true, expectedRevision: req.ExpectedRevision,
	}, progressSender(nil))
	if err != nil {
		return nil, err
	}
	return mcpUpgradeResult(before, after, req.DryRun)
}

func mcpUpgradeResult(before, after *release.Release, dryRun bool) (*MCPUpgradeResult, error) {
	var err error
	out := &MCPUpgradeResult{ChartVersion: after.Chart.Metadata.Version, Revision: after.Version, BaselineRevision: before.Version}
	if !dryRun {
		return out, nil
	}
	out.ValuesDiff, err = computeRedactedValuesDiff(&HelmValues{UserSupplied: cloneInstallValues(before.Config)}, &HelmValues{UserSupplied: cloneInstallValues(after.Config)}, before.Version, after.Version, false)
	if err != nil {
		return nil, fmt.Errorf("summarize preview values: %w", err)
	}
	out.ValuesDiff = aicontext.RedactSecrets(out.ValuesDiff)
	left, leftErrors := parseManifestResourceObjects(before.Manifest, before.Namespace)
	right, rightErrors := parseManifestResourceObjects(after.Manifest, after.Namespace)
	// Helm matches live resources without API version: changing the served
	// version of the same group/kind/name updates it rather than deleting it.
	identity := func(r renderedResource) string {
		return resourceid.ResourceKey(r.Object.GroupVersionKind().Group, r.Ref.Kind, r.Ref.Namespace, r.Ref.Name)
	}
	leftMap := make(map[string]renderedResource, len(left))
	for _, r := range left {
		leftMap[identity(r)] = r
	}
	rightKeys := make(map[string]bool, len(right))
	out.ParseErrors = leftErrors + rightErrors
	for _, r := range right {
		key := identity(r)
		rightKeys[key] = true
		old, exists := leftMap[key]
		if !exists {
			out.Added = append(out.Added, r.Ref)
			continue
		}
		// Compare complete rendered objects, not the timeline's kind-specific summary.
		// Metadata is part of the proposed change, including helm.sh/chart labels.
		if !reflect.DeepEqual(old.Object.Object, r.Object.Object) {
			out.Changed = append(out.Changed, r.Ref)
		}
	}
	for _, r := range left {
		if !rightKeys[identity(r)] {
			out.Removed = append(out.Removed, r.Ref)
		}
	}
	return out, nil
}

// StoredChartAsUser reads a release revision (<=0 means latest); it never fetches a chart
// from a repository. Helm storage may omit dependency chart bodies.
func (c *Client) StoredChartAsUser(namespace, name string, revision int, user *pkgauth.User) (*chart.Chart, error) {
	rel, err := c.getReleaseRevisionAsUser(namespace, name, revision, user)
	if err != nil {
		return nil, err
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable stored chart")
	}
	return rel.Chart, nil
}
