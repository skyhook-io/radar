import { useCallback } from 'react'
import { fetchJSON, createLogStream } from '../../api/client'
import { LogsViewer as SharedLogsViewer } from '@skyhook-io/k8s-ui'
import type { LogsFetchParams } from '@skyhook-io/k8s-ui'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useTheme } from '../../context/ThemeContext'
import { usePodHistory } from '../../api/loki'

interface LogsViewerProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
  /** Start streaming on mount. Default true — callers pass false for terminal (completed/failed) pods. */
  autoStream?: boolean
  /** Open on History (logs from Loki), e.g. for a pod that is gone. */
  history?: boolean
}

export function LogsViewer({ namespace, podName, containers, initialContainer, autoStream = true, history = false }: LogsViewerProps) {
  const fetchHistory = usePodHistory(namespace, podName)
  const desktopDownload = useDesktopDownload()
  const { theme } = useTheme()

  const fetchLogs = useCallback(async (params: LogsFetchParams) => {
    const query = new URLSearchParams()
    query.set('container', params.container)
    if (params.tailLines) query.set('tailLines', String(params.tailLines))
    if (params.sinceSeconds) query.set('sinceSeconds', String(params.sinceSeconds))
    if (params.previous) query.set('previous', 'true')
    const data = await fetchJSON<{ logs: { [container: string]: string } }>(
      `/pods/${namespace}/${podName}/logs?${query}`
    )
    return data.logs
  }, [namespace, podName])

  const makeStream = useCallback((params: Omit<LogsFetchParams, 'previous'>) => {
    return createLogStream(namespace, podName, params)
  }, [namespace, podName])

  return (
    <SharedLogsViewer
      namespace={namespace}
      podName={podName}
      containers={containers}
      initialContainer={initialContainer}
      fetchLogs={fetchLogs}
      createStream={makeStream}
      overrideDownload={desktopDownload}
      // Light logs on a dark app are never wanted: dark theme pins the palette,
      // light theme only sets where it starts and leaves the toggle available.
      forceDark={theme === 'dark' ? true : undefined}
      defaultDark={false}
      autoStream={autoStream && !history}
      fetchHistory={fetchHistory}
      initialMode={history ? 'history' : 'live'}
      // A pod opened on History is usually gone, and its lines are its last:
      // read the whole retention a read may cover.
      initialHistoryWindow={history ? '7d' : undefined}
    />
  )
}
