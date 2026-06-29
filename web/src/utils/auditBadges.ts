import { resourceKey, type AuditFinding, type CheckMeta } from '@skyhook-io/k8s-ui'

export interface AuditBadgeMessage {
  severity: string
  message: string
}

export interface AuditSeverityCounts {
  danger: number
  warning: number
  /** The finding messages behind the counts, danger-first, for inline tooltips.
   *  Lets a badge say WHAT is wrong on hover instead of just a count. */
  messages: AuditBadgeMessage[]
}

/**
 * isBadgeWorthy keeps per-resource badges high-signal: only findings whose check
 * is flagged `badgeWorthy` in the registry (reference-integrity / lifecycle —
 * "this resource is actually broken") count. Security-posture and best-practice
 * checks fire on nearly every resource and would turn the badges into noise;
 * they live in the Checks/Audit views. Unknown checks default to NOT badged.
 */
export function isBadgeWorthy(
  finding: AuditFinding,
  checks: Record<string, CheckMeta> | undefined,
): boolean {
  return !!checks?.[finding.checkID]?.badgeWorthy
}

/**
 * buildAuditSeverityMap keys badge-worthy findings by the same resource key the
 * backend stamps onto topology nodes (`node.data.auditKey`): `group|Kind|ns|name`,
 * group following the audit convention (built-ins → their group, CRDs → "").
 */
export function buildAuditSeverityMap(
  findings: AuditFinding[] | undefined,
  checks: Record<string, CheckMeta> | undefined,
): Map<string, AuditSeverityCounts> {
  const map = new Map<string, AuditSeverityCounts>()
  for (const f of findings ?? []) {
    if (!isBadgeWorthy(f, checks)) continue
    const key = resourceKey(f.group ?? '', f.kind, f.namespace ?? '', f.name)
    const cur = map.get(key) ?? { danger: 0, warning: 0, messages: [] }
    if (f.severity === 'danger') cur.danger++
    else if (f.severity === 'warning') cur.warning++
    cur.messages.push({ severity: f.severity, message: f.message })
    map.set(key, cur)
  }
  for (const cur of map.values()) {
    cur.messages.sort((a, b) => (a.severity === 'danger' ? 0 : 1) - (b.severity === 'danger' ? 0 : 1))
  }
  return map
}
