import { useState, useEffect, useRef, useCallback } from 'react'
import { usePodLogs, createLogStream, type LogStreamEvent } from '../../api/client'
import { Play, Pause, Download, Search, X, ChevronDown, Terminal, RotateCcw } from 'lucide-react'

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
    <div className="flex flex-col h-full bg-slate-900">
      {/* Toolbar */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-slate-700 bg-slate-800">
        {/* Container selector */}
        {containers.length > 1 && (
          <div className="relative">
            <select
              value={selectedContainer}
              onChange={(e) => setSelectedContainer(e.target.value)}
              className="appearance-none bg-slate-700 text-white text-xs rounded px-2 py-1.5 pr-6 border border-slate-600 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            >
              {containers.map(c => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
            <ChevronDown className="absolute right-1.5 top-1/2 -translate-y-1/2 w-3 h-3 text-slate-400 pointer-events-none" />
          </div>
        )}

        {/* Stream toggle */}
        <button
          onClick={isStreaming ? stopStreaming : startStreaming}
          className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors ${
            isStreaming
              ? 'bg-green-600 text-white hover:bg-green-700'
              : 'bg-slate-700 text-slate-300 hover:bg-slate-600'
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
          className="flex items-center gap-1.5 px-2 py-1.5 text-xs rounded bg-slate-700 text-slate-300 hover:bg-slate-600 disabled:opacity-50 disabled:cursor-not-allowed"
          title="Refresh logs"
        >
          <RotateCcw className={`w-3 h-3 ${isLoading ? 'animate-spin' : ''}`} />
        </button>

        {/* Previous logs toggle */}
        <label className="flex items-center gap-1.5 text-xs text-slate-400 cursor-pointer">
          <input
            type="checkbox"
            checked={showPrevious}
            onChange={(e) => setShowPrevious(e.target.checked)}
            className="w-3 h-3 rounded border-slate-600 bg-slate-700 text-indigo-500 focus:ring-indigo-500 focus:ring-offset-0"
          />
          <span>Previous</span>
        </label>

        {/* Tail lines selector */}
        <select
          value={tailLines}
          onChange={(e) => setTailLines(Number(e.target.value))}
          className="appearance-none bg-slate-700 text-white text-xs rounded px-2 py-1.5 pr-5 border border-slate-600 focus:outline-none focus:ring-1 focus:ring-indigo-500"
        >
          <option value={100}>100 lines</option>
          <option value={500}>500 lines</option>
          <option value={1000}>1000 lines</option>
          <option value={5000}>5000 lines</option>
        </select>

        <div className="flex-1" />

        {/* Search toggle */}
        <button
          onClick={() => setShowSearch(!showSearch)}
          className={`p-1.5 rounded transition-colors ${
            showSearch ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-white hover:bg-slate-700'
          }`}
          title="Search logs"
        >
          <Search className="w-4 h-4" />
        </button>

        {/* Download */}
        <button
          onClick={downloadLogs}
          className="p-1.5 rounded text-slate-400 hover:text-white hover:bg-slate-700"
          title="Download logs"
        >
          <Download className="w-4 h-4" />
        </button>
      </div>

      {/* Search bar */}
      {showSearch && (
        <div className="flex items-center gap-2 px-3 py-2 border-b border-slate-700 bg-slate-800/50">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="Search logs..."
            className="flex-1 bg-transparent text-white text-sm placeholder-slate-500 focus:outline-none"
            autoFocus
          />
          {searchQuery && (
            <>
              <span className="text-xs text-slate-500">
                {filteredLines.length} / {logLines.length}
              </span>
              <button
                onClick={() => setSearchQuery('')}
                className="p-1 rounded text-slate-400 hover:text-white"
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
          <div className="flex items-center justify-center h-full text-slate-500">
            <div className="flex items-center gap-2">
              <RotateCcw className="w-4 h-4 animate-spin" />
              <span>Loading logs...</span>
            </div>
          </div>
        ) : filteredLines.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full text-slate-500 gap-2">
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
          className="absolute bottom-4 right-4 px-3 py-1.5 bg-indigo-600 text-white text-xs rounded-full shadow-lg hover:bg-indigo-700"
        >
          Scroll to bottom
        </button>
      )}
    </div>
  )
}

// Individual log line component
function LogLineItem({ line, searchQuery }: { line: LogLine; searchQuery: string }) {
  // Determine log level from content for coloring
  const levelColor = getLogLevelColor(line.content)

  // Highlight search matches
  const content = searchQuery
    ? highlightMatches(line.content, searchQuery)
    : line.content

  return (
    <div className="flex hover:bg-slate-800/50 group leading-5">
      {/* Timestamp */}
      {line.timestamp && (
        <span className="text-slate-500 select-none pr-2 whitespace-nowrap">
          {formatTimestamp(line.timestamp)}
        </span>
      )}
      {/* Content */}
      <span
        className={`whitespace-pre-wrap break-all ${levelColor}`}
        dangerouslySetInnerHTML={{ __html: content }}
      />
    </div>
  )
}

// Parse K8s log line format: 2024-01-20T10:30:00.123456789Z content
function parseLogLine(line: string): { timestamp: string; content: string } {
  if (line.length > 30 && line[4] === '-' && line[7] === '-' && line[10] === 'T') {
    const spaceIdx = line.indexOf(' ')
    if (spaceIdx > 20 && spaceIdx < 40) {
      return { timestamp: line.slice(0, spaceIdx), content: line.slice(spaceIdx + 1) }
    }
  }
  return { timestamp: '', content: line }
}

// Format timestamp for display
function formatTimestamp(ts: string): string {
  try {
    const date = new Date(ts)
    return date.toLocaleTimeString('en-US', {
      hour12: false,
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  } catch {
    return ts.slice(11, 19) // Fallback: extract HH:MM:SS
  }
}

// Get color class based on log level
function getLogLevelColor(content: string): string {
  const lower = content.toLowerCase()
  if (lower.includes('error') || lower.includes('fatal') || lower.includes('panic')) {
    return 'text-red-400'
  }
  if (lower.includes('warn')) {
    return 'text-yellow-400'
  }
  if (lower.includes('debug') || lower.includes('trace')) {
    return 'text-slate-400'
  }
  return 'text-slate-200'
}

// Highlight search matches in text
function highlightMatches(text: string, query: string): string {
  if (!query) return escapeHtml(text)
  const escaped = escapeHtml(text)
  const escapedQuery = escapeHtml(query)
  const regex = new RegExp(`(${escapeRegExp(escapedQuery)})`, 'gi')
  return escaped.replace(regex, '<mark class="bg-yellow-500/30 text-yellow-200">$1</mark>')
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
