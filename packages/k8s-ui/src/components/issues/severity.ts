import type { IssueSeverity } from './types';

// Visual language for the 2-tier Issues severity, deliberately reusing the
// SAME class strings as the Checks queue (components/checks/severity.ts):
// critical = red (= Checks `critical`), warning = amber (= Checks `medium`).
// Issues and Checks are different severity axes, but the queues must read as
// one product — sharing the exact hues makes the rails/pills pixel-identical.
//
// Class strings are literal so each consuming app's Tailwind @source scan emits
// them.

export const ISSUE_SEVERITY_LABEL: Record<IssueSeverity, string> = {
  critical: 'Critical',
  warning: 'Warning',
};

// Pill badge — the loud, explicit severity signal on a row.
export const ISSUE_SEVERITY_BADGE_CLASS: Record<IssueSeverity, string> = {
  critical: 'bg-red-50 text-red-700 ring-1 ring-red-200 dark:bg-red-950/50 dark:text-red-300 dark:ring-red-900',
  warning: 'bg-amber-50 text-amber-700 ring-1 ring-amber-200 dark:bg-amber-950/50 dark:text-amber-300 dark:ring-amber-900',
};

// Solid fill — dots + the proportional distribution bar segments.
export const ISSUE_SEVERITY_FILL_CLASS: Record<IssueSeverity, string> = {
  critical: 'bg-red-500',
  warning: 'bg-amber-500',
};

export const ISSUE_SEVERITY_TEXT_CLASS: Record<IssueSeverity, string> = {
  critical: 'text-red-600 dark:text-red-400',
  warning: 'text-amber-600 dark:text-amber-400',
};

// Left accent rail on a queue row — the scan-down severity cue.
export const ISSUE_SEVERITY_RAIL_CLASS: Record<IssueSeverity, string> = {
  critical: 'border-l-red-500 hover:bg-red-50/40 dark:hover:bg-red-950/20',
  warning: 'border-l-amber-500 hover:bg-amber-50/30 dark:hover:bg-amber-950/15',
};

// Category-group accent — the quiet classification tag (severity is the loud
// one). One hue per group; unknown/unmapped falls back to a neutral theme tag.
const GROUP_BADGE_CLASS: Record<string, string> = {
  scheduling: 'bg-violet-50 text-violet-700 ring-1 ring-violet-200 dark:bg-violet-950/40 dark:text-violet-300 dark:ring-violet-900',
  startup: 'bg-sky-50 text-sky-700 ring-1 ring-sky-200 dark:bg-sky-950/40 dark:text-sky-300 dark:ring-sky-900',
  runtime: 'bg-rose-50 text-rose-700 ring-1 ring-rose-200 dark:bg-rose-950/40 dark:text-rose-300 dark:ring-rose-900',
  configuration: 'bg-teal-50 text-teal-700 ring-1 ring-teal-200 dark:bg-teal-950/40 dark:text-teal-300 dark:ring-teal-900',
  networking: 'bg-indigo-50 text-indigo-700 ring-1 ring-indigo-200 dark:bg-indigo-950/40 dark:text-indigo-300 dark:ring-indigo-900',
  storage: 'bg-cyan-50 text-cyan-700 ring-1 ring-cyan-200 dark:bg-cyan-950/40 dark:text-cyan-300 dark:ring-cyan-900',
  scaling: 'bg-fuchsia-50 text-fuchsia-700 ring-1 ring-fuchsia-200 dark:bg-fuchsia-950/40 dark:text-fuchsia-300 dark:ring-fuchsia-900',
  security: 'bg-amber-50 text-amber-700 ring-1 ring-amber-200 dark:bg-amber-950/40 dark:text-amber-300 dark:ring-amber-900',
  control_plane: 'bg-slate-100 text-slate-600 ring-1 ring-slate-200 dark:bg-slate-800/60 dark:text-slate-300 dark:ring-slate-700',
};

export function groupBadgeClass(group: string): string {
  return GROUP_BADGE_CLASS[group] ?? 'bg-theme-elevated text-theme-text-secondary ring-1 ring-theme-border';
}

// Display labels. The server emits raw snake_case category/group enums (so a
// new category needs no frontend deploy to APPEAR); the UI humanizes for
// display, falling back to title-cased snake_case for anything unmapped.
const CATEGORY_LABEL: Record<string, string> = {
  unschedulable: 'Unschedulable',
  quota_exceeded: 'Quota exceeded',
  admission_webhook_blocking: 'Admission blocked',
  image_pull_failed: 'Image pull failed',
  container_waiting: 'Container waiting',
  init_container_failed: 'Init container failed',
  crashloop: 'Crash loop',
  oom_killed: 'OOM killed',
  liveness_probe_failed: 'Liveness probe failing',
  readiness_failed: 'Readiness failing',
  workload_degraded: 'Workload degraded',
  high_restart: 'High restart count',
  missing_config_ref: 'Missing reference',
  pdb_blocks_evictions: 'PDB blocks evictions',
  service_no_endpoints: 'No endpoints',
  ingress_backend_missing: 'Ingress backend missing',
  dns_failure: 'DNS failure',
  network_policy_block: 'Network policy block',
  pvc_pending: 'PVC pending',
  pvc_lost: 'PVC lost',
  volume_mount_failed: 'Volume mount failed',
  volume_access_mode_conflict: 'Volume access conflict',
  job_failed: 'Job failed',
  cronjob_failed: 'CronJob failed',
  rollout_stalled: 'Rollout stalled',
  hpa_limited_or_failed: 'HPA limited',
  rbac_forbidden: 'RBAC forbidden',
  certificate_not_ready: 'Certificate not ready',
  pod_security_violation: 'Pod Security violation',
  node_not_ready: 'Node not ready',
  operator_condition_failed: 'Controller condition',
  gitops_sync_failed: 'GitOps sync failed',
  gitops_render_failed: 'GitOps render failed',
  gitops_spec_invalid: 'GitOps spec invalid',
  gitops_operation_failed: 'GitOps operation failed',
  gitops_out_of_sync: 'GitOps out of sync',
  gitops_health_degraded: 'GitOps health degraded',
  webhook_backend_down: 'Webhook backend down',
  control_plane_not_ready: 'Control plane not ready',
  machine_not_ready: 'Machine not ready',
  unknown: 'Unknown',
};

const GROUP_LABEL: Record<string, string> = {
  scheduling: 'Scheduling',
  startup: 'Startup',
  runtime: 'Runtime',
  configuration: 'Configuration',
  networking: 'Networking',
  storage: 'Storage',
  scaling: 'Scaling',
  security: 'Security',
  control_plane: 'Control plane',
  unknown: 'Unknown',
};

function humanize(raw: string): string {
  if (!raw) return '';
  const spaced = raw.replace(/_/g, ' ');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

export function categoryLabel(category: string): string {
  return CATEGORY_LABEL[category] ?? humanize(category);
}

export function groupLabel(group: string): string {
  return GROUP_LABEL[group] ?? humanize(group);
}
