import { useState, useEffect, useCallback } from 'react'
import { parseLogLine, parseLogRange } from '../../utils/log-format'
import { triggerDownload } from '../../utils/download'
import { useLogBuffer } from './useLogBuffer'
import { useLogStream } from './useLogStream'
import { ContainerSelect, LogRangeSelect } from './LogToolbarSelects'
import { LogCore } from './LogCore'
import type { DownloadFormat } from './LogCore'
import type { LogPalette } from './log-palette'
import { Tooltip } from '../ui/Tooltip'
import { useToast } from '../ui/Toast'

export interface LogsFetchParams {
  container: string
  tailLines?: number
  sinceSeconds?: number
  previous?: boolean
}

export interface LogsViewerProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
  /** Called to fetch logs. Return value is { [containerName]: rawLogText } */
  fetchLogs: (params: LogsFetchParams) => Promise<{ [container: string]: string }>
  /** If provided, the stream button is enabled. Called to open an SSE connection. */
  createStream?: (params: Omit<LogsFetchParams, 'previous'>) => EventSource
  /** Override the download mechanism (e.g. for desktop apps where blob URLs fail). */
  overrideDownload?: (content: string, mime: string, filename: string) => void
  /** Force dark mode on the logs container (default: true) */
  forceDark?: boolean
}

export function LogsViewer({
  namespace: _namespace,
  podName,
  containers,
  initialContainer,
  fetchLogs,
  createStream,
  overrideDownload,
  forceDark,
}: LogsViewerProps) {
  const [selectedContainer, setSelectedContainer] = useState(initialContainer || containers[0] || '')
  const [isLoading, setIsLoading] = useState(false)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const [logRange, setLogRange] = useState('500')
  const [showPrevious, setShowPrevious] = useState(false)
  const { showError, showSuccess } = useToast()

  const { tailLines, sinceSeconds } = parseLogRange(logRange)
  const { entries, append, set, clear } = useLogBuffer()
  const { isStreaming, startStreaming, stopStreaming } = useLogStream()

  const loadLogs = useCallback(async () => {
    if (!selectedContainer) return
    setIsLoading(true)
    setFetchError(null)
    try {
      const data = await fetchLogs({ container: selectedContainer, tailLines, sinceSeconds, previous: showPrevious })
      const logText = data[selectedContainer] ?? Object.values(data)[0] ?? ''
      set(logText.split('\n').filter(Boolean).map(line => {
        const { timestamp, content } = parseLogLine(line)
        return { timestamp, content, container: selectedContainer }
      }))
    } catch (err) {
      console.error('Failed to fetch logs:', err)
      setFetchError(err instanceof Error ? err.message : 'Failed to fetch logs')
    } finally {
      setIsLoading(false)
    }
  }, [selectedContainer, tailLines, sinceSeconds, showPrevious, fetchLogs, set])

  useEffect(() => { loadLogs() }, [loadLogs])
  useEffect(() => { stopStreaming() }, [selectedContainer, stopStreaming])

  const handleStartStreaming = useCallback(() => {
    if (!createStream) return
    startStreaming(
      () => createStream({ container: selectedContainer, tailLines: 100, sinceSeconds }),
      {
        onLog: (data: any) => append({
          timestamp: data.timestamp || '',
          content: data.content || '',
          container: data.container || selectedContainer,
        }),
      },
    )
  }, [createStream, startStreaming, selectedContainer, sinceSeconds, append])

  const downloadLogs = useCallback((format: DownloadFormat) => {
    let content: string
    let mime: string
    const filename = `${podName}-${selectedContainer}-logs.${format}`
    switch (format) {
      case 'json':
        content = JSON.stringify(entries.map(l => ({ timestamp: l.timestamp, content: l.content, container: l.container })), null, 2)
        mime = 'application/json'
        break
      case 'csv':
        content = 'timestamp,container,content\n' + entries.map(l =>
          `${l.timestamp},${l.container},"${l.content.replace(/"/g, '""')}"`)
          .join('\n')
        mime = 'text/csv'
        break
      default:
        content = entries.map(l => `${l.timestamp} ${l.content}`).join('\n')
        mime = 'text/plain'
    }
    try {
      triggerDownload(content, mime, filename, overrideDownload)
      if (!overrideDownload) {
        showSuccess('Log download started', `Saving ${filename}. Check your browser Downloads.`)
      }
    } catch (err) {
      showError('Failed to download logs', err instanceof Error ? err.message : 'Unknown download error')
    }
  }, [entries, podName, selectedContainer, overrideDownload, showError, showSuccess])

  const renderToolbarExtra = ({ isDark, palette }: { isDark: boolean; palette: LogPalette }) => (
    <>
      <ContainerSelect containers={containers} value={selectedContainer} onChange={setSelectedContainer} isDark={isDark} />

      <Tooltip content="Show logs from the pod's previous instance (if it was restarted). Useful for troubleshooting crashed containers." position="bottom">
        <label className={`flex items-center gap-1.5 text-xs ${palette.textSecondary}`}>
          <input
            type="checkbox"
            checked={showPrevious}
            onChange={(e) => setShowPrevious(e.target.checked)}
            className={`w-3 h-3 rounded ${palette.borderLight} ${palette.elevatedBg} text-blue-500 focus:ring-blue-500 focus:ring-offset-0`}
          />
          <span className={`border-b border-dotted ${isDark ? 'border-slate-500' : 'border-slate-400'}`}>Previous</span>
        </label>
      </Tooltip>

      <LogRangeSelect value={logRange} onChange={setLogRange} isDark={isDark} />
    </>
  )

  return (
    <LogCore
      entries={entries}
      isLoading={isLoading}
      errorMessage={fetchError}
      isStreaming={isStreaming}
      onStartStream={createStream ? handleStartStreaming : undefined}
      onStopStream={stopStreaming}
      onRefresh={loadLogs}
      onDownload={downloadLogs}
      onClear={clear}
      toolbarExtra={renderToolbarExtra}
      forceDark={forceDark}
    />
  )
}
