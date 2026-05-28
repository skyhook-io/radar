package audit

import "github.com/skyhook-io/radar/pkg/checks"

// EffectiveFindings converts raw (already locally-filtered) findings into the
// effective findings the Checks rollup consumes — detector-default severity
// mapping and state. This is the OSS single-cluster bridge: it has no org
// policy, so local settings (applied upstream via ApplySettings) are the only
// filter and no severity is ever overridden. The Hub builds its own effective
// findings (with org policy) and calls checks.BuildChecks directly.
func EffectiveFindings(findings []Finding, clusterID string) []checks.EffectiveFinding {
	out := make([]checks.EffectiveFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, checks.EffectiveFinding{
			Source: checks.SourceRadarBuiltin,
			Resource: checks.ResourceRef{
				ClusterID: clusterID,
				Group:     f.Group,
				Kind:      f.Kind,
				Namespace: f.Namespace,
				Name:      f.Name,
			},
			CheckID:           f.CheckID,
			Category:          f.Category,
			OriginalSeverity:  f.Severity,
			EffectiveSeverity: checks.MapSeverity(f.Severity),
			Message:           f.Message,
			State:             checks.DefaultEffectiveState(),
		})
	}
	return out
}
