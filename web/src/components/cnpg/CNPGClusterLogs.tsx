import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'
import { WorkloadLogsViewer, type WorkloadLogsFetchParams, type WorkloadLogsResult } from '@skyhook-io/k8s-ui'
import { fetchJSON } from '../../api/client'
import { getApiBase, getCredentialsMode } from '../../api/config'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useTheme } from '../../context/ThemeContext'
import { CNPGIntervalBanner, useCNPGIntervalParams } from './CNPGTrends'

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

// An interval is read from its start with both bounds applied server-side; the
// viewer's line-count and since selectors do not apply to it.
function intervalQuery(params: WorkloadLogsFetchParams, since: string, until: string) {
  const q = new URLSearchParams({ sinceTime: since, untilTime: until })
  if (params.container) q.set('container', params.container)
  return `?${q.toString()}`
}

/**
 * Logs merged from every instance Pod of a CloudNativePG Cluster. `?pod=`
 * preselects one instance (the fleet's "Logs" action and "Open instance logs");
 * `?since=&until=` bounds them to an interval selected on a Runtime trend.
 */
export function CNPGClusterLogs({ namespace, name }: { namespace: string; name: string }) {
  const [searchParams] = useSearchParams()
  const pod = searchParams.get('pod')
  const interval = useCNPGIntervalParams()
  const desktopDownload = useDesktopDownload()
  const { theme } = useTheme()
  const since = interval?.since
  const until = interval?.until

  const fetchAll = useCallback(
    (params: WorkloadLogsFetchParams) =>
      fetchJSON<WorkloadLogsResult>(`${logsPath(namespace, name)}${since && until ? intervalQuery(params, since, until) : query(params)}`, { signal: params.signal }),
    [namespace, name, since, until],
  )
  const createStream = useCallback(
    (params: WorkloadLogsFetchParams) =>
      new EventSource(`${getApiBase()}${logsPath(namespace, name)}/stream${query(params, 50)}`, {
        withCredentials: getCredentialsMode() === 'include',
      }),
    [namespace, name],
  )

  return (
    <div className="flex h-full flex-col">
      {interval && (
        <div className="px-3 pt-3">
          <CNPGIntervalBanner
            since={interval.since}
            until={interval.until}
            note="Selected on a Runtime trend. Lines from the start of the interval, up to 64 KiB per instance; the line-count selector does not apply and streaming is off."
            onClear={interval.clear}
          />
        </div>
      )}
      <div className="min-h-0 flex-1">
        <WorkloadLogsViewer
          key={`${pod ?? ''}|${since ?? ''}|${until ?? ''}`}
          name={name}
          fetchAll={fetchAll}
          createStream={interval ? undefined : createStream}
          overrideDownload={desktopDownload}
          forceDark={theme === 'dark' ? true : undefined}
          initialPods={pod ? [pod] : undefined}
        />
      </div>
    </div>
  )
}
