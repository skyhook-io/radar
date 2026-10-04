import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'
import { WorkloadLogsViewer, type WorkloadLogsFetchParams, type WorkloadLogsResult } from '@skyhook-io/k8s-ui'
import { fetchJSON } from '../../api/client'
import { getApiBase, getCredentialsMode } from '../../api/config'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useTheme } from '../../context/ThemeContext'

function logsPath(namespace: string, name: string) {
  return `/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/logs`
}

function query(params: WorkloadLogsFetchParams, tailDefault?: number) {
  const q = new URLSearchParams()
  if (params.container) q.set('container', params.container)
  const tail = params.tailLines ?? tailDefault
  if (tail) q.set('tailLines', String(tail))
  if (params.sinceSeconds) q.set('sinceSeconds', String(params.sinceSeconds))
  const s = q.toString()
  return s ? `?${s}` : ''
}

/**
 * Logs merged from every instance Pod of a CloudNativePG Cluster. `?pod=`
 * preselects one instance (the fleet's "Logs" action and "Open instance logs").
 */
export function CNPGClusterLogs({ namespace, name }: { namespace: string; name: string }) {
  const [searchParams] = useSearchParams()
  const pod = searchParams.get('pod')
  const desktopDownload = useDesktopDownload()
  const { theme } = useTheme()

  const fetchAll = useCallback(
    (params: WorkloadLogsFetchParams) =>
      fetchJSON<WorkloadLogsResult>(`${logsPath(namespace, name)}${query(params)}`, { signal: params.signal }),
    [namespace, name],
  )
  const createStream = useCallback(
    (params: WorkloadLogsFetchParams) =>
      new EventSource(`${getApiBase()}${logsPath(namespace, name)}/stream${query(params, 50)}`, {
        withCredentials: getCredentialsMode() === 'include',
      }),
    [namespace, name],
  )

  return (
    <div className="h-full">
      <WorkloadLogsViewer
        key={pod ?? ''}
        name={name}
        fetchAll={fetchAll}
        createStream={createStream}
        overrideDownload={desktopDownload}
        forceDark={theme === 'dark' ? true : undefined}
        // Light logs on a dark app are never wanted: dark theme pins the palette,
        // light theme only sets where it starts and leaves the toggle available.
        defaultDark={false}
        initialPods={pod ? [pod] : undefined}
      />
    </div>
  )
}
