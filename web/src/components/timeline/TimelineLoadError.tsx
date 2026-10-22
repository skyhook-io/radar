import { clsx } from 'clsx'
import { AlertTriangle, Bug, RefreshCw } from 'lucide-react'
import { timelineDatabaseError } from '../../api/client'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui'

const SECONDARY_BUTTON =
  'flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border-light rounded-lg hover:bg-theme-hover transition-colors'

// TimelineLoadError is the full-screen state when the timeline has nothing to
// show. An unreachable PostgreSQL database gets its own copy: it says the
// database is the problem and where to look. The driver's error stays in
// Radar's logs and the diagnostics overlay.
export function TimelineLoadError({
  error,
  onRetry,
  retrying = false,
  onOpenDiagnostics,
  className,
}: {
  error?: Error | null
  onRetry: () => void
  /** A request is in flight: the error stays up, the button shows the attempt. */
  retrying?: boolean
  onOpenDiagnostics?: () => void
  className?: string
}) {
  const databaseError = timelineDatabaseError(error)
  const detail = error?.message?.trim()

  return (
    <div className={clsx('flex flex-col items-center justify-center text-theme-text-tertiary gap-3 px-6', className)}>
      <AlertTriangle className="w-10 h-10 text-amber-400/70" />
      {databaseError !== null ? (
        <>
          <p className="text-base text-theme-text-secondary">Timeline history is unavailable</p>
          <p className="max-w-md text-center text-sm">
            Radar can't connect to its PostgreSQL database, so changes aren't being recorded. It keeps
            retrying, and this page recovers on its own once the database is back.
          </p>
          <p className="max-w-md text-center text-xs">
            Radar's logs have the details: search them for <code className="font-mono">[timeline]</code>.
          </p>
        </>
      ) : (
        <>
          <p className="text-base">Failed to load timeline data</p>
          {detail && <p className="max-w-md text-center text-sm">{detail}</p>}
        </>
      )}
      <div className="flex items-center gap-2">
        <button type="button" onClick={onRetry} disabled={retrying} className={clsx(SECONDARY_BUTTON, 'disabled:opacity-60 disabled:cursor-default')}>
          <RefreshCw className={clsx('w-3.5 h-3.5', retrying && 'animate-spin')} />
          Try again
        </button>
        {databaseError !== null && onOpenDiagnostics && (
          <button type="button" onClick={onOpenDiagnostics} className={SECONDARY_BUTTON}>
            <Bug className="w-3.5 h-3.5" />
            Open diagnostics
          </button>
        )}
      </div>
    </div>
  )
}

// TimelineStaleNote is the banner over data that is still on screen while its
// refresh fails. When the database is the cause it says so and where to look,
// since the data underneath otherwise reads as current.
export function TimelineStaleNote({
  error,
  subject,
  onRetry,
}: {
  error?: Error | null
  /** What may be stale: the swimlane view or the list. */
  subject: 'timeline' | 'list'
  onRetry: () => void
}) {
  const databaseDown = timelineDatabaseError(error) !== null
  return (
    <div className="flex items-center gap-1.5 border-b border-theme-border px-4 py-1.5 text-xs text-theme-text-tertiary">
      <AlertTriangle className={`h-3.5 w-3.5 shrink-0 ${SEVERITY_TEXT.warning}`} />
      {databaseDown ? (
        <span>
          Radar can't connect to its PostgreSQL database, so the {subject} may be stale. Search Radar's logs
          for <code className="font-mono">[timeline]</code>.
        </span>
      ) : (
        <span>Live updates are failing, so the {subject} may be stale.</span>
      )}
      <button type="button" onClick={onRetry} className="underline hover:text-theme-text-primary">
        Retry now
      </button>
    </div>
  )
}
