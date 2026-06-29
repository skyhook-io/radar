// Shared labels for rendering an issue's DiagnosticContext (the causal-link
// surface). Used by both the cluster Issues queue (IssuesView) and the
// per-resource Operational Issues block (ResourceIssuesSection) so the two
// can't drift on wording.

// Operator-facing — describes the issue's place in the causal picture in plain
// language, not Radar's internal role taxonomy.
export function diagnosticRoleLabel(role: string): string {
  switch (role) {
    case 'candidate':
      return 'Possible cause';
    case 'affected':
      return 'Affected';
    case 'rollup':
      return 'Grouped';
    default:
      return 'Context';
  }
}

// Operator-facing fact labels — plain language over the implementation-shaped
// internal type names.
export function diagnosticFactLabel(type: string): string {
  switch (type) {
    case 'explicit_reference':
      return 'Missing reference';
    case 'owner_rollup':
      return 'Grouped from pods';
    case 'selected_backend_issue':
      return 'Backend pods';
    case 'service_config_mismatch':
      return 'Service config';
    case 'service_env_reference':
      return 'Referenced service';
    case 'probe_target_mismatch':
      return 'Probe target';
    case 'blocked_init_container':
      return 'Init container';
    case 'restart_cause':
      return 'Restart evidence';
    case 'node_blast_radius':
      return 'Affected workloads';
    case 'pvc_blast_radius':
      return 'Blocked pods';
    case 'apiservice_hpa':
      return 'Stalled autoscalers';
    case 'secret_not_ready':
      return 'Dependent pods';
    default:
      return type.replace(/_/g, ' ');
  }
}

// Operator-facing lead-in for the symptom→root pointer chip. Honest per fact
// type: a declared PVC/Secret edge is a cause; a co-located node is only
// "related" (node pressure can be a shared victim, not the root), so it must not
// claim "caused by".
export function incidentParentLabel(factType?: string, confidence?: string): string {
  switch (factType) {
    case 'pvc_blast_radius':
    case 'secret_not_ready':
      return 'Caused by';
    case 'apiservice_hpa':
      return 'Likely cause';
    case 'node_blast_radius':
      return 'Related';
    default:
      return confidence === 'high' ? 'Caused by' : 'Possible cause';
  }
}

// Plain-language gloss for the confidence chip's tooltip — the operator should
// know a medium link is "these are co-located, the node may be the cause", not a
// proven fact.
export function confidenceTitle(confidence: string): string {
  switch (confidence) {
    case 'high':
      return 'High confidence: a declared structural link (selector, owner, or claim reference).';
    case 'medium':
      return 'Medium confidence: these resources are related, but causation is inferred — verify before acting.';
    case 'low':
      return 'Low confidence: a heuristic match.';
    default:
      return '';
  }
}
