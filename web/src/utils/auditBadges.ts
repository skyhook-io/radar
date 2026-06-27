import { resourceKey, type AuditFinding } from '@skyhook-io/k8s-ui'

export interface AuditSeverityCounts {
  danger: number
  warning: number
}

/**
 * buildAuditSeverityMap keys audit findings by the same resource key the backend
 * stamps onto topology nodes (`node.data.auditKey`) and the resource list
 * computes per row: `group|Kind|namespace|name`, group following the audit
 * convention (built-ins → their group, CRDs → ""). Lets the topology/list
 * surfaces join Cluster Audit findings with a single string lookup.
 */
export function buildAuditSeverityMap(
  findings: AuditFinding[] | undefined,
): Map<string, AuditSeverityCounts> {
  const map = new Map<string, AuditSeverityCounts>()
  for (const f of findings ?? []) {
    const key = resourceKey(f.group ?? '', f.kind, f.namespace ?? '', f.name)
    const cur = map.get(key) ?? { danger: 0, warning: 0 }
    if (f.severity === 'danger') cur.danger++
    else if (f.severity === 'warning') cur.warning++
    map.set(key, cur)
  }
  return map
}
