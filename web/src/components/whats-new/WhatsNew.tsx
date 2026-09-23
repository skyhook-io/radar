import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ArrowRight, ExternalLink, Sparkles, X } from 'lucide-react'
import { DialogPortal } from '@skyhook-io/k8s-ui'
import { useCapabilities, useVersionCheck } from '../../api/client'
import { releaseNotesFor, RELEASE_NOTES, type ReleaseNotes } from './releaseNotes'

const LAST_SEEN_KEY = 'radar-whats-new-seen'
const PREVIEW_PARAM = 'whats-new'
export const SHOW_WHATS_NEW_EVENT = 'radar:show-whats-new'

/**
 * A fresh install has nothing to compare against, so it only records the
 * version. Installs that predate this key are recognized by any other Radar
 * localStorage entry and see the notes once.
 */
export function shouldShowWhatsNew(
  currentVersion: string,
  lastSeen: string | null,
  hasPriorRadarState: boolean,
): boolean {
  if (!releaseNotesFor(currentVersion)) return false
  if (lastSeen === null) return hasPriorRadarState
  return normalize(lastSeen) !== normalize(currentVersion)
}

function normalize(version: string): string {
  return version.startsWith('v') ? version : `v${version}`
}

// undefined when storage is unavailable: nothing could record the dismissal,
// so auto-opening would repeat on every load.
function readLastSeen(): string | null | undefined {
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

function markSeen(version: string) {
  try { localStorage.setItem(LAST_SEEN_KEY, normalize(version)) } catch { /* ignore */ }
}

interface WhatsNewProps {
  onNavigate: (path: string) => void
}

/**
 * Opens once after Radar is upgraded to a version that has release notes.
 * `?whats-new` (or `?whats-new=v1.15.0`) opens it on demand for previews.
 */
export function WhatsNew({ onNavigate }: WhatsNewProps) {
  const { data: versionInfo } = useVersionCheck()
  const { data: capabilities } = useCapabilities()
  const [searchParams, setSearchParams] = useSearchParams()
  const [notes, setNotes] = useState<ReleaseNotes | null>(null)
  const [open, setOpen] = useState(false)
  const [previousVersion, setPreviousVersion] = useState<string | null>(null)

  // Radar Cloud ships its own release communication.
  const isCloud = capabilities?.deployment?.mode === 'cloud'
  const autoOpenAllowed = !!capabilities && !isCloud
  const currentVersion = versionInfo?.currentVersion
  const previewParam = searchParams.get(PREVIEW_PARAM)

  useEffect(() => {
    if (previewParam === null) return
    const preview = releaseNotesFor(previewParam) ?? releaseNotesFor(currentVersion) ?? RELEASE_NOTES[0]
    if (!preview) return
    setPreviousVersion(null)
    setNotes(preview)
    setOpen(true)
  }, [previewParam, currentVersion])

  useEffect(() => {
    if (!currentVersion || !autoOpenAllowed || previewParam !== null) return
    const lastSeen = readLastSeen()
    if (lastSeen === undefined) return
    if (shouldShowWhatsNew(currentVersion, lastSeen, HAD_PRIOR_RADAR_STATE)) {
      setPreviousVersion(lastSeen)
      setNotes(releaseNotesFor(currentVersion) ?? null)
      setOpen(true)
    } else if (lastSeen === null) {
      markSeen(currentVersion)
    }
  // Only the version decides; the preview param is handled above.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentVersion, autoOpenAllowed])

  useEffect(() => {
    const handler = () => {
      const current = releaseNotesFor(currentVersion) ?? RELEASE_NOTES[0]
      if (!current) return
      setPreviousVersion(null)
      setNotes(current)
      setOpen(true)
    }
    window.addEventListener(SHOW_WHATS_NEW_EVENT, handler)
    return () => window.removeEventListener(SHOW_WHATS_NEW_EVENT, handler)
  }, [currentVersion])

  const close = useCallback(() => {
    setOpen(false)
    if (currentVersion) markSeen(currentVersion)
    if (searchParams.has(PREVIEW_PARAM)) {
      const next = new URLSearchParams(searchParams)
      next.delete(PREVIEW_PARAM)
      setSearchParams(next, { replace: true })
    }
  }, [currentVersion, searchParams, setSearchParams])

  const go = useCallback((path: string) => {
    close()
    onNavigate(path)
  }, [close, onNavigate])

  if (isCloud) return null

  return (
    <DialogPortal open={open} onClose={close} className="w-[760px] max-w-[calc(100vw-2rem)] max-h-[min(820px,calc(100vh-4rem))] flex flex-col rounded-xl">
      {notes && (
        <WhatsNewContent
          notes={notes}
          previousVersion={previousVersion}
          onClose={close}
          onNavigate={go}
        />
      )}
    </DialogPortal>
  )
}

interface WhatsNewContentProps {
  notes: ReleaseNotes
  previousVersion?: string | null
  onClose: () => void
  onNavigate: (path: string) => void
}

export function WhatsNewContent({ notes, previousVersion, onClose, onNavigate }: WhatsNewContentProps) {
  return (
    <>
      <div className="relative px-6 pt-6 pb-4 border-b border-theme-border-subtle">
        <div className="flex items-center gap-3">
          <div className="flex items-center justify-center w-9 h-9 rounded-full bg-accent-muted shrink-0">
            <Sparkles className="w-4 h-4 text-accent" aria-hidden />
          </div>
          <div className="min-w-0">
            <h2 className="text-base font-semibold text-theme-text-primary">
              What's new in Radar <span className="font-mono">{notes.version}</span>
            </h2>
            <p className="text-xs text-theme-text-tertiary mt-0.5">
              {previousVersion && normalize(previousVersion) !== notes.version
                ? <>Updated from <span className="font-mono">{normalize(previousVersion)}</span></>
                : 'Highlights from this release'}
            </p>
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

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-4">
        <ul className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          {notes.highlights.map(item => {
            const Icon = item.icon
            return (
              <li key={item.id} className="flex gap-3 card-inner-lg">
                <div className="flex items-center justify-center w-8 h-8 rounded-md bg-theme-elevated border border-theme-border-subtle shrink-0">
                  <Icon className="w-4 h-4 text-theme-text-secondary" aria-hidden />
                </div>
                <div className="min-w-0 flex-1">
                  <h3 className="text-sm font-medium text-theme-text-primary leading-snug">{item.title}</h3>
                  <p className="text-xs text-theme-text-secondary mt-0.5 leading-relaxed">{item.description}</p>
                  {item.path && item.cta && (
                    <button
                      type="button"
                      onClick={() => onNavigate(item.path!)}
                      className="inline-flex items-center gap-1 mt-1.5 text-xs font-medium text-accent-text hover:underline"
                    >
                      {item.cta}
                      <ArrowRight className="w-3 h-3" aria-hidden />
                    </button>
                  )}
                </div>
              </li>
            )
          })}
        </ul>

        {notes.improvements.length > 0 && (
          <div className="mt-4 pt-4 border-t border-theme-border-subtle">
            <h3 className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary mb-2">
              Also in this release
            </h3>
            <ul className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1.5">
              {notes.improvements.map(line => (
                <li key={line} className="flex gap-2 text-xs text-theme-text-secondary leading-relaxed">
                  <span className="mt-[7px] w-1 h-1 rounded-full bg-theme-text-tertiary shrink-0" aria-hidden />
                  {line}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>

      <div className="flex items-center justify-between gap-3 px-6 py-3 border-t border-theme-border-subtle">
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
