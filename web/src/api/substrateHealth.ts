import { useQuery } from '@tanstack/react-query'
import { getApiBase } from './config'
import type { TimeSeries } from '@skyhook-io/k8s-ui/components/charts'
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
  /** The metrics read, in the spelling Prometheus holds them. */
  source: string
  state: SubstrateChartState
  /** Why there are no series; on an ok chart, which named series has no data. */
  reason?: string
  isolation: { mode: 'configured' | 'verified' | 'unverified' | ''; labels?: Record<string, string>; note: string }
  /** One display label per series, under `series`. A null value is a gap. */
  series: TimeSeries[]
  /** Series beyond the first 12. */
  omitted?: number
}

export type SubstrateVerdictState =
  /** Substrate's own capacity alert condition has held for five minutes. */
  | 'workerShortfall'
  /** No API replica sees a free actor slot. */
  | 'noFreeActorSlots'
  /** Some worker has a free actor slot. Slots are counted by actors alone: an
   *  actor can still fail to place for lack of CPU or memory. */
  | 'freeActorSlots'
  /** No schedulable worker of a known class. */
  | 'noSchedulableWorkers'
  /** A node: some actor's snapshot upload is overdue. */
  | 'uploadsOverdue'
  /** A node: no overdue upload is counted. Snapshots with no storage
   *  destination are not counted, so this does not prove all are saved. */
  | 'noUploadsOverdue'
  | 'unknown'

/**
 * One number behind a verdict. resumeP95, resumeFailures and crashes summarize
 * the requested range; workersWithFreeActorSlots, overdueUploads and oldestUnsaved read now.
 */
export interface SubstrateFact {
  id: string
  /** Display text. */
  label: string
  unit: 'workers' | 'seconds' | 'percent' | 'count'
  /** ok has a value; empty means the series exist and recorded nothing here; unavailable says why. */
  state: 'ok' | 'empty' | 'unavailable'
  value?: number
  /**
   * Only on an ok fact that compares: workersWithFreeActorSlots, out of the
   * schedulable workers of known classes; schedulableWorkers (present only on a
   * workerShortfall verdict, listed first), out of the fewest desired over five
   * minutes.
   */
  of?: number
  reason?: string
  /** Only on an ok overdueUploads: Substrate's band keys with actors in them, worst first. */
  parts?: { label: string; value: number }[]
}

export interface SubstrateVerdict {
  state: SubstrateVerdictState
  /** Why the state is unknown, or what qualifies a known one (a shortfall that could not be judged). */
  reason?: string
  facts: SubstrateFact[]
}

export interface SubstrateHealth {
  /**
   * available: some chart has data or a refusal of its own; read each chart's
   * state. notFound: no chart has series for this subject in the range, which
   * does not say whether Substrate or the object exists. notOnNode,
   * notAttributable: a node's atelet series. error: every chart failed.
   */
  status: 'available' | 'notFound' | 'notOnNode' | 'notAttributable' | 'error'
  reason?: string
  /** The range served: 1h when the request named an unknown one. */
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
/** The verdict and the charts read one response, so they share one range. */
export const SUBSTRATE_RANGE: PrometheusTimeRange = '1h'

export function useSubstrateHealth(target: SubstrateTarget, enabled: boolean) {
  const range = SUBSTRATE_RANGE
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
  return { ...query, upgrade: feature.upgrade, blockedReason: feature.blockedReason, support: feature.support }
}
