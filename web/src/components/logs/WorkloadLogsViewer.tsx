import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { useWorkloadLogs, createWorkloadLogStream } from '../../api/client'
import type { WorkloadPodInfo, WorkloadLogStreamEvent } from '../../types'
import { ChevronDown, Filter } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { parseLogRange, handleSSEError } from '../../utils/log-format'
import { useLogBuffer } from './useLogBuffer'
import { LogCore, type DownloadFormat } from './LogCore'

interface WorkloadLogsViewerProps {
  kind: string
  namespace: string
  name: string
}

const POD_COLORS = [
  'text-blue-400',
  'text-green-400',
  'text-yellow-400',
  'text-purple-400',
  'text-pink-400',
  'text-cyan-400',
  'text-orange-400',
  'text-lime-400',
]

export function WorkloadLogsViewer({ kind, namespace, name }: WorkloadLogsViewerProps) {
  const [selectedContainer, setSelectedContainer] = useState<string>('')
  const [selectedPods, setSelectedPods] = useState<Set<string>>(new Set())
  const [isStreaming, setIsStreaming] = useState(false)
  const [showPodFilter, setShowPodFilter] = useState(false)
  const [logRange, setLogRange] = useState('100')  // lines:N or since:N
  const { tailLines, sinceSeconds } = parseLogRange(logRange)
  const [pods, setPods] = useState<WorkloadPodInfo[]>([])
  const [podColors, setPodColors] = useState<Map<string, string>>(new Map())

  const eventSourceRef = useRef<EventSource | null>(null)
  const podColorsRef = useRef(podColors)
  podColorsRef.current = podColors
  const { entries, append, set, clear } = useLogBuffer()

  // Fetch initial logs (non-streaming)
  const { data: logsData, refetch, isLoading } = useWorkloadLogs(kind, namespace, name, {
    container: selectedContainer || undefined,
    tailLines,
    sinceSeconds,
  })

  // Get all unique containers across all pods
  const allContainers = useMemo(() => {
    const containers = new Set<string>()
    pods.forEach(pod => {
      pod.containers.forEach(c => containers.add(c))
    })
    return Array.from(containers)
  }, [pods])

  // Parse logs data into entries
  const podsInitialized = useRef(false)
  useEffect(() => {
    if (logsData) {
      const podsList = logsData.pods || []
      const logsList = logsData.logs || []

      setPods(podsList)

      const colors = new Map<string, string>()
      podsList.forEach((pod, i) => {
        colors.set(pod.name, POD_COLORS[i % POD_COLORS.length])
      })
      setPodColors(colors)

      if (!podsInitialized.current && podsList.length > 0) {
        podsInitialized.current = true
        setSelectedPods(new Set(podsList.map(p => p.name)))
      }

      const lines = logsList.map(log => ({
        timestamp: log.timestamp,
        content: log.content,
        container: log.container,
        pod: log.pod,
        podColor: colors.get(log.pod),
      }))
      set(lines)
    }
  }, [logsData, set])

  // Start streaming
  const startStreaming = useCallback(() => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
    }

    const es = createWorkloadLogStream(kind, namespace, name, {
      container: selectedContainer || undefined,
      tailLines: 50,
      sinceSeconds,
    })

    es.addEventListener('connected', (event) => {
      try {
        const data: WorkloadLogStreamEvent = JSON.parse(event.data)
        setIsStreaming(true)
        if (data.pods) {
          setPods(data.pods)
          const colors = new Map<string, string>()
          data.pods.forEach((pod, i) => {
            colors.set(pod.name, POD_COLORS[i % POD_COLORS.length])
          })
          setPodColors(colors)
          if (selectedPods.size === 0) {
            setSelectedPods(new Set(data.pods.map(p => p.name)))
          }
        }
      } catch (e) {
        console.error('Failed to parse connected event:', e)
      }
    })

    es.addEventListener('log', (event) => {
      try {
        const data: WorkloadLogStreamEvent = JSON.parse(event.data)
        if (data.pod && data.content !== undefined) {
          append({
            timestamp: data.timestamp || '',
            content: data.content || '',
            container: data.container || '',
            pod: data.pod || '',
            podColor: podColorsRef.current.get(data.pod || ''),
          })
        }
      } catch (e) {
        console.error('Failed to parse log event:', e)
      }
    })

    es.addEventListener('pod_added', (event) => {
      try {
        const data: WorkloadLogStreamEvent = JSON.parse(event.data)
        if (data.pods && data.pods.length > 0) {
          const newPod = data.pods[0]
          setPods(prev => [...prev, newPod])
          setSelectedPods(prev => new Set([...prev, newPod.name]))
          setPodColors(prev => {
            const newColors = new Map(prev)
            newColors.set(newPod.name, POD_COLORS[prev.size % POD_COLORS.length])
            return newColors
          })
        }
      } catch (e) {
        console.error('Failed to parse pod_added event:', e)
      }
    })

    es.addEventListener('pod_removed', (event) => {
      try {
        const data: WorkloadLogStreamEvent = JSON.parse(event.data)
        if (data.pod) {
          setPods(prev => prev.filter(p => p.name !== data.pod))
          setSelectedPods(prev => {
            const newSet = new Set(prev)
            newSet.delete(data.pod!)
            return newSet
          })
        }
      } catch (e) {
        console.error('Failed to parse pod_removed event:', e)
      }
    })

    es.addEventListener('end', () => {
      setIsStreaming(false)
    })

    es.addEventListener('error', (event) => {
      handleSSEError(event, 'Workload log stream error', () => { setIsStreaming(false); es.close() })
    })

    eventSourceRef.current = es
  }, [kind, namespace, name, selectedContainer, selectedPods.size, sinceSeconds, append])

  // Stop streaming
  const stopStreaming = useCallback(() => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
      eventSourceRef.current = null
    }
    setIsStreaming(false)
  }, [])

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close()
      }
    }
  }, [])

  // Stop streaming when container changes
  useEffect(() => {
    stopStreaming()
  }, [selectedContainer, stopStreaming])

  // Toggle pod selection
  const togglePod = useCallback((podName: string) => {
    setSelectedPods(prev => {
      const newSet = new Set(prev)
      if (newSet.has(podName)) {
        newSet.delete(podName)
      } else {
        newSet.add(podName)
      }
      return newSet
    })
  }, [])

  const toggleAllPods = useCallback(() => {
    if (selectedPods.size === pods.length) {
      setSelectedPods(new Set())
    } else {
      setSelectedPods(new Set(pods.map(p => p.name)))
    }
  }, [selectedPods.size, pods])

  // Filter entries by selected pods
  const filteredEntries = useMemo(() => {
    return entries.filter(e => !e.pod || selectedPods.has(e.pod))
  }, [entries, selectedPods])

  // Download logs
  const downloadLogs = useCallback((format: DownloadFormat) => {
    let content: string
    let mime: string
    const ext = format
    switch (format) {
      case 'json':
        content = JSON.stringify(filteredEntries.map(l => ({
          timestamp: l.timestamp, pod: l.pod, container: l.container, content: l.content,
        })), null, 2)
        mime = 'application/json'
        break
      case 'csv':
        content = 'timestamp,pod,container,content\n' + filteredEntries.map(l =>
          `${l.timestamp},${l.pod || ''},${l.container},"${l.content.replace(/"/g, '""')}"`
        ).join('\n')
        mime = 'text/csv'
        break
      default:
        content = filteredEntries.map(l => `${l.timestamp} [${l.pod}/${l.container}] ${l.content}`).join('\n')
        mime = 'text/plain'
    }
    const blob = new Blob([content], { type: mime })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${name}-logs.${ext}`
    a.click()
    URL.revokeObjectURL(url)
  }, [filteredEntries, name])

  const toolbarExtra = (
    <>
      {/* Pod filter */}
      <div className="relative">
        <button
          onClick={() => setShowPodFilter(!showPodFilter)}
          className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors ${
            showPodFilter
              ? 'bg-blue-600 text-theme-text-primary'
              : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
          }`}
        >
          <Filter className="w-3 h-3" />
          <span>{selectedPods.size}/{pods.length} pods</span>
          <ChevronDown className="w-3 h-3" />
        </button>

        {showPodFilter && (
          <div className="absolute top-full left-0 mt-1 w-64 bg-theme-elevated border border-theme-border rounded-lg shadow-lg z-50 max-h-64 overflow-y-auto">
            <div className="p-2 border-b border-theme-border">
              <button
                onClick={toggleAllPods}
                className="text-xs text-blue-400 hover:text-blue-300"
              >
                {selectedPods.size === pods.length ? 'Deselect all' : 'Select all'}
              </button>
            </div>
            {pods.map(pod => (
              <label
                key={pod.name}
                className="flex items-center gap-2 px-3 py-2 hover:bg-theme-hover cursor-pointer"
              >
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

      {/* Container selector */}
      {allContainers.length > 1 && (
        <div className="relative">
          <select
            value={selectedContainer}
            onChange={(e) => setSelectedContainer(e.target.value)}
            className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 pr-6 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
          >
            <option value="">All containers</option>
            {allContainers.map(c => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
          <ChevronDown className="absolute right-1.5 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-secondary pointer-events-none" />
        </div>
      )}

      {/* Log range selector */}
      <Tooltip content="How many logs to load per pod — by line count or time range" position="bottom">
        <select
          value={logRange}
          onChange={(e) => setLogRange(e.target.value)}
          className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 pr-5 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
        >
          <optgroup label="Lines">
            <option value="50">50 lines</option>
            <option value="100">100 lines</option>
            <option value="500">500 lines</option>
            <option value="1000">1,000 lines</option>
          </optgroup>
          <optgroup label="Time">
            <option value="since:60">Last 1 min</option>
            <option value="since:300">Last 5 min</option>
            <option value="since:900">Last 15 min</option>
            <option value="since:1800">Last 30 min</option>
            <option value="since:3600">Last 1 hour</option>
          </optgroup>
        </select>
      </Tooltip>
    </>
  )

  return (
    <LogCore
      entries={filteredEntries}
      isLoading={isLoading}
      isStreaming={isStreaming}
      onStartStream={startStreaming}
      onStopStream={stopStreaming}
      onRefresh={() => refetch()}
      onDownload={downloadLogs}
      onClear={clear}
      toolbarExtra={toolbarExtra}
      showPodName
      emptyMessage={pods.length === 0 ? 'No pods found' : 'No logs available'}
    />
  )
}
