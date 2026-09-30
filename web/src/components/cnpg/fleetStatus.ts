import type { CNPGFleetRow, HealthLevel } from '@skyhook-io/k8s-ui'

/**
 * A fleet row's status dot and the sentence behind it. Problems (warning or
 * worse) decide it first; without any, the operator's phase does, and a phase
 * Radar cannot read stays unknown rather than healthy.
 */
export function cnpgRowStatus(row: Pick<CNPGFleetRow, 'attention' | 'problems' | 'controllerStatus'>): { tone: HealthLevel; label: string } {
  const phase = `CNPG phase: ${row.controllerStatus.text || 'not reported'}`
  const counted = row.problems.filter((p) => p.severity !== 'posture')
  if (row.attention && counted.length > 0) {
    const critical = counted.filter((p) => p.severity === 'critical')
    const lead = critical[0] ?? counted[0]
    const more = counted.length - 1
    return {
      tone: critical.length > 0 ? 'unhealthy' : 'degraded',
      label: `${critical.length > 0 ? 'Critical' : 'Needs attention'}: ${lead.title}${more > 0 ? ` (+${more} more)` : ''}`,
    }
  }
  const level = row.controllerStatus.level
  switch (level) {
    case 'healthy':
    case 'neutral':
      return { tone: level, label: `No problems found · ${phase}` }
    case 'unknown':
      return { tone: 'unknown', label: `Status unknown · ${phase}` }
    default:
      return { tone: level, label: `No problems found, but the cluster is not healthy · ${phase}` }
  }
}

/** "pg-1 · replica (standby) · not ready": CNPG's docs say standby where the role label says replica. */
export function cnpgInstancePillLabel(pod: CNPGFleetRow['pods'][number]): string {
  const role = pod.role === 'primary' ? 'primary' : pod.role === 'replica' ? 'replica (standby)' : 'role unknown'
  const ready = pod.ready === true ? 'ready' : pod.ready === false ? 'not ready' : 'readiness unknown'
  return `${pod.name} · ${role} · ${ready}`
}
