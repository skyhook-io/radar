import { useState } from 'react'
import { ShieldOff, AlertTriangle, ServerCrash, LogIn, Copy, Check, type LucideIcon } from 'lucide-react'
import { clsx } from 'clsx'
import { PaneLoader } from './PaneLoader'
import { isFetchError } from '../../types/fetch-error'

// FetchResult collapses (loading, error, no-data) into one rendered outcome:
// loader while loading, a typed error surface when the fetch threw, or the
// notFoundMessage when neither — matching the prior plain-text "Resource not
// found" so a disabled React Query (loading=false, data=undefined, error=null
// under v5) still renders a sane fallback rather than going blank.
//
// Decision tree:
//   loading                  → <PaneLoader/>
//   error (403)              → "Access denied"            + error.message
//   error (404)              → notFoundMessage            + error.message
//   error (503)              → "Cluster unavailable"      + error.message
//   error (401)              → "Sign-in required"         (apiFetch redirects; fallback)
//   error (other / no shape) → "Couldn't load this view"  + error.message
//   no loading, no error     → notFoundMessage            (headline only, no detail)
//
// Separate from EmptyState (which conveys "no data here" with
// healthy/filtered/neutral tones) because HTTP-level fetch failures need
// distinct visual and informational semantics — error vs absence.
//
// The error contract is duck-typed via FetchErrorShape so this stays in
// @skyhook-io/k8s-ui without importing ApiError from either web/ (OSS)
// or radar-hub-web.

interface FetchResultProps {
  loading: boolean
  error?: unknown
  /** Body line for 404 (resource fetched, server says missing). Default "Resource not found". */
  notFoundMessage?: string
  /** Pin to parent height. Existing call sites use "h-32" or "h-full". */
  className?: string
}

export function FetchResult({
  loading,
  error,
  notFoundMessage = 'Resource not found',
  className = 'h-32',
}: FetchResultProps) {
  if (loading) {
    return <PaneLoader className={className} />
  }
  if (error === undefined || error === null) {
    // No loading + no error = the query is disabled or returned no data.
    // Render the headline-only "not found" state so callers gated on `!data`
    // don't end up with a blank body when React Query v5 leaves isLoading=false.
    return (
      <div className={clsx('flex items-center justify-center text-theme-text-tertiary', className)}>
        {notFoundMessage}
      </div>
    )
  }
  return <ErrorSurface error={error} notFoundMessage={notFoundMessage} className={className} />
}

interface ErrorSurfaceProps {
  error: unknown
  notFoundMessage: string
  className: string
}

function ErrorSurface({ error, notFoundMessage, className }: ErrorSurfaceProps) {
  const classified = classify(error, notFoundMessage)
  const Icon = classified.icon

  return (
    <div
      role="status"
      className={clsx('flex flex-col items-center justify-center gap-2 px-6 text-center', className)}
    >
      <Icon className="h-5 w-5 text-theme-text-tertiary" aria-hidden />
      <div className="text-sm font-medium text-theme-text-secondary">{classified.headline}</div>
      {classified.detail && (
        <div className="flex items-center gap-2 max-w-md">
          <span className="text-xs text-theme-text-tertiary break-words">{classified.detail}</span>
          <CopyErrorButton text={classified.detail} />
        </div>
      )}
    </div>
  )
}

interface Classified {
  headline: string
  detail: string | null
  icon: LucideIcon
}

function classify(error: unknown, notFoundMessage: string): Classified {
  if (isFetchError(error)) {
    switch (error.status) {
      case 403:
        return { headline: 'Access denied', detail: error.message, icon: ShieldOff }
      case 404:
        return { headline: notFoundMessage, detail: error.message, icon: AlertTriangle }
      case 401:
        return { headline: 'Sign-in required', detail: error.message, icon: LogIn }
      case 503:
        return { headline: 'Cluster unavailable', detail: error.message, icon: ServerCrash }
      default:
        return { headline: "Couldn't load this view", detail: error.message, icon: AlertTriangle }
    }
  }
  // Network failures (no .status), DOMException for AbortError, anything thrown without our shape.
  return { headline: "Couldn't load this view", detail: errorMessageOf(error), icon: AlertTriangle }
}

function errorMessageOf(error: unknown): string | null {
  if (error instanceof Error && error.message) return error.message
  if (typeof error === 'string') return error
  return null
}

function CopyErrorButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const onCopy = () => {
    navigator.clipboard.writeText(text).then(
      () => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1500)
      },
      () => { /* best-effort */ },
    )
  }
  return (
    <button
      type="button"
      onClick={onCopy}
      className="flex-shrink-0 p-1 rounded text-theme-text-tertiary hover:text-theme-text-secondary hover:bg-theme-hover"
      title={copied ? 'Copied' : 'Copy error'}
      aria-label={copied ? 'Copied' : 'Copy error'}
    >
      {copied ? <Check className="h-3 w-3" aria-hidden /> : <Copy className="h-3 w-3" aria-hidden />}
    </button>
  )
}
