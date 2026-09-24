import { useEffect, useRef, useState } from 'react'
import { clsx } from 'clsx'
import { BarChart3, Check, X } from 'lucide-react'
import { markUsagePromptShown, useSetUsageData, type UsageDataStatus } from '../../api/telemetry'
import { useVersionCheck } from '../../api/client'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'

// Long enough that the first thing a new user sees is their cluster, not a
// question about it.
export const FIRST_RUN_PROMPT_DELAY_MS = 15_000
const THANKS_MS = 4_000
const UPDATE_DISMISSED_KEY = 'radar-update-dismissed'

// The update notice uses the same corner. One nudge at a time.
export function updateNoticeVisible(latest: string | undefined, updateAvailable: boolean | undefined): boolean {
  if (!updateAvailable || !latest) return false
  try {
    return localStorage.getItem(UPDATE_DISMISSED_KEY) !== latest
  } catch {
    return true
  }
}

export function shouldShowFirstRunPrompt(opts: {
  status: UsageDataStatus | undefined
  ready: boolean
  updateNoticeShowing: boolean
}): boolean {
  return !!opts.status?.firstRunPrompt && opts.ready && !opts.updateNoticeShowing
}

// Shown once per machine, after the first install. Upgrading installs are
// asked in What's New instead. The server records the showing, so a reload,
// another browser or the Desktop app on the same machine won't show it again.
export function UsageDataPrompt({ status }: { status: UsageDataStatus | undefined }) {
  const setUsageData = useSetUsageData()
  const { data: versionInfo } = useVersionCheck()
  const [ready, setReady] = useState(false)
  const [, setTick] = useState(0)
  // Latched once shown: the server stops offering the prompt the moment it is
  // recorded, and the card must not disappear because of that.
  const [latched, setLatched] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  const [answer, setAnswer] = useState<boolean | null>(null)
  const recorded = useRef(false)

  const offered = !!status?.firstRunPrompt
  useEffect(() => {
    if (!offered) return
    const delay = window.setTimeout(() => setReady(true), FIRST_RUN_PROMPT_DELAY_MS)
    // Dismissing the update notice only touches localStorage, which does not
    // notify this tab, so look again now and then.
    const poll = window.setInterval(() => setTick((t) => t + 1), 5_000)
    return () => {
      window.clearTimeout(delay)
      window.clearInterval(poll)
    }
  }, [offered])

  const eligible = shouldShowFirstRunPrompt({
    status,
    ready,
    updateNoticeShowing: updateNoticeVisible(versionInfo?.latestVersion, versionInfo?.updateAvailable),
  })
  useEffect(() => {
    if (eligible && !recorded.current) {
      recorded.current = true
      setLatched(true)
      markUsagePromptShown()
    }
  }, [eligible])

  useEffect(() => {
    if (answer === null) return
    const t = window.setTimeout(() => setDismissed(true), THANKS_MS)
    return () => window.clearTimeout(t)
  }, [answer])

  const show = latched && !dismissed
  const { shouldRender, isOpen } = useAnimatedUnmount(show, overlayExitMs('menu'))
  if (!shouldRender) return null

  const readMore = () => {
    setDismissed(true)
    window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: 'privacy' } }))
  }
  const choose = (enabled: boolean) => setUsageData.mutate(enabled, { onSuccess: () => setAnswer(enabled) })

  return (
    <div
      role="dialog"
      aria-label="Help improve Radar"
      inert={!show || undefined}
      className={clsx(
        'fixed bottom-4 right-4 z-40 w-[19rem] max-w-[calc(100vw-2rem)] rounded-xl border border-theme-border bg-theme-surface shadow-theme-lg overflow-hidden origin-bottom-right',
        TRANSITION_MENU,
        isOpen ? 'opacity-100 translate-y-0 scale-100' : 'opacity-0 translate-y-1 scale-[0.97]',
        !show && 'pointer-events-none',
      )}
      style={overlayTransitionStyle(isOpen, 'menu')}
    >
      {answer === null ? (
        <div className="relative flex gap-3 p-3.5 pr-9">
          <button
            type="button"
            onClick={() => setDismissed(true)}
            aria-label="Close"
            className="absolute top-2.5 right-2.5 p-1 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated"
          >
            <X className="w-3.5 h-3.5" />
          </button>
          <span className="flex items-center justify-center w-8 h-8 shrink-0 rounded-lg bg-accent-muted text-accent">
            <BarChart3 className="w-4 h-4" aria-hidden />
          </span>
          <div className="min-w-0">
            <p className="text-sm font-medium text-theme-text-primary">Help improve Radar</p>
            <p className="mt-0.5 text-xs text-theme-text-secondary">
              {status?.shared ? 'Send anonymous usage stats from this shared Radar.' : 'Send anonymous usage stats.'}{' '}
              <button
                type="button"
                onClick={readMore}
                className="text-theme-text-primary underline underline-offset-2 hover:text-accent-text"
              >
                Read more
              </button>
            </p>
            <div className="flex items-center gap-2 mt-2.5">
              <button
                type="button"
                disabled={setUsageData.isPending}
                onClick={() => choose(true)}
                className="btn-brand px-3 py-1 text-xs font-medium rounded-md disabled:opacity-50"
              >
                Send stats
              </button>
              <button
                type="button"
                disabled={setUsageData.isPending}
                onClick={() => choose(false)}
                className="px-3 py-1 text-xs font-medium rounded-md text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated disabled:opacity-50"
              >
                No thanks
              </button>
            </div>
          </div>
        </div>
      ) : (
        <div className="flex items-center gap-2 px-4 py-3 text-xs text-theme-text-secondary">
          <Check className="w-3.5 h-3.5 shrink-0 text-accent" aria-hidden />
          {answer
            ? 'Thanks. Change this any time in Settings > Privacy.'
            : 'Nothing will be sent. Change this any time in Settings > Privacy.'}
        </div>
      )}
    </div>
  )
}
