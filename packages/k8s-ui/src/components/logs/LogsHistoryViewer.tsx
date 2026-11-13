import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { parseLogLine } from '../../utils/log-format'
import { triggerDownload } from '../../utils/download'
import { useLogBuffer } from './useLogBuffer'
import { ContainerSelect, HISTORY_WINDOWS, HistoryWindowSelect } from './LogToolbarSelects'
import { LogCore } from './LogCore'
import type { LogExportPayload } from '../../utils/log-export'
import type { LogPalette } from './log-palette'
import { useToast } from '../ui/Toast'

export type LogsHistoryWindow = '1h' | '6h' | '24h' | '7d'

/** A History read: lines per container ("<RFC3339> text\n"), oldest first. */
export interface LogsHistoryResult {
  containers: string[]
  logs: Record<string, string>
  /** The store returned `limit` lines, so older lines in the window are missing. */
  truncated: boolean
  limit: number
  window: { start: string; end: string }
}

export type FetchHistory = (p: { container?: string; since: LogsHistoryWindow }) => Promise<LogsHistoryResult>

type ToolbarLead = (ctx: { isDark: boolean; palette: LogPalette }) => ReactNode

/**
 * Logs read from the log store (Loki) for a window, instead of from
 * Kubernetes: no stream and no previous instance. For a pod that is gone the
 * host passes no containers, and they come from the result.
 */
export function LogsHistoryViewer({ podName, containers, initialContainer, fetchHistory, historyEmptyHint, overrideDownload, forceDark, defaultDark, toolbarLead }: {
  podName: string
  containers: string[]
  initialContainer?: string
  fetchHistory: FetchHistory
  historyEmptyHint?: ReactNode
  overrideDownload?: (content: string, mime: string, filename: string) => void
  forceDark?: boolean
  defaultDark?: boolean
  toolbarLead?: ToolbarLead
}) {
  const [since, setSince] = useState<LogsHistoryWindow>('6h')
  const [knownContainers, setKnownContainers] = useState(containers)
  // A gone pod's containers are unknown until the first read: read them all.
  const [container, setContainer] = useState(initialContainer || containers[0] || '')
  const [result, setResult] = useState<LogsHistoryResult | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { entries, isFollowing, pendingLineCount, retainedLineCount, missingLineCount, getRetainedEntries, setFollowing, set, clear } = useLogBuffer()
  const { showError, showSuccess } = useToast()
  const seq = useRef(0)

  const load = useCallback(async () => {
    const n = ++seq.current
    setIsLoading(true)
    setError(null)
    try {
      const r = await fetchHistory({ container: container || undefined, since })
      if (n !== seq.current) return
      setResult(r)
      setKnownContainers(known => (known.length === 0 ? r.containers : known))
      const keys = container ? [container] : Object.keys(r.logs)
      const lines = keys.flatMap(c => (r.logs[c] ?? '').split('\n').filter(Boolean).map(line => ({ ...parseLogLine(line), container: c })))
      lines.sort((a, b) => a.timestamp.localeCompare(b.timestamp))
      set(lines)
    } catch (err) {
      if (n !== seq.current) return
      set([])
      setResult(null)
      setError(err instanceof Error ? err.message : 'Failed to read log history')
    } finally {
      if (n === seq.current) setIsLoading(false)
    }
  }, [fetchHistory, container, since, set])

  useEffect(() => { load() }, [load])

  const downloadLogs = useCallback(({ content, mime, extension }: LogExportPayload) => {
    const filename = `${podName}-${container || 'all'}-history.${extension}`
    try {
      triggerDownload(content, mime, filename, overrideDownload)
      if (!overrideDownload) showSuccess('Log download started', `Saving ${filename}. Check your browser Downloads.`)
    } catch (err) {
      showError('Failed to download logs', err instanceof Error ? err.message : 'Unknown download error')
    }
  }, [podName, container, overrideDownload, showError, showSuccess])

  const windowLabel = HISTORY_WINDOWS.find(w => w.value === since)?.label.toLowerCase() ?? since
  return (
    <div className="flex h-full min-h-0 flex-col">
      {result && (
        <div className="shrink-0 border-b border-theme-border bg-theme-surface px-3 py-1.5 text-xs text-theme-text-secondary">
          From Loki, {windowLabel}
          {result.truncated && `, cut at ${result.limit.toLocaleString()} lines (the newest are shown)`}.
        </div>
      )}
      <div className="min-h-0 flex-1">
        <LogCore
          entries={entries}
          isFollowing={isFollowing}
          onFollowingChange={setFollowing}
          pendingLineCount={pendingLineCount}
          retainedLineCount={retainedLineCount}
          missingLineCount={missingLineCount}
          getRetainedEntries={getRetainedEntries}
          isLoading={isLoading}
          errorMessage={entries.length === 0 ? error : null}
          isStreaming={false}
          onStopStream={() => {}}
          onRefresh={load}
          onDownload={downloadLogs}
          onClear={clear}
          emptyMessage={
            <div className="space-y-1">
              <div>Loki has no lines for this pod in this window. If the pod&apos;s logs aren&apos;t shipped to Loki, they were never stored.</div>
              {historyEmptyHint}
            </div>
          }
          toolbarExtra={({ isDark, palette }) => (
            <>
              {toolbarLead?.({ isDark, palette })}
              <ContainerSelect containers={knownContainers} value={container} onChange={setContainer} includeAll={containers.length === 0 && knownContainers.length > 1} isDark={isDark} />
              <HistoryWindowSelect value={since} onChange={(v) => setSince(v as LogsHistoryWindow)} isDark={isDark} />
            </>
          )}
          forceDark={forceDark}
          defaultDark={defaultDark}
        />
      </div>
    </div>
  )
}
