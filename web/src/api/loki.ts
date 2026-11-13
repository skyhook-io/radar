import { useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { FetchHistory, LogsHistoryResult } from '@skyhook-io/k8s-ui'
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
export function usePodHistory(namespace: string, podName: string): FetchHistory | undefined {
  const { support, guard } = useRadarFeature('lokiPodHistory')
  const status = useLokiStatus(support === 'supported')
  const ready = support === 'supported' && !!status.data?.connected && !status.data.historyReason
  const fetchHistory = useCallback<FetchHistory>(async ({ container, since }) => {
    const query = new URLSearchParams({ since })
    if (container) query.set('container', container)
    const data = await guard(() => fetchJSON<LokiPodLogs>(`/loki/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(podName)}/logs?${query}`))
    return { containers: data.containers, logs: data.logs, truncated: data.truncated, limit: data.limit, window: data.window } satisfies LogsHistoryResult
  }, [guard, namespace, podName])
  return ready ? fetchHistory : undefined
}
