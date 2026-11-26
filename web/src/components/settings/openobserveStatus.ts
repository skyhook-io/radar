import type { MetricsSource } from '../../api/client'

export interface MetricsSourceLine {
  status: string
  detail?: string
  tone: 'success' | 'warning' | 'neutral'
}

// The one set of metric family names Radar shows (mirrored by FamilyLabel in
// pkg/prom/openobserve for the CLI). Each starts with a capital, even mid-list.
const FAMILY_LABELS: Record<string, string> = {
  cpu: 'CPU', memory: 'Memory', throttling: 'CPU throttling', network: 'Network',
  filesystem: 'Disk I/O', restarts: 'Restarts', volumes: 'Volumes', nodes: 'Nodes', hpa: 'HPA',
}

export const familyLabel = (family: string) => FAMILY_LABELS[family] ?? family

/** The one phrase for metric families OpenObserve holds no data for. */
export const missingFamiliesNote = (families: string[]) => `No data in OpenObserve for: ${families.map(familyLabel).join(', ')}.`

/** An unavailable source's error without the "OpenObserve unavailable" its heading already says. */
export const unavailableReason = (error: string | undefined) => error?.replace(/^OpenObserve unavailable: /, '')

/**
 * The status line for a metrics source, or undefined when the source is not
 * serving this context (the Prometheus line applies; see `sourceNote`).
 */
export function metricsSourceLine(source: MetricsSource | undefined): MetricsSourceLine | undefined {
  if (!source || source.kind !== 'openobserve') return undefined
  const shared = source.via === 'local' ? ' · shared connection' : ''
  if (source.unavailable) {
    return { status: 'OpenObserve unavailable', detail: unavailableReason(source.error), tone: 'warning' }
  }
  if (!source.matchedBy) return undefined
  const details: string[] = []
  // The backend's standing warning (name match, refused collector metrics)
  // says why and what to fix; the short hint stands in only without one.
  if (source.warning) details.push(source.warning)
  else if (source.matchedBy === 'name') details.push("Add the cluster ID label so this match can't break.")
  if (source.versionWarning) details.push(source.versionWarning)
  if (source.missingFamilies?.length) {
    details.push(missingFamiliesNote(source.missingFamilies))
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
