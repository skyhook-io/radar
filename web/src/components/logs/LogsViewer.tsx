import { useState, useEffect, useRef, useCallback } from 'react'
import { usePodLogs, createLogStream, type LogStreamEvent } from '../../api/client'
import { Play, Pause, Download, Search, X, ChevronDown, Terminal, RotateCcw } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import {
  formatLogTimestamp,
  getLogLevelColor,
  highlightSearchMatches,
  parseLogLine,
  escapeHtml,
} from '../../utils/log-format'

interface LogLine {
  timestamp: string
  content: string
  container: string
}

interface LogsViewerProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
}

export function LogsViewer({ namespace, podName, containers, initialContainer }: LogsViewerProps) {
  const [selectedContainer, setSelectedContainer] = useState(initialContainer || containers[0] || '')
  const [isStreaming, setIsStreaming] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [showSearch, setShowSearch] = useState(false)
  const [tailLines, setTailLines] = useState(500)
  const [logLines, setLogLines] = useState<LogLine[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const [showPrevious, setShowPrevious] = useState(false)

  const logContainerRef = useRef<HTMLDivElement>(null)
  const eventSourceRef = useRef<EventSource | null>(null)

  // Fetch initial logs (non-streaming)
  const { data: logsData, refetch, isLoading } = usePodLogs(namespace, podName, {
    container: selectedContainer,
    tailLines,
    previous: showPrevious,
  })

  // Parse logs data into lines
  useEffect(() => {
    if (logsData?.logs && selectedContainer) {
      const logContent = logsData.logs[selectedContainer] || ''
      const lines = logContent.split('\n').filter(Boolean).map(line => {
        const { timestamp, content } = parseLogLine(line)
        return { timestamp, content, container: selectedContainer }
      })
      setLogLines(lines)
    }
  }, [logsData, selectedContainer])

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

    const es = createLogStream(namespace, podName, {
      container: selectedContainer,
      tailLines: 100,
    })

    es.addEventListener('connected', () => {
      setIsStreaming(true)
    })

    es.addEventListener('log', (event) => {
      try {
        const data: LogStreamEvent['data'] = JSON.parse(event.data)
        setLogLines(prev => [...prev, {
          timestamp: data.timestamp || '',
          content: data.content || '',
          container: data.container || selectedContainer,
        }])
      } catch (e) {
        console.error('Failed to parse log event:', e)
      }
    })

    es.addEventListener('end', () => {
      setIsStreaming(false)
    })

    es.addEventListener('error', (event) => {
      console.error('Log stream error:', event)
      setIsStreaming(false)
      es.close()
    })

    eventSourceRef.current = es
  }, [namespace, podName, selectedContainer])

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

  // Download logs
  const downloadLogs = useCallback(() => {
    const content = logLines.map(l => `${l.timestamp} ${l.content}`).join('\n')
    const blob = new Blob([content], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${podName}-${selectedContainer}-logs.txt`
    a.click()
    URL.revokeObjectURL(url)
  }, [logLines, podName, selectedContainer])

  // Filter logs by search
  const filteredLines = searchQuery
    ? logLines.filter(l => l.content.toLowerCase().includes(searchQuery.toLowerCase()))
    : logLines

  return (
    <div className="flex flex-col h-full bg-theme-base">
      {/* Toolbar */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-theme-border bg-theme-surface">
        {/* Container selector */}
        {containers.length > 1 && (
          <div className="relative">
            <select
              value={selectedContainer}
              onChange={(e) => setSelectedContainer(e.target.value)}
              className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 pr-6 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
            >
              {containers.map(c => (
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

        {/* Previous logs toggle */}
        <Tooltip content="Show logs from the pod's previous instance (if it was restarted). Useful for troubleshooting crashed containers." position="bottom">
          <label className="flex items-center gap-1.5 text-xs text-theme-text-secondary cursor-pointer">
            <input
              type="checkbox"
              checked={showPrevious}
              onChange={(e) => setShowPrevious(e.target.checked)}
              className="w-3 h-3 rounded border-theme-border-light bg-theme-elevated text-blue-500 focus:ring-blue-500 focus:ring-offset-0"
            />
            <span className="border-b border-dotted border-theme-text-tertiary">Previous</span>
          </label>
        </Tooltip>

        {/* Tail lines selector */}
        <Tooltip content="Number of historical log lines to load. Larger values may be slower to fetch." position="bottom">
          <select
            value={tailLines}
            onChange={(e) => setTailLines(Number(e.target.value))}
            className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 pr-5 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
          >
            <option value={100}>100 lines</option>
            <option value={500}>500 lines</option>
            <option value={1000}>1000 lines</option>
            <option value={5000}>5000 lines</option>
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
            onKeyDown={(e) => { if (e.key === 'Escape') { setShowSearch(false); setSearchQuery('') } }}
            placeholder="Search logs..."
            className="flex-1 bg-transparent text-theme-text-primary text-sm placeholder-theme-text-disabled focus:outline-none"
            autoFocus
          />
          {searchQuery && (
            <>
              <span className="text-xs text-theme-text-tertiary">
                {filteredLines.length} / {logLines.length}
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
      >
        {isLoading && logLines.length === 0 ? (
          <div className="flex items-center justify-center h-full text-theme-text-tertiary">
            <div className="flex items-center gap-2">
              <RotateCcw className="w-4 h-4 animate-spin" />
              <span>Loading logs...</span>
            </div>
          </div>
        ) : filteredLines.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-theme-text-tertiary gap-2">
            <Terminal className="w-8 h-8" />
            <span>No logs available</span>
          </div>
        ) : (
          <div className="p-2">
            {filteredLines.map((line, i) => (
              <LogLineItem key={i} line={line} searchQuery={searchQuery} />
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
function LogLineItem({ line, searchQuery }: { line: LogLine; searchQuery: string }) {
  const levelColor = getLogLevelColor(line.content)
  const content = searchQuery
    ? highlightSearchMatches(line.content, searchQuery)
    : escapeHtml(line.content)

  return (
    <div className="flex hover:bg-theme-surface/50 group leading-5">
      {line.timestamp && (
        <span className="text-theme-text-tertiary select-none pr-2 whitespace-nowrap">
          {formatLogTimestamp(line.timestamp)}
        </span>
      )}
      <span
        className={`whitespace-pre-wrap break-all ${levelColor}`}
        dangerouslySetInnerHTML={{ __html: content }}
      />
    </div>
  )
}
