// Shared labels for rendering an issue's DiagnosticContext (the causal-link
// surface). Used by both the cluster Issues queue (IssuesView) and the
// per-resource Operational Issues block (ResourceIssuesSection) so the two
// can't drift on wording.

export function diagnosticRoleLabel(role: string): string {
  switch (role) {
    case 'candidate':
      return 'Candidate signal';
    case 'affected':
      return 'Affected signal';
    case 'rollup':
      return 'Rollup';
    default:
      return 'Context';
  }
}

export function diagnosticFactLabel(type: string): string {
  switch (type) {
    case 'explicit_reference':
      return 'Explicit reference';
    case 'owner_rollup':
      return 'Owner rollup';
    case 'selected_backend_issue':
      return 'Selected backend';
    case 'service_config_mismatch':
      return 'Service config';
    case 'service_env_reference':
      return 'Service env';
    case 'probe_target_mismatch':
      return 'Probe target';
    case 'blocked_init_container':
      return 'Init container';
    case 'restart_cause':
      return 'Restart cause';
    case 'node_blast_radius':
      return 'Affected workloads';
    case 'pvc_blast_radius':
      return 'Blocked pods';
    default:
      return type.replace(/_/g, ' ');
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
