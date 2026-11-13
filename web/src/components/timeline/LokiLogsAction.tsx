import { useCallback } from 'react'
import { useOpenLogs } from '@skyhook-io/k8s-ui'
import type { TimelineEvent } from '../../types'
import { useLokiHistoryReady } from '../../api/loki'

/**
 * "Logs (Loki)" on a Pod's Timeline card: opens the pod's History in the dock,
 * which works after the pod is gone. undefined when History can't be read.
 */
export function useLokiTimelineActions(): ((item: TimelineEvent) => React.ReactNode) | undefined {
  const ready = useLokiHistoryReady()
  const openLogs = useOpenLogs()
  const render = useCallback((item: TimelineEvent) => {
    if (item.kind !== 'Pod' || !item.namespace || (item.apiVersion && item.apiVersion !== 'v1')) return null
    return (
      <button type="button" className="btn-secondary px-2 py-0.5 text-xs" onClick={() => openLogs({ namespace: item.namespace, podName: item.name, containers: [], history: true })}>
        Logs (Loki)
      </button>
    )
  }, [openLogs])
  return ready ? render : undefined
}
