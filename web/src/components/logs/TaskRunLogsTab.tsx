import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { LogCore, useLogBuffer, parseLogLine, tektonNodeStatusFromConditions, type DownloadFormat } from '@skyhook-io/k8s-ui'
import { fetchJSON } from '../../api/client'
import { triggerDownload } from '@skyhook-io/k8s-ui/utils/download'
import { useDesktopDownload } from '../../hooks/useDesktopDownload'
import { useToast } from '../ui/Toast'

const POLL_INTERVAL_MS = 5000

interface TaskRunLogsTabProps {
  namespace: string
  resource: any
}

// A TaskRun has exactly one pod with one container per step (Tekton names
// them `step-<name>`), running sequentially — the opposite shape of the
// generic MultiPodLogsTab (built for N-pod workloads picking one pod at a
// time). This fetches every step's container in one call and combines them
// into a single sequential view in declared step order, labeling each line
// by step instead of making the user pick a container.
export function TaskRunLogsTab({ namespace, resource }: TaskRunLogsTabProps) {
  const podName = resource?.status?.podName as string | undefined
  const stepNames = useMemo(
    // Prefer the container name Tekton actually reports; the step-<name>
    // convention is only a fallback for an older/stripped status shape.
    () => ((resource?.status?.steps ?? []) as Array<{ name: string; container?: string }>).map((s) => s.container ?? `step-${s.name}`),
    [resource],
  )
  const { entries, set, clear } = useLogBuffer()
  const [isLoading, setIsLoading] = useState(false)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const desktopDownload = useDesktopDownload()
  const { showError, showSuccess } = useToast()

  // Guards against a stale response landing after a newer one — podName
  // changing (a different TaskRun/pod entirely) fires a fresh load() while
  // the previous pod's fetch may still be in flight; only the response for
  // the generation that's still current gets applied.
  const requestGenRef = useRef(0)

  const load = useCallback(async () => {
    if (!podName) return
    const gen = ++requestGenRef.current
    setIsLoading(true)
    setFetchError(null)
    try {
      const data = await fetchJSON<{ logs: Record<string, string> }>(`/pods/${namespace}/${podName}/logs`)
      if (gen !== requestGenRef.current) return
      const combined = stepNames.flatMap((container) => {
        const raw = data.logs?.[container]
        if (!raw) return []
        return raw.split('\n').filter(Boolean).map((line) => {
          const { timestamp, content } = parseLogLine(line)
          return { timestamp, content, container }
        })
      })
      set(combined)
    } catch (err) {
      if (gen !== requestGenRef.current) return
      setFetchError(err instanceof Error ? err.message : 'Failed to fetch logs')
    } finally {
      if (gen === requestGenRef.current) setIsLoading(false)
    }
  }, [namespace, podName, stepNames, set])

  // A pod change is a genuinely different TaskRun execution — clear stale
  // entries before the fresh fetch lands rather than leaving the previous
  // pod's logs on screen while loading (a plain manual refresh of the SAME
  // pod, via onRefresh below, intentionally leaves the buffer alone so the
  // view doesn't flash empty).
  useEffect(() => { clear() }, [podName, clear])
  useEffect(() => { load() }, [load])

  // Poll while the run hasn't settled — a step's live output otherwise never
  // reaches the tab until the user manually refreshes. Stops as soon as the
  // TaskRun reaches a terminal condition (or unmounts), then the effect
  // above's normal load() already covers the final snapshot.
  const taskRunStatus = tektonNodeStatusFromConditions(resource?.status?.conditions).status
  const isTerminal = taskRunStatus === 'succeeded' || taskRunStatus === 'failed' || taskRunStatus === 'skipped'
  useEffect(() => {
    if (isTerminal || !podName) return
    const id = window.setInterval(load, POLL_INTERVAL_MS)
    return () => window.clearInterval(id)
  }, [isTerminal, podName, load])

  const downloadLogs = useCallback((format: DownloadFormat) => {
    const filename = `${podName ?? 'taskrun'}-logs.${format}`
    let content: string
    let mime: string
    switch (format) {
      case 'json':
        content = JSON.stringify(entries.map((e) => ({ timestamp: e.timestamp, step: e.container, content: e.content })), null, 2)
        mime = 'application/json'
        break
      case 'csv':
        content = 'timestamp,step,content\n' + entries.map((e) =>
          `${e.timestamp},${e.container},"${e.content.replace(/"/g, '""')}"`).join('\n')
        mime = 'text/csv'
        break
      default:
        content = entries.map((e) => `${e.timestamp} [${e.container}] ${e.content}`).join('\n')
        mime = 'text/plain'
    }
    try {
      triggerDownload(content, mime, filename, desktopDownload)
      if (!desktopDownload) showSuccess('Log download started', `Saving ${filename}. Check your browser Downloads.`)
    } catch (err) {
      showError('Failed to download logs', err instanceof Error ? err.message : 'Unknown download error')
    }
  }, [entries, podName, desktopDownload, showError, showSuccess])

  if (!podName) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-theme-text-tertiary">
        No pod recorded for this TaskRun (it may have been garbage-collected after completion).
      </div>
    )
  }

  return (
    <LogCore
      entries={entries}
      isLoading={isLoading}
      isStreaming={false}
      onStopStream={() => {}}
      onRefresh={load}
      onDownload={downloadLogs}
      onClear={clear}
      showContainerName
      emptyMessage="No step logs available"
      errorMessage={fetchError}
    />
  )
}
