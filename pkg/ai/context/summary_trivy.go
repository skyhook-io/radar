package context

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TrivyGroup is the API group Trivy Operator writes its reports in. Its
// reports have no spec or status: every finding lives under a top-level
// `report` field.
const TrivyGroup = "aquasecurity.github.io"

// summarizeTrivyReport summarizes a Trivy Operator report (VulnerabilityReport,
// SbomReport, ConfigAuditReport, ExposedSecretReport, …): the scanned image,
// the object the scan was about, finding counts and when it ran. A report
// describes one image at scan time, so the scan age is part of the status.
func summarizeTrivyReport(obj *unstructured.Unstructured) *ResourceSummary {
	// Read in place: NestedMap deep-copies, and a report can hold thousands of
	// findings this summary never reads.
	report, _ := obj.Object["report"].(map[string]any)
	labels := obj.GetLabels()
	s := &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
		Image:     TrivyArtifactImage(report),
	}
	if kind, name := labels["trivy-operator.resource.kind"], labels["trivy-operator.resource.name"]; kind != "" && name != "" {
		s.Owner = kind + "/" + name
	}
	if c := labels["trivy-operator.container.name"]; c != "" {
		s.Target = "container " + c
	}

	var parts []string
	if ts, _, _ := unstructured.NestedString(report, "updateTimestamp"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			parts = append(parts, "scanned "+age(t)+" ago")
		}
	}
	if counts := trivyCounts(report); counts != "" {
		parts = append(parts, counts)
	}
	s.Status = strings.Join(parts, ": ")
	return s
}

// TrivyArtifactImage renders a report's scanned image as a pullable
// reference (registry/repository:tag@digest).
func TrivyArtifactImage(report map[string]any) string {
	artifact, _ := report["artifact"].(map[string]any)
	repo, _ := artifact["repository"].(string)
	if repo == "" {
		return ""
	}
	ref := repo
	registry, _ := report["registry"].(map[string]any)
	if server, _ := registry["server"].(string); server != "" {
		ref = server + "/" + repo
	}
	if tag, _ := artifact["tag"].(string); tag != "" {
		ref += ":" + tag
	}
	if digest, _ := artifact["digest"].(string); digest != "" {
		ref += "@" + digest
	}
	return ref
}

// trivyCounts renders report.summary's non-zero counts, worst first. Scan
// reports use criticalCount..unknownCount; SBOM reports count components.
func trivyCounts(report map[string]any) string {
	summary, _ := report["summary"].(map[string]any)
	if len(summary) == 0 {
		return ""
	}
	var parts []string
	for _, k := range []struct{ key, label string }{
		{"criticalCount", "critical"},
		{"highCount", "high"},
		{"mediumCount", "medium"},
		{"lowCount", "low"},
		{"unknownCount", "unknown"},
		{"componentsCount", "components"},
	} {
		if n := countValue(summary[k.key]); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k.label))
		}
	}
	if len(parts) == 0 {
		return "no findings"
	}
	return strings.Join(parts, ", ")
}

func countValue(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
