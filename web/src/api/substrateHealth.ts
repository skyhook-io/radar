import { useQuery } from '@tanstack/react-query'
import { getApiBase } from './config'
import { ApiError, fetchJSON, useClusterInfo, useRadarFeature, type PrometheusTimeRange } from './client'

export type SubstrateChartState =
  | 'ok'
  | 'empty'
  | 'noSeries'
  | 'notAttributable'
  | 'duplicateSources'
  | 'ambiguous'
  | 'mismatch'
  | 'error'

export interface SubstrateChart {
  id: string
  title: string
  unit: string
  population: string
  source: string
  seriesBy?: string
  state: SubstrateChartState
  reason?: string
  isolation: { mode: 'configured' | 'verified' | 'unverified' | ''; labels?: Record<string, string>; note: string }
  series: { labels: Record<string, string>; dataPoints: { timestamp: number; value: number }[] }[]
  omitted?: number
  steps: number
  covered: number
}

export type SubstrateVerdictState =
  /** Substrate's own capacity alert condition has held for five minutes. */
  | 'short'
  | 'full'
  | 'serving'
  | 'noWorkers'
  /** A node: some actor's snapshot upload is overdue. */
  | 'behind'
  | 'current'
  | 'unknown'

export interface SubstrateFact {
  id: string
  label: string
  unit: string
  /** ok has a value; empty means the series exist and recorded nothing here; unavailable says why. */
  state: 'ok' | 'empty' | 'unavailable'
  value?: number
  of?: number
  reason?: string
  parts?: { label: string; value: number }[]
}

export interface SubstrateVerdict {
  state: SubstrateVerdictState
  reason?: string
  facts: SubstrateFact[]
}

export interface SubstrateHealth {
  /** available · notFound (no Substrate series at all) · notOnNode · notAttributable · error (every chart failed) */
  status: 'available' | 'notFound' | 'notOnNode' | 'notAttributable' | 'error'
  reason?: string
  range: string
  /** Unix seconds. */
  start: number
  end: number
  stepSeconds: number
  charts: SubstrateChart[]
  /** Set when status is available. */
  verdict?: SubstrateVerdict
}

export type SubstrateTarget =
  | { kind: 'workerpool'; namespace: string; name: string }
  | { kind: 'node'; name: string }

function substratePath(target: SubstrateTarget, range: PrometheusTimeRange): string {
  const query = new URLSearchParams({ range })
  return target.kind === 'workerpool'
    ? `/prometheus/substrate/workerpools/${encodeURIComponent(target.namespace)}/${encodeURIComponent(target.name)}?${query}`
    : `/prometheus/substrate/nodes/${encodeURIComponent(target.name)}?${query}`
}

/**
 * Curated Agent Substrate charts for one WorkerPool or Node. Requests only a
 * Radar that advertises the endpoints: Radar Hub can embed this UI against an
 * older Radar, which would answer with a 404.
 */
export function useSubstrateHealth(target: SubstrateTarget, range: PrometheusTimeRange, enabled: boolean) {
  const { data: cluster } = useClusterInfo()
  const feature = useRadarFeature('substrateHealthMetrics')
  const query = useQuery<SubstrateHealth>({
    queryKey: ['substrate-health', getApiBase(), cluster?.context, target.kind, target.kind === 'workerpool' ? target.namespace : '', target.name, range],
    queryFn: ({ signal }) => feature.guard(() => fetchJSON<SubstrateHealth>(substratePath(target, range), signal)),
    enabled: enabled && feature.support === 'supported',
    staleTime: 30_000,
    refetchInterval: 60_000,
    // A changed connection answers 409 and is safe to retry once; a denial is final.
    retry: (failureCount, error) => failureCount < 1 && error instanceof ApiError && error.status === 409,
  })
  return { ...query, upgrade: feature.upgrade, support: feature.support }
}
