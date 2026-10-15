import { useState, useEffect, useCallback, useRef } from 'react'
import { parseLogLine, parseLogRange } from '../../utils/log-format'
import { triggerDownload } from '../../utils/download'
import { useLogBuffer } from './useLogBuffer'
import { useLogStream } from './useLogStream'
import { ContainerSelect, LogRangeSelect } from './LogToolbarSelects'
import { LogCore } from './LogCore'
import type { LogExportPayload } from '../../utils/log-export'
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
  /** Pin the logs container to dark (true) or light (false) and hide the toggle */
  forceDark?: boolean
  /** Palette used until the user toggles it, e.g. the app theme (default: true) */
  defaultDark?: boolean
  /**
   * Open the stream automatically on mount (and on container switch) instead of
   * loading a static snapshot. The user can still Stop, and a manual Stop is not
   * re-armed. Requires `createStream`. Default: false.
   */
  autoStream?: boolean
}

// Kubernetes' answers that there are no logs to show, such as "no earlier run".
// They describe the pod, not a failure in Radar, so they are shown like an
// empty pane rather than as an error.
const CLUSTER_ANSWER_CODES = new Set(['no_previous_run', 'logs_unavailable', 'container_not_started'])

function isClusterAnswerCode(code: unknown): boolean {
  return typeof code === 'string' && CLUSTER_ANSWER_CODES.has(code)
}

function clusterAnswerOf(err: unknown): string | null {
  if (!(err instanceof Error) || !('data' in err)) return null
  const { data } = err
  if (typeof data !== 'object' || data === null || !('error_code' in data)) return null
  return isClusterAnswerCode(data.error_code) ? err.message : null
}

// Keyed by pod: everything the viewer remembers (the container, Previous, the
// auto-start latch, a manual Stop) belongs to one pod, and a host that swaps
// the pod without remounting would carry it over to the next one.
export function LogsViewer(props: LogsViewerProps) {
  return <PodLogsViewer key={`${props.namespace}/${props.podName}`} {...props} />
}

function PodLogsViewer({
  namespace: _namespace,
  podName,
  containers,
  initialContainer,
  fetchLogs,
  createStream,
  overrideDownload,
  forceDark,
  defaultDark,
  autoStream = false,
}: LogsViewerProps) {
  const [selectedContainer, setSelectedContainer] = useState(initialContainer || containers[0] || '')
  const [isLoading, setIsLoading] = useState(false)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const [emptyMessage, setEmptyMessage] = useState<string | null>(null)
  const [logRange, setLogRange] = useState('500')
  const [showPrevious, setShowPrevious] = useState(false)
  // What an open stream says it is waiting for, e.g. a container to start.
  const [streamNotice, setStreamNotice] = useState<string | null>(null)
  const { showError, showSuccess } = useToast()

  const { tailLines, sinceSeconds } = parseLogRange(logRange)
  const { entries, append, set, clear } = useLogBuffer()
  const { isStreaming, streamError, streamErrorCode, connecting, startStreaming, stopStreaming } = useLogStream()

  const willAutoStream = autoStream && !!createStream
  // Tracks the container we've already auto-started for, so re-renders don't
  // re-open the stream, and a container switch arms a fresh auto-start.
  const autoStartedForRef = useRef<string | null>(null)
  // Once the user explicitly Stops, don't auto-resume for this viewer's lifetime.
  const userStoppedRef = useRef(false)
  // Set when starting a stream unticks Previous, so that change does not also
  // load a snapshot over the stream.
  const skipSnapshotRef = useRef(false)
  // Numbers each snapshot load; a load superseded by a newer one or by a
  // stream start must not write over what replaced it.
  const loadSeqRef = useRef(0)
  // Whether a stream is open or opening, read where its end must not itself
  // trigger anything.
  const streamOpenRef = useRef(false)
  useEffect(() => { streamOpenRef.current = isStreaming || connecting }, [isStreaming, connecting])

  const loadLogs = useCallback(async () => {
    if (!selectedContainer) return
    const seq = ++loadSeqRef.current
    // The snapshot now owns the pane, so an ended stream's answer or failure
    // must not outlive it. A stream still open or opening is left alone.
    if (!streamOpenRef.current) stopStreaming()
    setIsLoading(true)
    setFetchError(null)
    setEmptyMessage(null)
    setStreamNotice(null)
    try {
      const data = await fetchLogs({ container: selectedContainer, tailLines, sinceSeconds, previous: showPrevious })
      if (seq !== loadSeqRef.current) return
      const logText = data[selectedContainer] ?? Object.values(data)[0] ?? ''
      set(logText.split('\n').filter(Boolean).map(line => {
        const { timestamp, content } = parseLogLine(line)
        return { timestamp, content, container: selectedContainer }
      }))
    } catch (err) {
      if (seq !== loadSeqRef.current) return
      const answer = clusterAnswerOf(err)
      if (answer) {
        set([])
        setEmptyMessage(answer)
        return
      }
      console.error('Failed to fetch logs:', err)
      setFetchError(err instanceof Error ? err.message : 'Failed to fetch logs')
    } finally {
      if (seq === loadSeqRef.current) setIsLoading(false)
    }
  }, [selectedContainer, tailLines, sinceSeconds, showPrevious, fetchLogs, set, stopStreaming])

  // When auto-streaming the stream supplies the initial tail, so the static
  // snapshot fetch is skipped to avoid a redundant request and a flash of
  // snapshot content before the stream takes over. If the user has Stopped we
  // won't auto-start, so fall back to the snapshot — otherwise a container
  // switch would keep showing the previous container's lines. Once the stream
  // has ended by itself, Previous and the range load a snapshot too.
  useEffect(() => {
    if (skipSnapshotRef.current) {
      skipSnapshotRef.current = false
      return
    }
    if (!willAutoStream || userStoppedRef.current || !streamOpenRef.current) loadLogs()
  }, [loadLogs, willAutoStream])
  useEffect(() => { stopStreaming() }, [selectedContainer, stopStreaming])

  // If auto-stream turns off while a stream is open (e.g. the pod went
  // terminal), stop following so live appends don't race the snapshot.
  const prevWillAutoStreamRef = useRef(willAutoStream)
  useEffect(() => {
    if (prevWillAutoStreamRef.current && !willAutoStream && isStreaming) stopStreaming()
    prevWillAutoStreamRef.current = willAutoStream
  }, [willAutoStream, isStreaming, stopStreaming])

  const handleStartStreaming = useCallback(() => {
    if (!createStream) return
    // The stream always shows the current run, so starting one leaves the
    // Previous view without loading a snapshot over the stream. The updater
    // reads Previous so this callback does not depend on it: a new identity
    // would re-run the auto-start effect and reopen the stream.
    setShowPrevious(previous => {
      if (previous) skipSnapshotRef.current = true
      return false
    })
    loadSeqRef.current++
    setIsLoading(false)
    setFetchError(null)
    setEmptyMessage(null)
    setStreamNotice(null)
    // The stream replays the last N lines (TailLines + Follow); clear first so
    // they don't duplicate lines already in the buffer (the snapshot on the
    // manual path, or an earlier stream on restart).
    clear()
    startStreaming(
      () => createStream({ container: selectedContainer, tailLines: 100, sinceSeconds }),
      {
        onLog: (data: any) => append({
          timestamp: data.timestamp || '',
          content: data.content || '',
          container: data.container || selectedContainer,
        }),
        onNotice: (data: unknown) => {
          const notice = typeof data === 'object' && data !== null && 'notice' in data && typeof data.notice === 'string' ? data.notice : ''
          setStreamNotice(notice || null)
        },
      },
      'Log stream connection failed',
    )
  }, [createStream, startStreaming, selectedContainer, sinceSeconds, append, clear])

  const handleStopStreaming = useCallback(() => {
    userStoppedRef.current = true
    setStreamNotice(null)
    stopStreaming()
  }, [stopStreaming])

  useEffect(() => {
    if (!willAutoStream || !selectedContainer) return
    if (userStoppedRef.current) return
    if (autoStartedForRef.current !== selectedContainer) {
      autoStartedForRef.current = selectedContainer
      handleStartStreaming()
    }
    // Reset the arm latch on teardown so a re-run re-streams — without this,
    // React Strict Mode's mount→unmount→mount closes the stream but the latch
    // stays set, leaving the viewer static. Only while a stream is open: one
    // that ended by itself stays ended, and a range change loads a snapshot.
    return () => { if (streamOpenRef.current) autoStartedForRef.current = null }
  }, [willAutoStream, selectedContainer, handleStartStreaming])

  const downloadLogs = useCallback(({ content, mime, extension }: LogExportPayload) => {
    const filename = `${podName}-${selectedContainer}-logs.${extension}`
    try {
      triggerDownload(content, mime, filename, overrideDownload)
      if (!overrideDownload) {
        showSuccess('Log download started', `Saving ${filename}. Check your browser Downloads.`)
      }
    } catch (err) {
      showError('Failed to download logs', err instanceof Error ? err.message : 'Unknown download error')
    }
  }, [podName, selectedContainer, overrideDownload, showError, showSuccess])

  // Previous and the range describe what a snapshot loads, so they stay
  // locked from the moment a stream starts opening, not only once it is open.
  const streamBusy = isStreaming || connecting

  const renderToolbarExtra = ({ isDark, palette }: { isDark: boolean; palette: LogPalette }) => (
    <>
      <ContainerSelect containers={containers} value={selectedContainer} onChange={setSelectedContainer} isDark={isDark} />

      <Tooltip content={streamBusy ? 'Stop streaming to view the previous instance' : "Show logs from the pod's previous instance (if it was restarted). Useful for troubleshooting crashed containers."} position="bottom">
        <label className={`flex items-center gap-1.5 text-xs ${palette.textSecondary} ${streamBusy ? 'opacity-50 cursor-not-allowed' : ''}`}>
          <input
            type="checkbox"
            checked={showPrevious}
            onChange={(e) => setShowPrevious(e.target.checked)}
            disabled={streamBusy}
            className={`w-3 h-3 rounded ${palette.borderLight} ${palette.elevatedBg} text-blue-500 focus:ring-blue-500 focus:ring-offset-0`}
          />
          <span className={`border-b border-dotted ${isDark ? 'border-slate-500' : 'border-slate-400'}`}>Previous</span>
        </label>
      </Tooltip>

      <LogRangeSelect value={logRange} onChange={setLogRange} isDark={isDark} disabled={streamBusy} />
    </>
  )

  // While the auto-stream is opening (before it first settles), show the
  // loading state rather than the empty-logs placeholder.
  const isConnecting = willAutoStream && connecting && entries.length === 0
  const streamAnswer = isClusterAnswerCode(streamErrorCode) ? streamError : null

  return (
    <div className="flex h-full min-h-0 flex-col">
    {streamError && entries.length > 0 && !fetchError && <div role="alert" className="shrink-0 border-b border-theme-border bg-theme-surface px-3 py-2 text-xs text-theme-text-secondary">{streamError} · Previously loaded logs remain below.</div>}
    <div className="min-h-0 flex-1"><LogCore
      entries={entries}
      isLoading={isLoading || isConnecting}
      errorMessage={fetchError || (entries.length === 0 && !emptyMessage && !streamAnswer ? streamError : null)}
      isStreaming={streamBusy}
      onStartStream={createStream ? handleStartStreaming : undefined}
      onStopStream={handleStopStreaming}
      onRefresh={loadLogs}
      onDownload={downloadLogs}
      onClear={clear}
      emptyMessage={emptyMessage ?? streamAnswer ?? streamNotice ?? undefined}
      toolbarExtra={renderToolbarExtra}
      forceDark={forceDark}
      defaultDark={defaultDark}
    /></div></div>
  )
}
