import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { Filter, ChevronDown } from 'lucide-react'
import { parseLogRange } from '../../utils/log-format'
import { triggerDownload } from '../../utils/download'
import { useLogBuffer } from './useLogBuffer'
import { useLogStream } from './useLogStream'
import { ContainerSelect, LogRangeSelect } from './LogToolbarSelects'
import { LogCore } from './LogCore'
import type { DownloadFormat } from './LogCore'
import type { WorkloadPodInfo } from '../../types'
import { useToast } from '../ui/Toast'

export interface WorkloadRawLog {
  pod: string
  container: string
  timestamp: string
  content: string
}

export interface WorkloadLogsFetchParams {
  container?: string
  tailLines?: number
  sinceSeconds?: number
}

export interface WorkloadLogsResult {
  pods: WorkloadPodInfo[]
  logs: WorkloadRawLog[]
}

export interface WorkloadLogsViewerProps {
  /** Workload name — used for the download filename */
  name: string
  /**
   * Called to fetch workload logs. Returns the pod list and merged log lines.
   * The component owns the log range / container filter controls and passes
   * the current params on every fetch.
   */
  fetchAll: (params: WorkloadLogsFetchParams) => Promise<WorkloadLogsResult>
  /**
   * If provided, the stream button is enabled.
   * Called to open an SSE connection for the whole workload.
   */
  createStream?: (params: WorkloadLogsFetchParams) => EventSource
  /** Force dark mode on the logs container (default: true) */
  forceDark?: boolean
}

const POD_COLORS = [
  'text-blue-400', 'text-green-400', 'text-yellow-400', 'text-purple-400',
  'text-pink-400', 'text-cyan-400', 'text-orange-400', 'text-lime-400',
]

export function WorkloadLogsViewer({ name, fetchAll, createStream, forceDark }: WorkloadLogsViewerProps) {
  const [selectedContainer, setSelectedContainer] = useState<string>('')
  const [pods, setPods] = useState<WorkloadPodInfo[]>([])
  const [selectedPods, setSelectedPods] = useState<Set<string>>(new Set())
  const [isLoading, setIsLoading] = useState(false)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const [showPodFilter, setShowPodFilter] = useState(false)
  const [logRange, setLogRange] = useState('100')
  const { showError, showSuccess } = useToast()

  const { tailLines, sinceSeconds } = parseLogRange(logRange)
  const { entries, append, set, clear } = useLogBuffer()
  const { isStreaming, startStreaming, stopStreaming } = useLogStream()

  const podColors = useMemo(() => {
    const m = new Map<string, string>()
    pods.forEach((pod, i) => m.set(pod.name, POD_COLORS[i % POD_COLORS.length]))
    return m
  }, [pods])

  const podsInitialized = useRef(false)

  const loadLogs = useCallback(async () => {
    setIsLoading(true)
    setFetchError(null)
    try {
      const result = await fetchAll({ container: selectedContainer || undefined, tailLines, sinceSeconds })

      setPods(result.pods)

      if (!podsInitialized.current && result.pods.length > 0) {
        podsInitialized.current = true
        setSelectedPods(new Set(result.pods.map(p => p.name)))
      }

      const colors = new Map<string, string>()
      result.pods.forEach((pod, i) => colors.set(pod.name, POD_COLORS[i % POD_COLORS.length]))

      set(result.logs.map(log => ({
        timestamp: log.timestamp,
        content: log.content,
        container: log.container,
        pod: log.pod,
        podColor: colors.get(log.pod),
      })))
    } catch (err) {
      console.error('Failed to fetch workload logs:', err)
      setFetchError(err instanceof Error ? err.message : 'Failed to fetch logs')
    } finally {
      setIsLoading(false)
    }
  }, [fetchAll, selectedContainer, tailLines, sinceSeconds, set])

  useEffect(() => { loadLogs() }, [loadLogs])
  useEffect(() => { stopStreaming() }, [selectedContainer, stopStreaming])

  const handleStartStreaming = useCallback(() => {
    if (!createStream) return
    startStreaming(
      () => createStream({ container: selectedContainer || undefined, tailLines: 50, sinceSeconds }),
      {
        onConnected: (data: any) => {
          if (data?.pods) {
            setPods(data.pods)
            if (selectedPods.size === 0) {
              setSelectedPods(new Set((data.pods as WorkloadPodInfo[]).map((p: WorkloadPodInfo) => p.name)))
            }
          }
        },
        onLog: (data: any) => {
          if (data?.pod && data.content !== undefined) {
            append({
              timestamp: data.timestamp || '',
              content: data.content || '',
              container: data.container || '',
              pod: data.pod || '',
              podColor: podColors.get(data.pod || ''),
            })
          }
        },
        onPodAdded: (data: any) => {
          if (data?.pods) {
            const newPods = data.pods as WorkloadPodInfo[]
            setPods(prev => {
              const existing = new Set(prev.map(p => p.name))
              const toAdd = newPods.filter(p => !existing.has(p.name))
              return toAdd.length > 0 ? [...prev, ...toAdd] : prev
            })
            setSelectedPods(prev => {
              const next = new Set(prev)
              newPods.forEach(p => next.add(p.name))
              return next
            })
          }
        },
        onPodRemoved: (data: any) => {
          if (data?.pod) {
            const removedName = data.pod as string
            setPods(prev => prev.filter(p => p.name !== removedName))
            // Don't remove from selectedPods — keep old log entries visible
            // while new pod logs start flowing in
          }
        },
      },
      'Workload log stream error',
    )
  }, [createStream, startStreaming, selectedContainer, sinceSeconds, append, podColors, selectedPods.size])

  const allContainers = useMemo(() => {
    const s = new Set<string>()
    pods.forEach(pod => pod.containers.forEach(c => s.add(c)))
    return Array.from(s)
  }, [pods])

  const togglePod = useCallback((podName: string) => {
    setSelectedPods(prev => {
      const next = new Set(prev)
      if (next.has(podName)) next.delete(podName)
      else next.add(podName)
      return next
    })
  }, [])

  const toggleAllPods = useCallback(() => {
    const allSelected = pods.every(p => selectedPods.has(p.name))
    setSelectedPods(allSelected ? new Set() : new Set(pods.map(p => p.name)))
  }, [selectedPods, pods])

  const filteredEntries = useMemo(
    () => entries.filter(e => !e.pod || selectedPods.has(e.pod)),
    [entries, selectedPods],
  )

  const downloadLogs = useCallback((format: DownloadFormat) => {
    let content: string
    let mime: string
    const filename = `${name}-logs.${format}`
    switch (format) {
      case 'json':
        content = JSON.stringify(filteredEntries.map(l => ({
          timestamp: l.timestamp, pod: l.pod, container: l.container, content: l.content,
        })), null, 2)
        mime = 'application/json'
        break
      case 'csv':
        content = 'timestamp,pod,container,content\n' + filteredEntries.map(l =>
          `${l.timestamp},${l.pod || ''},${l.container},"${l.content.replace(/"/g, '""')}"`)
          .join('\n')
        mime = 'text/csv'
        break
      default:
        content = filteredEntries.map(l => `${l.timestamp} [${l.pod}/${l.container}] ${l.content}`).join('\n')
        mime = 'text/plain'
    }
    try {
      triggerDownload(content, mime, filename)
      showSuccess('Log download started', `Saving ${filename}. Check your browser or desktop Downloads location.`)
    } catch (err) {
      showError('Failed to download logs', err instanceof Error ? err.message : 'Unknown download error')
    }
  }, [filteredEntries, name, showError, showSuccess])

  const toolbarExtra = (
    <>
      {/* Pod filter */}
      <div className="relative">
        <button
          onClick={() => setShowPodFilter(v => !v)}
          className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors ${
            showPodFilter ? 'bg-blue-600 text-theme-text-primary' : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
          }`}
        >
          <Filter className="w-3 h-3" />
          <span>{pods.filter(p => selectedPods.has(p.name)).length}/{pods.length} pods</span>
          <ChevronDown className="w-3 h-3" />
        </button>

        {showPodFilter && (
          <div className="absolute top-full left-0 mt-1 w-64 bg-theme-elevated border border-theme-border rounded-lg shadow-lg z-50 max-h-64 overflow-y-auto">
            <div className="p-2 border-b border-theme-border">
              <button onClick={toggleAllPods} className="text-xs text-blue-400 hover:text-blue-300">
                {pods.every(p => selectedPods.has(p.name)) ? 'Deselect all' : 'Select all'}
              </button>
            </div>
            {pods.map(pod => (
              <label key={pod.name} className="flex items-center gap-2 px-3 py-2 hover:bg-theme-hover">
                <input
                  type="checkbox"
                  checked={selectedPods.has(pod.name)}
                  onChange={() => togglePod(pod.name)}
                  className="w-3 h-3 rounded border-theme-border-light bg-theme-elevated text-blue-500 focus:ring-blue-500 focus:ring-offset-0"
                />
                <span className={`w-2 h-2 rounded-full ${podColors.get(pod.name)?.replace('text-', 'bg-')}`} />
                <span className="text-xs text-theme-text-primary truncate flex-1">{pod.name}</span>
                <span className={`text-xs ${pod.ready ? 'text-green-400' : 'text-yellow-400'}`}>
                  {pod.ready ? 'Ready' : 'Not Ready'}
                </span>
              </label>
            ))}
          </div>
        )}
      </div>

      <ContainerSelect
        containers={allContainers}
        value={selectedContainer}
        onChange={setSelectedContainer}
        includeAll
      />

      <LogRangeSelect
        value={logRange}
        onChange={setLogRange}
        lineOptions={[50, 100, 500, 1000]}
        tooltip="How many logs to load per pod — by line count or time range"
      />
    </>
  )

  return (
    <LogCore
      entries={filteredEntries}
      isLoading={isLoading}
      isStreaming={isStreaming}
      onStartStream={createStream ? handleStartStreaming : undefined}
      onStopStream={stopStreaming}
      onRefresh={loadLogs}
      onDownload={downloadLogs}
      onClear={clear}
      toolbarExtra={toolbarExtra}
      showPodName
      emptyMessage={pods.length === 0 ? 'No pods found' : 'No logs available'}
      errorMessage={fetchError}
      forceDark={forceDark}
    />
  )
}
