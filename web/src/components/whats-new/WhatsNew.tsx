import { useCallback, useEffect, useId, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { clsx } from 'clsx'
import { ArrowRight, Check, ExternalLink, Megaphone, X } from 'lucide-react'
import { DialogPortal } from '@skyhook-io/k8s-ui'
import { getApiBase } from '../../api/config'
import { markWhatsNewSeen, useCapabilities, useWhatsNewState, type WhatsNewState } from '../../api/client'
import { compareVersions } from '../../utils/version'
import {
  AUTO_OPEN_SCORE,
  CHANGELOG_URL,
  composeReleaseNotes,
  latestReleaseNotesFor,
  releaseLine,
  releaseNotesFor,
  RELEASE_NOTES,
  unreadReleases,
  type ComposedHighlight,
  type ComposedNotes,
  type ReleaseNotes,
} from './releaseNotes'
import type { UsageDataStatus } from '../../api/usage-data'
import { UsageDataAsk } from '../usage-data/UsageDataAsk'

const LAST_SEEN_KEY = 'radar-whats-new-seen'
const PREVIEW_PARAM = 'whats-new'
const PREVIEW_FROM_PARAM = 'whats-new-from'
export const SHOW_WHATS_NEW_EVENT = 'radar:show-whats-new'

/**
 * The notes to open automatically, if any: the unseen releases, composed, when
 * what they'd show is worth an interruption. Unseen notes below the bar stay
 * behind the nav rail's unread dot.
 */
export function whatsNewToOpen(
  currentVersion: string,
  lastSeen: string | null,
  priorInstall: boolean,
  catalog: ReleaseNotes[] = RELEASE_NOTES,
): ComposedNotes | null {
  const unread = unreadReleases(currentVersion, lastSeen, priorInstall, catalog)
  if (unread.length === 0) return null
  const notes = composeReleaseNotes(unread)
  return notes.score >= AUTO_OPEN_SCORE ? notes : null
}

/**
 * The version to record once the running version's notes are acknowledged, or
 * null when nothing should change. It only moves forward, so a downgrade
 * doesn't replay notes on the way back up, and a development build records
 * nothing — a non-version record could never be compared again.
 */
export function nextSeenVersion(currentVersion: string, lastSeen: string | null): string | null {
  if (compareVersions(currentVersion, currentVersion) === null) return null
  const cmp = lastSeen === null ? null : compareVersions(currentVersion, lastSeen)
  if (cmp !== null && cmp <= 0) return null
  return normalize(currentVersion)
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
  // Omitted by hosts that don't run usage data; the dialog then never asks.
  usageData?: UsageDataStatus
}

interface DialogView {
  notes: ComposedNotes
  /** The last version the user saw notes for, when this view covers what came after it. */
  since: string | null
  /** The version the notes bring the user up to. */
  to: string
  /** Closing records `to` as seen; previews record nothing. */
  acknowledges: boolean
}

/**
 * Opens after an upgrade when the releases the user hasn't seen have enough to
 * show; otherwise the nav rail's unread dot carries them.
 * `?whats-new` (or `?whats-new=v1.15.0`) opens it on demand for previews, and
 * `&whats-new-from=v1.14.0` previews it as composed for an upgrade from that version.
 */
export function WhatsNew({ onNavigate, usageData }: WhatsNewProps) {
  const { data: capabilities } = useCapabilities()
  // Radar Cloud ships its own release communication.
  const isCloud = capabilities?.deployment?.mode === 'cloud'
  const { data: state } = useWhatsNewState(!!capabilities && !isCloud)
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()
  // Fixed while open, so an acknowledgment from another tab can't reshuffle what's being read.
  const [view, setView] = useState<DialogView | null>(null)
  const [open, setOpen] = useState(false)
  // In-cluster only; undefined when browser storage is unavailable.
  const [browserLastSeen, setBrowserLastSeen] = useState(readBrowserLastSeen)
  const decidedRef = useRef(false)
  const { pathname } = useLocation()
  const titleId = useId()

  const currentVersion = state?.currentVersion
  const previewParam = searchParams.get(PREVIEW_PARAM)
  const previewFrom = searchParams.get(PREVIEW_FROM_PARAM)
  // One authority per install: the server record for a personal install, this
  // browser's storage for a shared one. undefined = nothing can be recorded.
  const lastSeen = !state ? undefined : state.storage === 'server' ? state.seenVersion ?? null : browserLastSeen
  const priorInstall = state?.storage === 'server' ? !!state.priorInstall : HAD_PRIOR_RADAR_STATE

  // Another tab acknowledging the notes clears this tab's dot too.
  useEffect(() => {
    const onStorage = () => setBrowserLastSeen(readBrowserLastSeen())
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  // Records the version a dialog was opened for, not the running one: the
  // server can be upgraded while the dialog is open.
  const recordSeen = useCallback((version: string, storage: WhatsNewState['storage'], seen: string | null) => {
    const next = nextSeenVersion(version, seen)
    if (next === null) return
    if (storage === 'browser') {
      writeBrowserLastSeen(next)
      setBrowserLastSeen(readBrowserLastSeen())
      return
    }
    // Only a confirmed write clears the dot; a failed one is retried on the
    // next close. A read still in flight predates the write, so it's dropped.
    const queryKey = ['whats-new', getApiBase()]
    markWhatsNewSeen(next).then(
      async () => {
        await queryClient.cancelQueries({ queryKey })
        queryClient.setQueryData<WhatsNewState>(queryKey, prev => prev && { ...prev, seenVersion: next })
      },
      err => console.warn('[whats-new] Failed to record seen version', next, err),
    )
  }, [queryClient])

  const show = useCallback((next: DialogView) => {
    setView(next)
    setOpen(true)
  }, [])

  useEffect(() => {
    if (previewParam === null) return
    const target = releaseNotesFor(previewParam)?.version ?? currentVersion ?? RELEASE_NOTES[0]?.version
    if (!target) return
    const upgrade = previewFrom ? unreadReleases(target, previewFrom, true) : []
    if (upgrade.length > 0) {
      show({ notes: composeReleaseNotes(upgrade), since: previewFrom, to: target, acknowledges: false })
      return
    }
    const single = releaseNotesFor(previewParam) ?? latestReleaseNotesFor(currentVersion) ?? RELEASE_NOTES[0]
    if (single) show({ notes: composeReleaseNotes([single]), since: null, to: single.version, acknowledges: false })
  }, [previewParam, previewFrom, currentVersion, show])

  useEffect(() => {
    if (!state || lastSeen === undefined || previewParam !== null || decidedRef.current) return
    decidedRef.current = true
    const due = whatsNewToOpen(state.currentVersion, lastSeen, priorInstall)
    // Only on Home: someone arriving on a deep link came for that page, often
    // mid-incident. Elsewhere the nav rail's unread dot carries the notes.
    if (due && (pathname === '/' || pathname === '/home')) {
      show({ notes: due, since: lastSeen, to: state.currentVersion, acknowledges: true })
    } else if (lastSeen === null && unreadReleases(state.currentVersion, lastSeen, priorInstall).length === 0) {
      // A fresh install: start the record, so the next upgrade has something to compare against.
      recordSeen(state.currentVersion, state.storage, lastSeen)
    }
  // Decided once, when the state loads; the preview param is handled above.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state?.currentVersion, state?.storage])

  const available = !!state && !isCloud && !!latestReleaseNotesFor(state.currentVersion)
  const unread = available && lastSeen !== undefined && unreadReleases(state.currentVersion, lastSeen, priorInstall).length > 0
  useEffect(() => { publishStatus({ available, unread }) }, [available, unread])
  useEffect(() => () => publishStatus(NO_STATUS), [])

  useEffect(() => {
    const handler = () => {
      if (!currentVersion) return
      const pending = lastSeen === undefined ? [] : unreadReleases(currentVersion, lastSeen, priorInstall)
      if (pending.length > 0) {
        show({ notes: composeReleaseNotes(pending), since: lastSeen ?? null, to: currentVersion, acknowledges: true })
        return
      }
      const latest = latestReleaseNotesFor(currentVersion)
      if (latest) show({ notes: composeReleaseNotes([latest]), since: null, to: currentVersion, acknowledges: true })
    }
    window.addEventListener(SHOW_WHATS_NEW_EVENT, handler)
    return () => window.removeEventListener(SHOW_WHATS_NEW_EVENT, handler)
  }, [currentVersion, lastSeen, priorInstall, show])

  const close = useCallback(() => {
    setOpen(false)
    if (view?.acknowledges && state && lastSeen !== undefined) recordSeen(view.to, state.storage, lastSeen)
    if (searchParams.has(PREVIEW_PARAM) || searchParams.has(PREVIEW_FROM_PARAM)) {
      const next = new URLSearchParams(searchParams)
      next.delete(PREVIEW_PARAM)
      next.delete(PREVIEW_FROM_PARAM)
      setSearchParams(next, { replace: true })
    }
  }, [state, lastSeen, view, recordSeen, searchParams, setSearchParams])

  const go = useCallback((path: string) => {
    close()
    onNavigate(path)
  }, [close, onNavigate])

  const readAboutUsageData = useCallback(() => {
    close()
    window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: 'privacy' } }))
  }, [close])

  if (isCloud) return null

  return (
    <DialogPortal open={open} onClose={close} ariaLabelledBy={titleId} className="w-[760px] max-w-[calc(100vw-2rem)] max-h-[min(840px,calc(100vh-4rem))] flex flex-col overflow-hidden rounded-xl">
      {view && (
        <WhatsNewContent
          titleId={titleId}
          notes={view.notes}
          since={view.since}
          to={view.to}
          onClose={close}
          onNavigate={go}
          ask={<UsageDataAsk usageData={usageData} onReadMore={readAboutUsageData} />}
        />
      )}
    </DialogPortal>
  )
}

interface WhatsNewContentProps {
  titleId?: string
  notes: ComposedNotes
  /** The last version the user saw notes for, when the notes cover what came after it. */
  since?: string | null
  /** The version the notes bring the user up to, which can be newer than the releases they cover. */
  to?: string
  onClose: () => void
  onNavigate: (path: string) => void
  // Rendered between the notes and the footer, outside the scroll area so it
  // stays visible however long the notes run.
  ask?: ReactNode
}

function isVersion(version: string | null | undefined): version is string {
  return !!version && compareVersions(version, version) !== null
}

export function WhatsNewContent({ titleId, notes, since, to: toVersion, onClose, onNavigate, ask }: WhatsNewContentProps) {
  const [lead, ...rest] = notes.highlights
  const newest = notes.versions[0]
  const mixed = notes.versions.length > 1
  const to = isVersion(toVersion) ? normalize(toVersion) : newest
  const from = isVersion(since) && normalize(since) !== to ? normalize(since) : null
  // Older releases than the dialog covers were left out, so "since" would overpromise.
  const title = !mixed
    ? <>What's new in Radar <span className="font-mono">{releaseLine(newest)}</span></>
    : from && notes.complete
      ? <>What's new since Radar <span className="font-mono">{releaseLine(from)}</span></>
      : <>Recent highlights in Radar</>
  // Only releases older than the newest are tagged: the heading already names it.
  const olderTag = (version: string) => mixed && version !== newest ? releaseLine(version) : null

  return (
    <>
      <div className="relative px-6 pt-5 pb-4 bg-gradient-to-b from-accent-muted to-transparent">
        <div className="flex items-center gap-4 pr-8">
          <div className="flex items-center justify-center w-11 h-11 rounded-xl bg-accent text-white shadow-glow-brand-sm shrink-0">
            <Megaphone className="w-5 h-5" aria-hidden />
          </div>
          <div className="min-w-0">
            <h2 id={titleId} className="text-lg font-semibold text-theme-text-primary leading-tight">
              {title}
            </h2>
            {from ? (
              <p className="flex flex-wrap items-center gap-1.5 mt-1.5 text-xs text-theme-text-tertiary">
                <span className="font-mono px-1.5 py-0.5 rounded bg-theme-elevated text-theme-text-secondary">{from}</span>
                <ArrowRight className="w-3 h-3" aria-label="to" />
                <span className="font-mono px-1.5 py-0.5 rounded bg-accent-muted text-accent-text">{to}</span>
              </p>
            ) : (
              <p className="mt-1 text-xs text-theme-text-tertiary">{mixed ? 'Highlights from recent releases' : 'Highlights from this release'}</p>
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
              <HighlightCard item={lead} lead tag={olderTag(lead.version)} onNavigate={onNavigate} />
            </li>
          )}
          {rest.map((item, i) => (
            // An odd card out spans the row instead of leaving a hole beside it.
            <li key={`${item.version}/${item.id}`} className={clsx(rest.length % 2 === 1 && i === rest.length - 1 && 'sm:col-span-2')}>
              <HighlightCard item={item} tone={TONE_TILES[i % TONE_TILES.length]} tag={olderTag(item.version)} onNavigate={onNavigate} />
            </li>
          ))}
        </ul>

        {notes.lines.length > 0 && (
          <div className="mt-4">
            <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary mb-2">
              {mixed ? 'Also new' : 'Also in this release'}
            </h3>
            <ul className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1">
              {notes.lines.map(line => {
                const tag = olderTag(line.version)
                return (
                  <li key={`${line.version}/${line.text}`} className="flex gap-2 text-xs text-theme-text-secondary leading-relaxed">
                    <Check className="w-3.5 h-3.5 mt-px text-accent shrink-0" aria-hidden />
                    <span>
                      {line.text}
                      {tag && <span className="ml-1.5 font-mono text-[11px] text-theme-text-tertiary">{tag}</span>}
                    </span>
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </div>

      {ask}

      <div className="flex items-center justify-end gap-2 px-6 py-3 border-t border-theme-border bg-theme-base/60">
        <a
          href={CHANGELOG_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1.5 rounded-lg border border-theme-border px-4 py-1.5 text-sm font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
        >
          Full changelog
          <ExternalLink className="w-3.5 h-3.5" aria-hidden />
        </a>
        <button type="button" onClick={onClose} className="btn-brand px-4 py-1.5 text-sm font-medium rounded-lg">
          Got it
        </button>
      </div>
    </>
  )
}

// By position, not by release, so cards mixed from several releases never
// repeat a color. Literal class strings so Tailwind keeps them.
const TONE_TILES = [
  'bg-violet-500/10 text-violet-600 dark:text-violet-400',
  'bg-teal-500/10 text-teal-600 dark:text-teal-400',
  'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  'bg-indigo-500/10 text-indigo-600 dark:text-indigo-400',
]

function HighlightCard({ item, lead = false, tone, tag, onNavigate }: {
  item: ComposedHighlight
  lead?: boolean
  tone?: string
  /** The release an older highlight came from, when the dialog mixes releases. */
  tag?: string | null
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
        lead ? 'w-10 h-10 bg-accent text-white' : clsx('w-8 h-8', tone ?? 'bg-accent-muted text-accent'),
      )}>
        <Icon className={lead ? 'w-5 h-5' : 'w-4 h-4'} aria-hidden />
      </span>
      <span className="block min-w-0 flex-1">
        <span className={clsx('block font-medium text-theme-text-primary leading-snug', lead ? 'text-base' : 'text-sm')}>
          {item.title}
          {tag && <span className="ml-1.5 font-mono font-normal text-[11px] text-theme-text-tertiary">{tag}</span>}
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
