import { useEffect } from 'react'
import { clsx } from 'clsx'
import { Terminal } from 'lucide-react'
import { usePodLogs, useResource } from '../../api/client'
import type { ResourceRef } from '../../types'
import { LogsViewer } from './LogsViewer'
import { PodLogsUnavailable } from './PodLogsUnavailable'

export function MultiPodLogsTab({
  pods,
  namespace,
  selectedPod,
  onSelectPod,
  initialContainer,
  emptyMessage = 'No pods available',
}: {
  pods: ResourceRef[]
  namespace: string
  selectedPod: string | null
  onSelectPod: (name: string | null) => void
  initialContainer?: string | null
  emptyMessage?: string
}) {
  useEffect(() => {
    if (pods.length > 0 && !selectedPod) {
      onSelectPod(pods[0].name)
    }
  }, [pods, selectedPod, onSelectPod])

  const podNamespace = pods.find((p) => p.name === selectedPod)?.namespace || namespace

  // Fetch container list for the selected pod
  const { data: logsData, error: containersError } = usePodLogs(podNamespace, selectedPod || '', {
    tailLines: 1,
  })
  const containers = logsData?.containers || []

  // A terminated pod (common for Job/CronJob children) has nothing to follow —
  // only stream live ones. Wait for the pod to load before deciding so we don't
  // briefly auto-stream a completed pod while its phase is still unknown.
  const { data: selectedPodResource } = useResource<any>('Pod', podNamespace, selectedPod || '')
  const phase = selectedPodResource?.status?.phase
  const autoStream = !!phase && phase !== 'Succeeded' && phase !== 'Failed'

  // A selected Pod that left the list (deleted, replaced, or not in a newer
  // list) stays selectable so its loaded lines aren't thrown away, but is
  // marked, and the other Pods stay one click away.
  const unlisted = selectedPod && !pods.some((p) => p.name === selectedPod) ? selectedPod : null
  const chips = unlisted ? [...pods, { kind: 'Pod', namespace: podNamespace, name: unlisted }] : pods

  if (pods.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary">
        <Terminal className="w-12 h-12 mb-4 opacity-50" />
        <p>{emptyMessage}</p>
      </div>
    )
  }

  return (
    <div className="h-full flex flex-col">
      {chips.length > 1 && (
        <div className="shrink-0 border-b border-theme-border bg-theme-surface/50 px-4 py-2 flex gap-2 overflow-x-auto">
          {chips.map((pod) => (
            <button
              key={pod.name}
              onClick={() => onSelectPod(pod.name)}
              aria-pressed={selectedPod === pod.name}
              className={clsx(
                'px-3 py-1.5 text-sm rounded-lg whitespace-nowrap transition-colors',
                selectedPod === pod.name
                  ? 'selection-strong selection-text'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover',
              )}
            >
              {pod.name.length > 40 ? '...' + pod.name.slice(-37) : pod.name}
              {pod.name === unlisted && <span className="ml-1.5 text-xs opacity-75">· no longer listed</span>}
            </button>
          ))}
        </div>
      )}
      {selectedPod && containersError && !logsData && (
        <PodLogsUnavailable podName={selectedPod} error={containersError} />
      )}
      {selectedPod && containers.length > 0 && (
        <div className="flex-1 min-h-0">
          <LogsViewer
            key={selectedPod}
            namespace={podNamespace}
            podName={selectedPod}
            containers={containers}
            initialContainer={initialContainer || undefined}
            autoStream={autoStream}
          />
        </div>
      )}
    </div>
  )
}
