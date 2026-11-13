import { useQuery } from '@tanstack/react-query'
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
