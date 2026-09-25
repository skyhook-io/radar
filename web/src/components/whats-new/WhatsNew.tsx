import { useCallback, useEffect, useId, useRef, useState, useSyncExternalStore } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { ArrowRight, Check, ExternalLink, Megaphone, X } from 'lucide-react'
import { DialogPortal } from '@skyhook-io/k8s-ui'
import { getApiBase } from '../../api/config'
import { markWhatsNewSeen, useCapabilities, useWhatsNewState, type WhatsNewState } from '../../api/client'
import { compareVersions } from '../../utils/version'
import { latestReleaseNotesFor, releaseNotesFor, RELEASE_NOTES, type ReleaseHighlight, type ReleaseNotes } from './releaseNotes'

const LAST_SEEN_KEY = 'radar-whats-new-seen'
const PREVIEW_PARAM = 'whats-new'
export const SHOW_WHATS_NEW_EVENT = 'radar:show-whats-new'

/**
 * The notes to open automatically, if any: the newest release at or below the
 * running version that is newer than what was last seen. A fresh install has
 * nothing to compare against and sees nothing; an install that predates the
 * seen record sees the notes once.
 */
export function whatsNewToShow(
  currentVersion: string,
  lastSeen: string | null,
  priorInstall: boolean,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ReleaseNotes | null {
  const notes = latestReleaseNotesFor(currentVersion, catalog)
  if (!notes) return null
  if (lastSeen === null) return priorInstall ? notes : null
  const newer = compareVersions(notes.version, lastSeen)
  return newer !== null && newer > 0 ? notes : null
}

/** The seen record only moves forward, so a downgrade doesn't replay notes on the way back up. */
export function nextSeenVersion(currentVersion: string, lastSeen: string | null): string {
  if (lastSeen === null) return normalize(currentVersion)
  const cmp = compareVersions(currentVersion, lastSeen)
  return cmp !== null && cmp > 0 ? normalize(currentVersion) : lastSeen
}

function normalize(version: string): string {
  return version.startsWith('v') ? version : `v${version}`
}

// A local Radar's browser origin changes between launches (Desktop binds a
// random port), so its record lives server-side; in-cluster, each browser
// keeps its own. undefined = nothing can be recorded, so don't auto-open.
function readBrowserLastSeen(): string | null | undefined {
  try {
    return localStorage.getItem(LAST_SEEN_KEY)
  } catch {
    return undefined
  }
}

function hadRadarStateBeforeThisSession(): boolean {
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      if (key && key !== LAST_SEEN_KEY && key.startsWith('radar')) return true
    }
  } catch { /* ignore */ }
  return false
}

// Read at module load, before this session's own queries write radar-* keys
// (the version check records its last run), which would make every fresh
// install look like an upgrade.
const HAD_PRIOR_RADAR_STATE = hadRadarStateBeforeThisSession()

function writeBrowserLastSeen(version: string) {
  try { localStorage.setItem(LAST_SEEN_KEY, version) } catch { /* ignore */ }
}

export interface WhatsNewStatus {
  /** Notes exist for this version, so there is something to open. */
  available: boolean
  /** Those notes are newer than what the user has seen. */
  unread: boolean
}

// The rail and the omnibar sit outside this component but need its status.
const NO_STATUS: WhatsNewStatus = { available: false, unread: false }
let status = NO_STATUS
const statusListeners = new Set<() => void>()

function publishStatus(next: WhatsNewStatus) {
  if (next.available === status.available && next.unread === status.unread) return
  status = next
  statusListeners.forEach(listener => listener())
}

function subscribeStatus(listener: () => void) {
  statusListeners.add(listener)
  return () => { statusListeners.delete(listener) }
}

export function useWhatsNewStatus(): WhatsNewStatus {
  return useSyncExternalStore(subscribeStatus, () => status)
}

export function openWhatsNew() {
  window.dispatchEvent(new Event(SHOW_WHATS_NEW_EVENT))
}

interface WhatsNewProps {
  onNavigate: (path: string) => void
}

/**
 * Opens once after Radar is upgraded to a version that has release notes.
 * `?whats-new` (or `?whats-new=v1.15.0`) opens it on demand for previews.
 */
export function WhatsNew({ onNavigate }: WhatsNewProps) {
  const { data: capabilities } = useCapabilities()
  // Radar Cloud ships its own release communication.
  const isCloud = capabilities?.deployment?.mode === 'cloud'
  const { data: state } = useWhatsNewState(!!capabilities && !isCloud)
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()
  const [notes, setNotes] = useState<ReleaseNotes | null>(null)
  const [open, setOpen] = useState(false)
  const [previousVersion, setPreviousVersion] = useState<string | null>(null)
  // undefined until loaded, or when nothing can be recorded.
  const [seen, setSeen] = useState<{ lastSeen: string | null } | undefined>()
  const decidedRef = useRef(false)
  const { pathname } = useLocation()
  const titleId = useId()

  const currentVersion = state?.currentVersion
  const previewParam = searchParams.get(PREVIEW_PARAM)

  const readLastSeen = useCallback((s: WhatsNewState): string | null | undefined => (
    s.storage === 'server' ? s.seenVersion ?? null : readBrowserLastSeen()
  ), [])

  const recordSeen = useCallback((s: WhatsNewState) => {
    const lastSeen = readLastSeen(s)
    if (lastSeen === undefined) return
    const next = nextSeenVersion(s.currentVersion, lastSeen)
    if (next === lastSeen) return
    setSeen({ lastSeen: next })
    if (s.storage === 'browser') {
      writeBrowserLastSeen(next)
      return
    }
    queryClient.setQueryData<WhatsNewState>(['whats-new', getApiBase()], { ...s, seenVersion: next })
    markWhatsNewSeen(next).catch(() => { /* retried on the next close or launch */ })
  }, [queryClient, readLastSeen])

  useEffect(() => {
    if (previewParam === null) return
    const preview = releaseNotesFor(previewParam) ?? latestReleaseNotesFor(currentVersion) ?? RELEASE_NOTES[0]
    if (!preview) return
    setPreviousVersion(null)
    setNotes(preview)
    setOpen(true)
  }, [previewParam, currentVersion])

  const priorInstall = state?.storage === 'server' ? !!state.priorInstall : HAD_PRIOR_RADAR_STATE

  useEffect(() => {
    if (!state) return
    const lastSeen = readLastSeen(state)
    setSeen(lastSeen === undefined ? undefined : { lastSeen })
    if (lastSeen === undefined || previewParam !== null || decidedRef.current) return
    decidedRef.current = true
    const due = whatsNewToShow(state.currentVersion, lastSeen, priorInstall)
    // Only on Home: someone arriving on a deep link came for that page, often
    // mid-incident. Elsewhere the nav rail's unread dot carries the notes.
    if (due && pathname === '/') {
      setPreviousVersion(lastSeen)
      setNotes(due)
      setOpen(true)
    } else if (!due && lastSeen === null) {
      recordSeen(state)
    }
  // Decided once, when the state loads; the preview param is handled above.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state?.currentVersion, state?.storage])

  const available = !!state && !isCloud && !!latestReleaseNotesFor(state.currentVersion)
  const unread = available && !!seen && !!whatsNewToShow(state.currentVersion, seen.lastSeen, priorInstall)
  useEffect(() => { publishStatus({ available, unread }) }, [available, unread])
  useEffect(() => () => publishStatus(NO_STATUS), [])

  useEffect(() => {
    const handler = () => {
      const latest = latestReleaseNotesFor(currentVersion) ?? RELEASE_NOTES[0]
      if (!latest) return
      setPreviousVersion(null)
      setNotes(latest)
      setOpen(true)
    }
    window.addEventListener(SHOW_WHATS_NEW_EVENT, handler)
    return () => window.removeEventListener(SHOW_WHATS_NEW_EVENT, handler)
  }, [currentVersion])

  const close = useCallback(() => {
    setOpen(false)
    // A preview of another release acknowledges nothing about this one.
    if (state && notes && notes === latestReleaseNotesFor(state.currentVersion)) recordSeen(state)
    if (searchParams.has(PREVIEW_PARAM)) {
      const next = new URLSearchParams(searchParams)
      next.delete(PREVIEW_PARAM)
      setSearchParams(next, { replace: true })
    }
  }, [state, notes, recordSeen, searchParams, setSearchParams])

  const go = useCallback((path: string) => {
    close()
    onNavigate(path)
  }, [close, onNavigate])

  if (isCloud) return null

  return (
    <DialogPortal open={open} onClose={close} ariaLabelledBy={titleId} className="w-[760px] max-w-[calc(100vw-2rem)] max-h-[min(840px,calc(100vh-4rem))] flex flex-col overflow-hidden rounded-xl">
      {notes && (
        <WhatsNewContent
          titleId={titleId}
          notes={notes}
          previousVersion={previousVersion}
          currentVersion={currentVersion}
          onClose={close}
          onNavigate={go}
        />
      )}
    </DialogPortal>
  )
}

interface WhatsNewContentProps {
  titleId?: string
  notes: ReleaseNotes
  previousVersion?: string | null
  /** The running version, which can be newer than the release the notes are for. */
  currentVersion?: string
  onClose: () => void
  onNavigate: (path: string) => void
}

export function WhatsNewContent({ titleId, notes, previousVersion, currentVersion, onClose, onNavigate }: WhatsNewContentProps) {
  const [lead, ...rest] = notes.highlights
  const to = currentVersion ? normalize(currentVersion) : notes.version
  const from = previousVersion && normalize(previousVersion) !== to ? normalize(previousVersion) : null

  return (
    <>
      <div className="relative px-6 pt-5 pb-4 bg-gradient-to-b from-accent-muted to-transparent">
        <div className="flex items-center gap-4 pr-8">
          <div className="flex items-center justify-center w-11 h-11 rounded-xl bg-accent text-white shadow-glow-brand-sm shrink-0">
            <Megaphone className="w-5 h-5" aria-hidden />
          </div>
          <div className="min-w-0">
            <h2 id={titleId} className="text-lg font-semibold text-theme-text-primary leading-tight">
              What's new in Radar <span className="font-mono">{notes.version}</span>
            </h2>
            {from ? (
              <p className="flex flex-wrap items-center gap-1.5 mt-1.5 text-xs text-theme-text-tertiary">
                <span>You just updated</span>
                <span className="font-mono px-1.5 py-0.5 rounded bg-theme-elevated text-theme-text-secondary">{from}</span>
                <ArrowRight className="w-3 h-3" aria-label="to" />
                <span className="font-mono px-1.5 py-0.5 rounded bg-accent-muted text-accent-text">{to}</span>
              </p>
            ) : (
              <p className="mt-1 text-xs text-theme-text-tertiary">Highlights from this release</p>
            )}
          </div>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="absolute top-4 right-4 p-1 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      {/* The fade sits over the bottom padding, so it only covers content while more is below. */}
      <div className="flex-1 min-h-0 overflow-y-auto px-6 pb-6 [mask-image:linear-gradient(to_bottom,black_calc(100%-1.5rem),transparent)]">
        <ul className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          {lead && (
            <li className="sm:col-span-2">
              <HighlightCard item={lead} lead onNavigate={onNavigate} />
            </li>
          )}
          {rest.map((item, i) => (
            // An odd card out spans the row instead of leaving a hole beside it.
            <li key={item.id} className={clsx(rest.length % 2 === 1 && i === rest.length - 1 && 'sm:col-span-2')}>
              <HighlightCard item={item} onNavigate={onNavigate} />
            </li>
          ))}
        </ul>

        {notes.improvements.length > 0 && (
          <div className="mt-4">
            <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary mb-2">
              Also in this release
            </h3>
            <ul className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1">
              {notes.improvements.map(line => (
                <li key={line} className="flex gap-2 text-xs text-theme-text-secondary leading-relaxed">
                  <Check className="w-3.5 h-3.5 mt-px text-accent shrink-0" aria-hidden />
                  {line}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>

      <div className="flex items-center justify-between gap-3 px-6 py-3 border-t border-theme-border bg-theme-base/60">
        <a
          href={notes.releaseUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:underline"
        >
          Full release notes
          <ExternalLink className="w-3 h-3" aria-hidden />
        </a>
        <button type="button" onClick={onClose} className="btn-brand px-4 py-1.5 text-sm font-medium rounded-lg">
          Got it
        </button>
      </div>
    </>
  )
}

function HighlightCard({ item, lead = false, onNavigate }: {
  item: ReleaseHighlight
  lead?: boolean
  onNavigate: (path: string) => void
}) {
  const Icon = item.icon
  const descriptionId = useId()
  const actionable = !!(item.path && item.cta)
  const className = clsx(
    'flex gap-3 w-full h-full text-left rounded-lg border',
    lead ? 'p-3.5 border-accent/30 bg-gradient-to-br from-accent-muted to-transparent' : 'p-3 border-theme-border',
    actionable && 'group transition-colors hover:border-accent/50 hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
  )
  const body = (
    <>
      <span className={clsx(
        'flex items-center justify-center shrink-0 rounded-lg',
        lead ? 'w-10 h-10 bg-accent text-white' : 'w-8 h-8 bg-accent-muted text-accent',
      )}>
        <Icon className={lead ? 'w-5 h-5' : 'w-4 h-4'} aria-hidden />
      </span>
      <span className="block min-w-0 flex-1">
        <span className={clsx('block font-medium text-theme-text-primary leading-snug', lead ? 'text-base' : 'text-sm')}>
          {item.title}
        </span>
        <span id={descriptionId} className={clsx('block mt-0.5 text-theme-text-secondary leading-relaxed', lead ? 'text-sm' : 'text-xs')}>
          {item.description}
        </span>
        {actionable && (
          <span className="inline-flex items-center gap-1 mt-1.5 text-xs font-medium text-accent-text group-hover:underline">
            {item.cta}
            <ArrowRight className="w-3 h-3" aria-hidden />
          </span>
        )}
      </span>
    </>
  )

  return actionable ? (
    <button
      type="button"
      onClick={() => onNavigate(item.path!)}
      aria-label={`${item.cta}: ${item.title}`}
      aria-describedby={descriptionId}
      className={className}
    >
      {body}
    </button>
  ) : (
    <div className={className}>{body}</div>
  )
}
