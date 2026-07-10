import type { IssueSeverity } from './types';
import { BADGE_SEVERITY_COLORS as sev } from '../ui/Badge';
import { NEUTRAL_CHIP_CLASS } from '../ui/CardSection';
import {
  TONE_FILL_CLASS,
  TONE_HEADER_BAND_CLASS,
  TONE_RAIL_CLASS,
  TONE_SOLID_CLASS,
  TONE_TEXT_CLASS,
  type SeverityTone,
} from '../ui/severity-tone';

// Visual language for the 2-tier Issues severity. critical = red, warning =
// amber. Issues and Checks are different severity axes but must read as one
// product, so the color strings are shared via the tone module; here we only map
// each tier onto its tone. The pill still pulls from the canonical Badge tones
// so it matches every status badge elsewhere.
const ISSUE_SEVERITY_TONE: Record<IssueSeverity, SeverityTone> = {
  critical: 'red',
  warning: 'amber',
};

const byTone = <T,>(toneMap: Record<SeverityTone, T>): Record<IssueSeverity, T> => ({
  critical: toneMap[ISSUE_SEVERITY_TONE.critical],
  warning: toneMap[ISSUE_SEVERITY_TONE.warning],
});

export const ISSUE_SEVERITY_LABEL: Record<IssueSeverity, string> = {
  critical: 'Critical',
  warning: 'Warning',
};

// Pill badge — the loud, explicit severity signal on a row (rendered with `badge-sm`).
export const ISSUE_SEVERITY_BADGE_CLASS: Record<IssueSeverity, string> = {
  critical: sev.error,
  warning: sev.warning,
};

export const ISSUE_SEVERITY_TEXT_CLASS = byTone(TONE_TEXT_CLASS);
export const ISSUE_SEVERITY_FILL_CLASS = byTone(TONE_FILL_CLASS);
export const ISSUE_SEVERITY_RAIL_CLASS = byTone(TONE_RAIL_CLASS);
export const ISSUE_SEVERITY_SOLID_CLASS = byTone(TONE_SOLID_CLASS);
export const ISSUE_SEVERITY_HEADER_BAND_CLASS = byTone(TONE_HEADER_BAND_CLASS);

const GROUP_CHIP_EMPHASIS_CLASS = 'inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-semibold leading-tight bg-theme-elevated text-theme-text-primary ring-1 ring-theme-border-light';

const EMPHASIZED_GROUPS = new Set(['control_plane', 'unknown']);

export function groupBadgeClass(group: string): string {
  return EMPHASIZED_GROUPS.has(group) ? GROUP_CHIP_EMPHASIS_CLASS : NEUTRAL_CHIP_CLASS;
}

// Display labels. The server emits raw snake_case category/group enums (so a
// new category needs no frontend deploy to APPEAR); the UI humanizes for
// display, falling back to title-cased snake_case for anything unmapped.
const CATEGORY_LABEL: Record<string, string> = {
  unschedulable: "Can't be scheduled",
  quota_exceeded: 'Quota exceeded',
  admission_webhook_blocking: 'Admission blocked',
  image_pull_failed: 'Image pull failed',
  container_waiting: 'Container waiting',
  init_container_failed: 'Init container failed',
  crashloop: 'Crash loop',
  oom_killed: 'OOM killed',
  liveness_probe_failed: 'Health check failing',
  readiness_failed: 'Not ready for traffic',
  workload_degraded: 'Workload degraded',
  high_restart: 'High restart count',
  missing_config_ref: 'Missing reference',
  pdb_blocks_evictions: 'Evictions blocked',
  secret_sync_failed: 'Secret sync failed',
  service_no_endpoints: 'No endpoints',
  ingress_backend_missing: 'Ingress backend missing',
  load_balancer_pending: 'Load balancer pending',
  gateway_not_ready: 'Gateway not ready',
  gateway_route_invalid: 'Gateway route invalid',
  dns_failure: 'DNS failure',
  network_policy_block: 'Network policy block',
  pvc_pending: 'PVC pending',
  pvc_lost: 'PVC lost',
  pv_failed: 'PV failed',
  pvc_resize_failed: 'PVC resize failed',
  volume_mount_failed: 'Volume mount failed',
  volume_access_mode_conflict: 'Volume access conflict',
  job_failed: 'Job failed',
  cronjob_failed: 'CronJob failed',
  rollout_stalled: 'Rollout stalled',
  hpa_limited_or_failed: 'Autoscaling limited',
  rbac_forbidden: 'Permission denied',
  certificate_not_ready: 'Certificate not ready',
  pod_security_violation: 'Pod Security blocked',
  node_not_ready: 'Node not ready',
  node_provisioning_failed: 'Node provisioning failed',
  apiservice_unavailable: 'API extension unavailable',
  crossplane_reconcile_failed: 'Crossplane reconcile failed',
  termination_stuck: 'Stuck terminating',
  operator_condition_failed: 'Controller reports a problem',
  gitops_sync_failed: 'GitOps sync failed',
  gitops_render_failed: 'GitOps render failed',
  gitops_spec_invalid: 'GitOps spec invalid',
  gitops_operation_failed: 'GitOps operation failed',
  gitops_out_of_sync: 'GitOps out of sync',
  gitops_health_degraded: 'GitOps health degraded',
  helm_release_failed: 'Helm release failed',
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
