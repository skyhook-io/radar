import { useCallback } from 'react'
import { fetchJSON, createWorkloadLogStream } from '../../api/client'
import { WorkloadLogsViewer as SharedWorkloadLogsViewer } from '@skyhook-io/k8s-ui'
import type { WorkloadLogsFetchParams, WorkloadLogsResult } from '@skyhook-io/k8s-ui'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useTheme } from '../../context/ThemeContext'

interface WorkloadLogsViewerProps {
  kind: string
  namespace: string
  name: string
  /** Start streaming on mount. Default true — workload logs are aggregated from live pods. */
  snapshotOnly?: boolean
  role?: string
  autoStream?: boolean
}

export function WorkloadLogsViewer({ kind, namespace, name, autoStream = true, snapshotOnly = false, role }: WorkloadLogsViewerProps) {
  const desktopDownload = useDesktopDownload()
  const { theme } = useTheme()

  const fetchAll = useCallback(async (params: WorkloadLogsFetchParams): Promise<WorkloadLogsResult> => {
    const query = new URLSearchParams()
    if (params.container) query.set('container', params.container)
    if (role) query.set('role', role)
    if (params.tailLines) query.set('tailLines', String(params.tailLines))
    if (params.sinceSeconds) query.set('sinceSeconds', String(params.sinceSeconds))
    const qs = query.toString()
    const data = await fetchJSON<WorkloadLogsResult>(
      `${snapshotOnly ? `/jobsets/${namespace}/${name}` : `/workloads/${kind}/${namespace}/${name}`}/logs${qs ? `?${qs}` : ''}`, { signal: params.signal }
    )
    return data
  }, [kind, namespace, name, snapshotOnly, role])

  const makeStream = useCallback((params: WorkloadLogsFetchParams) => {
    return createWorkloadLogStream(kind, namespace, name, params)
  }, [kind, namespace, name])

  return (
    <SharedWorkloadLogsViewer
      name={name}
      fetchAll={fetchAll}
      createStream={snapshotOnly ? undefined : makeStream}
      overrideDownload={desktopDownload}
      forceDark={theme === 'dark' ? true : undefined}
      autoStream={autoStream}
    />
  )
}
