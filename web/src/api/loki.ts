import { useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { FetchHistory, LogsHistoryResult, WorkloadLogsResult } from '@skyhook-io/k8s-ui'
import { fetchJSON, useRadarFeature } from './client'
import { shouldRetryRadarQuery } from './radarFeatures'

export interface LokiStreamLabels { namespace: string; pod: string; container: string; job?: string }

// GET /api/loki/status. Never carries a header value or a full URL.
export interface LokiStatus {
  configured: boolean
  connected: boolean
  discovering: boolean
  address?: string
  source?: 'manual' | 'discovered'
  labels?: LokiStreamLabels
  historyReason?: string
  error?: string
}

const POLL_DISCOVERING_MS = 2_000
const POLL_IDLE_MS = 60_000

export function lokiStatusRefetchInterval(status: LokiStatus | undefined): number {
  if (!status) return POLL_IDLE_MS
  const settled = status.connected || !!status.error || !status.configured
  return status.discovering || !settled ? POLL_DISCOVERING_MS : POLL_IDLE_MS
}

export function useLokiStatus(enabled = true) {
  const { support, guard, gatedKey } = useRadarFeature('lokiConnection')
  const query = useQuery<LokiStatus>({
    queryKey: ['loki-status', ...gatedKey],
    enabled,
    queryFn: ({ signal }) => guard(() => fetchJSON<LokiStatus>('/loki/status', signal)),
    staleTime: 30_000,
    refetchInterval: (q) => lokiStatusRefetchInterval(q.state.data),
    retry: shouldRetryRadarQuery,
  })
  return { ...query, support }
}

/** "namespace, pod, container, job job_name": the stream labels Radar reads. */
export function describeLokiLabels(l: LokiStreamLabels): string {
  return [l.namespace, l.pod, l.container, l.job && `job ${l.job}`].filter(Boolean).join(', ')
}

interface LokiPodLogs { containers: string[]; logs: Record<string, string>; truncated: boolean; limit: number; window: { start: string; end: string } }

/**
 * The History reader for one pod, or undefined when the connected Radar can't
 * serve it or Loki isn't connected with namespace and pod stream labels.
 */
/** Whether this Radar can read pod History from Loki now. */
export function useLokiHistoryReady(): boolean {
  const { support } = useRadarFeature('lokiPodHistory')
  const status = useLokiStatus(support === 'supported')
  return support === 'supported' && !!status.data?.connected && !status.data.historyReason
}

export function usePodHistory(namespace: string, podName: string): FetchHistory | undefined {
  const { guard } = useRadarFeature('lokiPodHistory')
  const ready = useLokiHistoryReady()
  const fetchHistory = useCallback<FetchHistory>(async ({ container, since }) => {
    const query = new URLSearchParams({ since })
    if (container) query.set('container', container)
    const data = await guard(() => fetchJSON<LokiPodLogs>(`/loki/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(podName)}/logs?${query}`))
    return { containers: data.containers, logs: data.logs, truncated: data.truncated, limit: data.limit, window: data.window } satisfies LogsHistoryResult
  }, [guard, namespace, podName])
  return ready ? fetchHistory : undefined
}

/** Whether this Radar can read finished Job runs from Loki now. */
export function useLokiJobRunsReady(): boolean {
  const { support } = useRadarFeature('lokiJobRuns')
  const status = useLokiStatus(support === 'supported')
  return support === 'supported' && !!status.data?.connected && !status.data.historyReason
}

interface LokiJobLogs {
  pods: WorkloadLogsResult['pods']
  logs: WorkloadLogsResult['logs']
  notice?: string
  truncated: boolean
  limit: number
  window: { start: string; end: string }
}

// The key Radar's history endpoints use when Loki has no container label.
const ALL_CONTAINERS = '(all containers)'

const timeOf = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })

/**
 * A Job run whose pods are gone reads from Loki instead, when it can (`ready`),
 * keeping to `container` when one is chosen. Anything else, or a Loki without
 * the run's lines, keeps Kubernetes' answer; a Loki that failed says so.
 */
export async function withLokiRunFallback(kind: string, namespace: string, name: string, data: WorkloadLogsResult, ready: boolean, signal?: AbortSignal, container?: string): Promise<WorkloadLogsResult> {
  if (!ready || data.emptyReason !== 'pods-gone' || !['job', 'jobs'].includes(kind.toLowerCase())) return data
  let run: LokiJobLogs
  try {
    run = await fetchJSON<LokiJobLogs>(`/loki/jobs/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/logs`, signal)
  } catch (err) {
    if (signal?.aborted) throw err
    // Loki is the logging system the message points to: say why it didn't answer.
    return { ...data, notice: `Couldn't read this run from Loki: ${err instanceof Error ? err.message : String(err)}` }
  }
  // Without a container label every line is the whole pod's: keep them all.
  const wholePod = run.logs.length > 0 && run.logs.every(l => l.container === ALL_CONTAINERS)
  const logs = container && !wholePod ? run.logs.filter(l => l.container === container) : run.logs
  if (logs.length === 0) return data
  const notice = [
    `From Loki, ${timeOf(run.window.start)}–${timeOf(run.window.end)}`,
    container && wholePod && 'all containers (Loki has no container label)',
    run.truncated && `the newest ${run.limit.toLocaleString()} lines (the limit; older lines may be missing)`,
    run.notice,
  ].filter(Boolean).join('. ')
  return { pods: run.pods, logs, notice }
}
