import { useState, useEffect, useRef, useCallback } from 'react'
import { usePodLogs, createLogStream, type LogStreamEvent } from '../../api/client'
import { ChevronDown } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { parseLogLine, parseLogRange, handleSSEError } from '../../utils/log-format'
import { useLogBuffer } from './useLogBuffer'
import { LogCore, type DownloadFormat } from './LogCore'

interface LogsViewerProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
}

export function LogsViewer({ namespace, podName, containers, initialContainer }: LogsViewerProps) {
  const [selectedContainer, setSelectedContainer] = useState(initialContainer || containers[0] || '')
  const [isStreaming, setIsStreaming] = useState(false)
  const [logRange, setLogRange] = useState('500')  // lines:N or since:N
  const [showPrevious, setShowPrevious] = useState(false)

  const { tailLines, sinceSeconds } = parseLogRange(logRange)

  const eventSourceRef = useRef<EventSource | null>(null)
  const { entries, append, set, clear } = useLogBuffer()

  // Fetch initial logs (non-streaming)
  const { data: logsData, refetch, isLoading } = usePodLogs(namespace, podName, {
    container: selectedContainer,
    tailLines,
    sinceSeconds,
    previous: showPrevious,
  })

  // Parse logs data into entries
  useEffect(() => {
    if (logsData?.logs && selectedContainer) {
      const logContent = logsData.logs[selectedContainer] || ''
      const lines = logContent.split('\n').filter(Boolean).map(line => {
        const { timestamp, content } = parseLogLine(line)
        return { timestamp, content, container: selectedContainer }
      })
      set(lines)
    }
  }, [logsData, selectedContainer, set])

  // Start streaming
  const startStreaming = useCallback(() => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
    }

    const es = createLogStream(namespace, podName, {
      container: selectedContainer,
      tailLines: 100,
      sinceSeconds,
    })

    es.addEventListener('connected', () => {
      setIsStreaming(true)
    })

    es.addEventListener('log', (event) => {
      try {
        const data: LogStreamEvent['data'] = JSON.parse(event.data)
        append({
          timestamp: data.timestamp || '',
          content: data.content || '',
          container: data.container || selectedContainer,
        })
      } catch (e) {
        console.error('Failed to parse log event:', e)
      }
    })

    es.addEventListener('end', () => {
      setIsStreaming(false)
    })

    es.addEventListener('error', (event) => {
      handleSSEError(event, 'Log stream error', () => { setIsStreaming(false); es.close() })
    })

    eventSourceRef.current = es
  }, [namespace, podName, selectedContainer, sinceSeconds, append])

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
  const downloadLogs = useCallback((format: DownloadFormat) => {
    let content: string
    let mime: string
    const ext = format
    switch (format) {
      case 'json':
        content = JSON.stringify(entries.map(l => ({
          timestamp: l.timestamp, content: l.content, container: l.container,
        })), null, 2)
        mime = 'application/json'
        break
      case 'csv':
        content = 'timestamp,container,content\n' + entries.map(l =>
          `${l.timestamp},${l.container},"${l.content.replace(/"/g, '""')}"`
        ).join('\n')
        mime = 'text/csv'
        break
      default:
        content = entries.map(l => `${l.timestamp} ${l.content}`).join('\n')
        mime = 'text/plain'
    }
    const blob = new Blob([content], { type: mime })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${podName}-${selectedContainer}-logs.${ext}`
    a.click()
    URL.revokeObjectURL(url)
  }, [entries, podName, selectedContainer])

  const toolbarExtra = (
    <>
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

      {/* Log range selector */}
      <Tooltip content="How many logs to load — by line count or time range" position="bottom">
        <select
          value={logRange}
          onChange={(e) => setLogRange(e.target.value)}
          className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1.5 pr-5 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500"
        >
          <optgroup label="Lines">
            <option value="100">100 lines</option>
            <option value="500">500 lines</option>
            <option value="1000">1,000 lines</option>
            <option value="5000">5,000 lines</option>
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
      entries={entries}
      isLoading={isLoading}
      isStreaming={isStreaming}
      onStartStream={startStreaming}
      onStopStream={stopStreaming}
      onRefresh={() => refetch()}
      onDownload={downloadLogs}
      onClear={clear}
      toolbarExtra={toolbarExtra}
    />
  )
}
