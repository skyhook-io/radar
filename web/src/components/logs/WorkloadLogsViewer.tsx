import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { useWorkloadLogs, createWorkloadLogStream } from '../../api/client'
import type { WorkloadPodInfo, WorkloadLogStreamEvent } from '../../types'
import { Play, Pause, Download, Search, X, ChevronDown, Terminal, RotateCcw, Filter } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import {
  formatLogTimestamp,
  getLogLevelColor,
  highlightSearchMatches,
  stripAnsi,
  ansiToHtml,
} from '../../utils/log-format'

interface LogLine {
  timestamp: string
  content: string
  container: string
  pod: string
}

interface WorkloadLogsViewerProps {
  kind: string
  namespace: string
  name: string
}

// Pod colors for visual distinction
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
  const [searchQuery, setSearchQuery] = useState('')
  const [showSearch, setShowSearch] = useState(false)
  const [showPodFilter, setShowPodFilter] = useState(false)
  const [tailLines, setTailLines] = useState(100)
  const [logLines, setLogLines] = useState<LogLine[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const [pods, setPods] = useState<WorkloadPodInfo[]>([])
  const [podColors, setPodColors] = useState<Map<string, string>>(new Map())

  const logContainerRef = useRef<HTMLDivElement>(null)
  const eventSourceRef = useRef<EventSource | null>(null)

  // Fetch initial logs (non-streaming)
  const { data: logsData, refetch, isLoading } = useWorkloadLogs(kind, namespace, name, {
    container: selectedContainer || undefined,
    tailLines,
  })

  // Get all unique containers across all pods
  const allContainers = useMemo(() => {
    const containers = new Set<string>()
    pods.forEach(pod => {
      pod.containers.forEach(c => containers.add(c))
    })
    return Array.from(containers)
  }, [pods])

  // Parse logs data into lines
  useEffect(() => {
    if (logsData) {
      const podsList = logsData.pods || []
      const logsList = logsData.logs || []

      setPods(podsList)

      // Assign colors to pods
      const colors = new Map<string, string>()
      podsList.forEach((pod, i) => {
        colors.set(pod.name, POD_COLORS[i % POD_COLORS.length])
      })
      setPodColors(colors)

      // Initialize selected pods to all pods
      if (selectedPods.size === 0 && podsList.length > 0) {
        setSelectedPods(new Set(podsList.map(p => p.name)))
      }

      // Convert logs to lines
      const lines = logsList.map(log => ({
        timestamp: log.timestamp,
        content: log.content,
        container: log.container,
        pod: log.pod,
      }))
      setLogLines(lines)
    }
  }, [logsData, selectedPods.size])

  // Auto-scroll to bottom
  useEffect(() => {
    if (autoScroll && logContainerRef.current) {
      logContainerRef.current.scrollTop = logContainerRef.current.scrollHeight
    }
  }, [logLines, autoScroll])

  // Handle scroll to detect if user scrolled up
  const handleScroll = useCallback(() => {
    if (!logContainerRef.current) return
    const { scrollTop, scrollHeight, clientHeight } = logContainerRef.current
    const isAtBottom = scrollHeight - scrollTop - clientHeight < 50
    setAutoScroll(isAtBottom)
  }, [])

  // Start streaming
  const startStreaming = useCallback(() => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
    }

    const es = createWorkloadLogStream(kind, namespace, name, {
      container: selectedContainer || undefined,
      tailLines: 50,
    })

    es.addEventListener('connected', (event) => {
      try {
        const data: WorkloadLogStreamEvent = JSON.parse(event.data)
        setIsStreaming(true)
        if (data.pods) {
          setPods(data.pods)
          // Assign colors to pods
          const colors = new Map<string, string>()
          data.pods.forEach((pod, i) => {
            colors.set(pod.name, POD_COLORS[i % POD_COLORS.length])
          })
          setPodColors(colors)
          // Initialize selected pods
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
          setLogLines(prev => [...prev, {
            timestamp: data.timestamp || '',
            content: data.content || '',
            container: data.container || '',
            pod: data.pod || '',
          }])
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
      console.error('Workload log stream error:', event)
      setIsStreaming(false)
      es.close()
    })

    eventSourceRef.current = es
  }, [kind, namespace, name, selectedContainer, selectedPods.size])

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

  // Select/deselect all pods
  const toggleAllPods = useCallback(() => {
    if (selectedPods.size === pods.length) {
      setSelectedPods(new Set())
    } else {
      setSelectedPods(new Set(pods.map(p => p.name)))
    }
  }, [selectedPods.size, pods])

  // Download logs
  const downloadLogs = useCallback(() => {
    const content = logLines
      .filter(l => selectedPods.has(l.pod))
      .map(l => `${l.timestamp} [${l.pod}/${l.container}] ${l.content}`)
      .join('\n')
    const blob = new Blob([content], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${name}-logs.txt`
    a.click()
    URL.revokeObjectURL(url)
  }, [logLines, name, selectedPods])

  // Filter logs by search and selected pods
  const filteredLines = useMemo(() => {
    let lines = logLines.filter(l => selectedPods.has(l.pod))
    if (searchQuery) {
      lines = lines.filter(l => l.content.toLowerCase().includes(searchQuery.toLowerCase()))
    }
    return lines
  }, [logLines, selectedPods, searchQuery])

  return (
    <div className="flex flex-col h-full bg-theme-base">
      {/* Toolbar */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-theme-border bg-theme-surface">
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

        {/* Stream toggle */}
        <button
          onClick={isStreaming ? stopStreaming : startStreaming}
          className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors ${
            isStreaming
              ? 'bg-green-600 text-theme-text-primary hover:bg-green-700'
              : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
          }`}
          title={isStreaming ? 'Stop streaming' : 'Start streaming'}
        >
          {isStreaming ? <Pause className="w-3 h-3" /> : <Play className="w-3 h-3" />}
          <span className="hidden sm:inline">{isStreaming ? 'Streaming' : 'Stream'}</span>
        </button>

        {/* Refresh button */}
        <button
          onClick={() => refetch()}
          disabled={isLoading || isStreaming}
          className="flex items-center gap-1.5 px-2 py-1.5 text-xs rounded bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50 disabled:cursor-not-allowed"
          title="Refresh logs"
        >
          <RotateCcw className={`w-3 h-3 ${isLoading ? 'animate-spin' : ''}`} />
        </button>

        {/* Tail lines selector */}
        <Tooltip content="Number of historical log lines to load per pod." position="bottom">
          <select
            value={tailLines}
            onChange={(e) => setTailLines(Number(e.target.value))}
            className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 pr-5 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
          >
            <option value={50}>50 lines</option>
            <option value={100}>100 lines</option>
            <option value={500}>500 lines</option>
            <option value={1000}>1000 lines</option>
          </select>
        </Tooltip>

        <div className="flex-1" />

        {/* Search toggle */}
        <button
          onClick={() => setShowSearch(!showSearch)}
          className={`p-1.5 rounded transition-colors ${
            showSearch ? 'bg-blue-600 text-theme-text-primary' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
          }`}
          title="Search logs"
        >
          <Search className="w-4 h-4" />
        </button>

        {/* Download */}
        <button
          onClick={downloadLogs}
          className="p-1.5 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated"
          title="Download logs"
        >
          <Download className="w-4 h-4" />
        </button>
      </div>

      {/* Search bar */}
      {showSearch && (
        <div className="flex items-center gap-2 px-3 py-2 border-b border-theme-border bg-theme-surface/50">
          <Search className="w-4 h-4 text-theme-text-secondary" />
          <input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="Search logs..."
            className="flex-1 bg-transparent text-theme-text-primary text-sm placeholder-theme-text-disabled focus:outline-none"
            autoFocus
          />
          {searchQuery && (
            <>
              <span className="text-xs text-theme-text-tertiary">
                {filteredLines.length} / {logLines.filter(l => selectedPods.has(l.pod)).length}
              </span>
              <button
                onClick={() => setSearchQuery('')}
                className="p-1 rounded text-theme-text-secondary hover:text-theme-text-primary"
              >
                <X className="w-3 h-3" />
              </button>
            </>
          )}
        </div>
      )}

      {/* Log content */}
      <div
        ref={logContainerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-auto font-mono text-xs"
        onClick={() => setShowPodFilter(false)}
      >
        {isLoading && logLines.length === 0 ? (
          <div className="flex items-center justify-center h-full text-theme-text-tertiary">
            <div className="flex items-center gap-2">
              <RotateCcw className="w-4 h-4 animate-spin" />
              <span>Loading logs...</span>
            </div>
          </div>
        ) : pods.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-2">
            <Terminal className="w-8 h-8" />
            <span>No pods found</span>
          </div>
        ) : filteredLines.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-2">
            <Terminal className="w-8 h-8" />
            <span>No logs available</span>
          </div>
        ) : (
          <div className="p-2">
            {filteredLines.map((line, i) => (
              <WorkloadLogLineItem
                key={i}
                line={line}
                searchQuery={searchQuery}
                podColor={podColors.get(line.pod) || 'text-theme-text-primary'}
              />
            ))}
          </div>
        )}
      </div>

      {/* Auto-scroll indicator */}
      {!autoScroll && (
        <button
          onClick={() => {
            setAutoScroll(true)
            if (logContainerRef.current) {
              logContainerRef.current.scrollTop = logContainerRef.current.scrollHeight
            }
          }}
          className="absolute bottom-4 right-4 px-3 py-1.5 bg-blue-600 text-theme-text-primary text-xs rounded-full shadow-lg hover:bg-blue-700"
        >
          Scroll to bottom
        </button>
      )}
    </div>
  )
}

// Individual log line component
function WorkloadLogLineItem({
  line,
  searchQuery,
  podColor,
}: {
  line: LogLine
  searchQuery: string
  podColor: string
}) {
  const levelColor = getLogLevelColor(stripAnsi(line.content))
  const content = searchQuery
    ? highlightSearchMatches(stripAnsi(line.content), searchQuery)
    : ansiToHtml(line.content)

  // Extract short pod name (last two segments after -)
  const shortPodName = line.pod.split('-').slice(-2).join('-')

  return (
    <div className="flex hover:bg-theme-surface/50 group leading-5">
      {line.timestamp && (
        <span className="text-theme-text-tertiary select-none pr-2 whitespace-nowrap">
          {formatLogTimestamp(line.timestamp)}
        </span>
      )}
      <span className={`${podColor} select-none pr-2 whitespace-nowrap min-w-[80px] max-w-[120px] truncate`} title={line.pod}>
        [{shortPodName}]
      </span>
      <span
        className={`whitespace-pre-wrap break-all ${levelColor}`}
        dangerouslySetInnerHTML={{ __html: content }}
      />
    </div>
  )
}
