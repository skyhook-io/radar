import { useCallback } from 'react'
import { fetchJSON, createLogStream } from '../../api/client'
import { LogsViewer as SharedLogsViewer } from '@skyhook-io/k8s-ui'
import type { LogsFetchParams } from '@skyhook-io/k8s-ui'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useTheme } from '../../context/ThemeContext'

interface LogsViewerProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
}

export function LogsViewer({ namespace, podName, containers, initialContainer }: LogsViewerProps) {
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
      forceDark={theme === 'dark' ? true : undefined}
    />
  )
}
