// Shared Checks identity contract + data shapes + severity vocabulary.
//
// k8s-ui owns these because the Checks queue presentation (ChecksView) is
// host-agnostic: Radar Hub feeds it fleet-resolved data, and OSS Radar can feed
// a single-cluster ("fleet of one") resolve. Hosts map their wire payloads onto
// these types; the components render against them.
//
// Mirrors Radar OSS's resource-key convention (radar/pkg/audit.ResourceKey).

/** Canonical Checks severity ladder — distinct from the raw detector severity
 *  (danger/warning) so operational criticality and compliance risk stay
 *  separate axes. */
export type CheckSeverity = 'critical' | 'high' | 'medium' | 'low';

/** Raw detector severity Radar emits. */
export type RadarSeverity = 'danger' | 'warning';

/** Ordered worst→least, for rendering severity filters/sorts consistently. */
export const CHECK_SEVERITIES: CheckSeverity[] = ['critical', 'high', 'medium', 'low'];

/** Worst-first ordering rank for the ladder. */
export const CHECK_SEVERITY_RANK: Record<CheckSeverity, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
};

export function isCheckSeverity(s: string): s is CheckSeverity {
  return s === 'critical' || s === 'high' || s === 'medium' || s === 'low';
}

/** mapRadarSeverity maps a raw detector severity to the Checks ladder
 *  (danger→high, warning→medium). critical/low are only reachable via an org
 *  severity override; the detector never emits them. */
export function mapRadarSeverity(raw: RadarSeverity | string): CheckSeverity {
  switch (raw) {
    case 'danger':
      return 'high';
    case 'warning':
      return 'medium';
    default:
      return 'medium';
  }
}

/**
 * Canonical resource identity. `group` is '' for the core API group;
 * `namespace` is '' for cluster-scoped resources. Both are always present.
 * `cluster_id` scopes the ref to its source cluster — the disambiguator when
 * two clusters' display names collapse to the same label.
 */
export interface CheckResourceRef {
  cluster_id: string;
  group: string;
  kind: string;
  namespace: string;
  name: string;
}

/** Built-in (Radar-detected) finding source. The only V1 source. */
export const SOURCE_RADAR_BUILTIN = 'radar_builtin';

/**
 * resourceKey mirrors Go `audit.ResourceKey(group, kind, namespace, name)`:
 * `group|Kind|namespace|name`. Group first because group and namespace can each
 * independently be empty; `|` is delimiter-safe (K8s API groups follow
 * DNS-subdomain rules and can't contain it).
 */
export function resourceKey(group: string, kind: string, namespace: string, name: string): string {
  return `${group}|${kind}|${namespace}|${name}`;
}

export function resourceRefKey(ref: CheckResourceRef): string {
  return resourceKey(ref.group, ref.kind, ref.namespace, ref.name);
}

/**
 * checkFindingKey is the canonical built-in finding key:
 * `cluster_id + source + resourceKey + checkID + optional detail`. cluster_id is
 * part of the key so identical resources across two clusters never collide,
 * even when display names render the same.
 */
export function checkFindingKey(
  clusterId: string,
  source: string,
  resKey: string,
  checkID: string,
  detail?: string,
): string {
  const base = `${clusterId} ${source} ${resKey} ${checkID}`;
  return detail ? `${base} ${detail}` : base;
}

/** Explains how org config shaped a finding. */
export interface EffectiveFindingState {
  visibility: 'visible' | 'hidden';
  source: 'detector_default' | 'org_config';
  scoreImpact: 'counts' | 'excluded';
  alertImpact: 'alerts' | 'muted';
  complianceImpact: 'counts' | 'excluded_by_config';
  reason?: string;
}

export interface EffectiveCheckFinding {
  source: 'radar_builtin';
  resource: CheckResourceRef;
  checkID: string;
  category: string;
  originalSeverity: RadarSeverity;
  effectiveSeverity: CheckSeverity;
  message: string;
  state: EffectiveFindingState;
}

/**
 * A failing check, rolled up across every resource that fails it — one row of
 * the remediation queue. `subject` is the most-severe representative resource;
 * `findings` holds the per-resource detail underneath. (Distinct from CheckMeta,
 * the check's static definition.)
 */
export interface Check {
  id: string;
  source: 'radar_builtin';
  subject: CheckResourceRef;
  checkID: string;
  category: string;
  effectiveSeverity: CheckSeverity;
  title: string;
  message: string;
  affectedFindings: number;
  affectedResources: number;
  representativeFinding: EffectiveCheckFinding;
  findings: EffectiveCheckFinding[];
  /** Source cluster's environment label (e.g. "prod"), shown as a context tag.
   *  Empty for OSS single-cluster and unlabeled clusters. */
  environment?: string;
}
