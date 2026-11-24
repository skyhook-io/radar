import type { MetricsSource } from '../../api/client'

export interface MetricsSourceLine {
  status: string
  detail?: string
  tone: 'success' | 'warning' | 'neutral'
}

const FAMILY_LABELS: Record<string, string> = {
  cpu: 'CPU', memory: 'memory', throttling: 'CPU throttling', network: 'network',
  filesystem: 'disk I/O', restarts: 'restarts', volumes: 'volumes', nodes: 'nodes', hpa: 'HPA',
}

/**
 * The status line for a metrics source, or undefined when the source is not
 * serving this context (the Prometheus line applies; see `sourceNote`).
 */
export function metricsSourceLine(source: MetricsSource | undefined): MetricsSourceLine | undefined {
  if (!source || source.kind !== 'openobserve') return undefined
  const shared = source.via === 'local' ? ' · shared connection' : ''
  if (source.unavailable) {
    return { status: 'OpenObserve unavailable', detail: source.error, tone: 'warning' }
  }
  if (!source.matchedBy) return undefined
  const details: string[] = []
  if (source.matchedBy === 'name') details.push("Add the cluster ID label so this match can't break.")
  if (source.versionWarning) details.push(source.versionWarning)
  if (source.missingFamilies?.length) {
    details.push(`Not collected: ${source.missingFamilies.map(f => FAMILY_LABELS[f] ?? f).join(', ')}.`)
  }
  return {
    status: `OpenObserve${shared} · matched by ${source.matchedBy === 'id' ? 'cluster ID' : 'confirmed name'}`,
    detail: details.length ? details.join(' ') : undefined,
    tone: 'success',
  }
}

/** Why a configured source isn't serving this context, if it says. */
export function sourceNote(source: MetricsSource | undefined): string | undefined {
  if (!source || source.unavailable || source.matchedBy) return undefined
  return source.error ?? source.warning
}
