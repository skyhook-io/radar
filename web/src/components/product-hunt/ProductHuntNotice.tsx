import { useEffect, useReducer, useState } from 'react'
import type { CSSProperties } from 'react'
import { clsx } from 'clsx'
import { Heart, Rocket, X } from 'lucide-react'
import { useCapabilities, useVersionCheck } from '../../api/client'
import type { UsageDataStatus } from '../../api/usage-data'
import { USAGE_PROMPT_VISIBILITY_EVENT, updateNoticeVisible, usagePromptOnScreen } from '../usage-data/UsageDataPrompt'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU } from '../../utils/animation'
import { LAUNCH_NOTICE_REOPEN_EVENT, LAUNCH_RAIL_ITEM_ID, isPhaseDismissed, launchDayLabel, launchPhase, launchShownIn, markPhaseDismissed, resolveLaunch, type LaunchPhase, type ProductHuntLaunch } from './launchWindow'

// The same tag for every install: it says the click came from inside Radar,
// nothing about who clicked.
const UTM = { utm_source: 'radar-oss', utm_medium: 'app', utm_campaign: 'ph-launch-2026-10' }
const FEATURE_URL = `https://radarhq.io/product/ai-investigations?${new URLSearchParams(UTM)}`
const SHOW_DELAY_MS = 10_000
const LAUNCH_RECHECK_MS = 60 * 60_000
// Long enough to read as growing out of the sidebar item, not snapping in.
const ENTER_MS = 320
const EXIT_MS = 220

export function shouldShowLaunchNotice(opts: {
  phase: LaunchPhase | null
  deploymentMode: string | undefined
  dismissed: boolean
  ready: boolean
  cornerBusy: boolean
}): boolean {
  return opts.phase !== null && launchShownIn(opts.deploymentMode, opts.phase) && !opts.dismissed && opts.ready && !opts.cornerBusy
}

export function launchLink(launch: ProductHuntLaunch): string {
  const url = new URL(launch.url)
  for (const [key, value] of Object.entries(UTM)) url.searchParams.set(key, value)
  return url.toString()
}

// A one-off notice for the October 2026 Product Hunt launch, whose date and
// link come from the update check. Before the day it waits its turn behind the
// update notice and the first-run usage prompt; on the day they wait for it.
// Each phase is dismissed on its own, so the launch-day card still shows to
// someone who closed the one before.
export function ProductHuntNotice({ usageData, usageDataPending }: { usageData: UsageDataStatus | undefined; usageDataPending: boolean }) {
  const { data: capabilities } = useCapabilities()
  const { data: versionInfo, isPending: versionPending, refetch: refetchVersion } = useVersionCheck()
  const [now, setNow] = useState(() => Date.now())
  const [ready, setReady] = useState(false)

  const [usagePromptShowing, setUsagePromptShowing] = useState(usagePromptOnScreen)
  const launch = resolveLaunch(versionInfo?.productHuntLaunch)
  const phase = launchPhase(launch, now)
  const ended = phase === null

  useEffect(() => {
    if (ended) return
    const delay = window.setTimeout(() => setReady(true), SHOW_DELAY_MS)
    // Re-reads the clock for the phase change, and the update notice's
    // dismissal, which only touches localStorage.
    const poll = window.setInterval(() => setNow(Date.now()), 60_000)
    // Picks up a launch moved or called off while this page stays open.
    const recheck = window.setInterval(() => void refetchVersion(), LAUNCH_RECHECK_MS)
    return () => {
      window.clearTimeout(delay)
      window.clearInterval(poll)
      window.clearInterval(recheck)
    }
  }, [ended, refetchVersion])

  // Opened again from the sidebar: shows at once, over whatever holds the corner.
  const [reopened, setReopened] = useState(false)
  // Re-renders after a dismissal, which lives outside React state.
  const [, rerender] = useReducer((n: number) => n + 1, 0)
  useEffect(() => {
    const onReopen = () => setReopened(true)
    window.addEventListener(LAUNCH_NOTICE_REOPEN_EVENT, onReopen)
    return () => window.removeEventListener(LAUNCH_NOTICE_REOPEN_EVENT, onReopen)
  }, [])

  // Tracked whatever the launch, so a launch switched back on sees a usage
  // prompt that opened meanwhile.
  useEffect(() => {
    const onUsagePrompt = () => setUsagePromptShowing(usagePromptOnScreen())
    window.addEventListener(USAGE_PROMPT_VISIBILITY_EVENT, onUsagePrompt)
    return () => window.removeEventListener(USAGE_PROMPT_VISIBILITY_EVENT, onUsagePrompt)
  }, [])

  const show = shouldShowLaunchNotice({
    phase,
    deploymentMode: capabilities ? (capabilities.deployment?.mode ?? 'local') : undefined,
    dismissed: phase !== null && isPhaseDismissed(launch, phase) && !reopened,
    // Waits for the version check, which says whether the launch is still on.
    ready: (reopened || ready) && !versionPending,
    // A card already on screen keeps the corner, whatever the day.
    cornerBusy: !reopened && (usagePromptShowing || (phase === 'upcoming' && (
      updateNoticeVisible(versionInfo?.latestVersion, versionInfo?.updateAvailable) || usageDataPending || !!usageData?.firstRunPrompt
    ))),
  })
  const { shouldRender, isOpen } = useAnimatedUnmount(show, EXIT_MS)
  // Beside the sidebar's Product Hunt item, growing out of it; the bottom-right
  // corner when the sidebar isn't there (narrow windows, embedded).
  const [anchor, setAnchor] = useState<{ left: number; bottom: number } | null>(null)
  useEffect(() => {
    if (!shouldRender) return
    const item = document.getElementById(LAUNCH_RAIL_ITEM_ID)
    let frame = 0
    const place = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        const r = item?.getBoundingClientRect()
        setAnchor(r && r.width > 0 ? { left: r.right + 8, bottom: Math.max(16, window.innerHeight - r.bottom) } : null)
      })
    }
    place()
    const observer = item && typeof ResizeObserver !== 'undefined' ? new ResizeObserver(place) : null
    if (item) observer?.observe(item)
    window.addEventListener('resize', place)
    return () => {
      cancelAnimationFrame(frame)
      observer?.disconnect()
      window.removeEventListener('resize', place)
    }
  }, [shouldRender])
  // What the card last showed, kept through the exit fade: the day can end, or
  // the launch be called off, while it is on screen.
  const [shown, setShown] = useState<{ phase: LaunchPhase; launch: ProductHuntLaunch } | null>(null)
  if (show && phase !== null && (shown?.phase !== phase || shown.launch.url !== launch.url || shown.launch.start !== launch.start)) {
    setShown({ phase, launch })
  }
  if (!shouldRender || !shown) return null

  const dismiss = () => {
    markPhaseDismissed(shown.launch, shown.phase)
    setReopened(false)
    rerender()
  }

  const placement: CSSProperties = anchor
    ? { left: anchor.left, bottom: anchor.bottom, maxWidth: `calc(100vw - ${anchor.left + 16}px)`, maxHeight: `calc(100vh - ${anchor.bottom + 16}px)` }
    : { right: 16, bottom: 16, maxWidth: 'calc(100vw - 2rem)', maxHeight: 'calc(100vh - 2rem)' }

  return (
    <div
      role="dialog"
      aria-label="Radar on Product Hunt"
      inert={!show || undefined}
      className={clsx(
        'launch-border fixed z-40 w-[24rem] rounded-xl border border-emerald-500/40 dark:border-emerald-400/30 shadow-[0_10px_40px_-6px_rgba(16,185,129,0.6)] dark:shadow-[0_10px_40px_-8px_rgba(16,185,129,0.45)]',
        anchor ? 'origin-bottom-left' : 'origin-bottom-right',
        TRANSITION_MENU,
        isOpen ? 'opacity-100 translate-x-0 translate-y-0 scale-100' : anchor ? 'opacity-0 -translate-x-3 scale-75' : 'opacity-0 translate-y-1 scale-[0.97]',
        !show && 'pointer-events-none',
      )}
      style={{ ...placement, transitionDuration: `${isOpen ? ENTER_MS : EXIT_MS}ms` }}
    >
      <div className="relative flex gap-3 p-4 rounded-[11px] max-h-[inherit] overflow-y-auto bg-theme-surface bg-gradient-to-br from-emerald-500/12 via-emerald-500/4 to-transparent dark:from-emerald-400/18 dark:via-emerald-400/6">
        <button
          type="button"
          onClick={dismiss}
          aria-label="Close"
          className="absolute top-2.5 right-2.5 p-1 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated"
        >
          <X className="w-3.5 h-3.5" />
        </button>
        <span className="flex items-center justify-center w-9 h-9 shrink-0 rounded-lg bg-emerald-500 text-white shadow-[0_4px_14px_-4px_rgba(16,185,129,0.6)]">
          <Rocket className="w-4 h-4 animate-launch-rocket" aria-hidden />
        </span>
        <div className="min-w-0">
          <p className="flex items-center gap-1.5 pr-6 text-sm font-medium text-theme-text-primary">
            Radar <Heart className="w-3.5 h-3.5 fill-current text-red-500 dark:text-red-400" aria-label="loves" /> Agents
          </p>
          <p className="text-xs font-medium text-emerald-700 dark:text-emerald-300 mt-0.5">
            {shown.phase === 'live' ? 'Live on Product Hunt today' : `Launching on Product Hunt ${launchDayLabel(shown.launch)}`}
          </p>
          <p className="text-xs text-theme-text-secondary mt-1.5">
            {shown.phase === 'live'
              ? "We're on Product Hunt today with Radar's newly upgraded MCP server and AI investigations. If Radar has made Kubernetes a little easier for you, a comment on the launch would mean a lot. It helps other engineers find Radar."
              : "We're launching Radar's newly upgraded MCP server and AI investigations on Product Hunt. If Radar has made Kubernetes a little easier for you, follow the launch and come say hi on the day. It helps other engineers find Radar."}
          </p>
          <div className="flex items-center gap-3 mt-3">
            <a
              href={launchLink(shown.launch)}
              target="_blank"
              rel="noopener noreferrer"
              onClick={dismiss}
              className="inline-flex items-center rounded-lg bg-emerald-600 hover:bg-emerald-700 dark:bg-emerald-500 dark:hover:bg-emerald-400 px-3 py-1.5 text-xs font-semibold text-white dark:text-emerald-950 whitespace-nowrap transition-colors"
            >
              {shown.phase === 'live' ? 'Comment on Product Hunt →' : 'Follow on Product Hunt →'}
            </a>
            <a
              href={FEATURE_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary hover:underline whitespace-nowrap"
            >
              How it works
            </a>
          </div>
        </div>
      </div>
    </div>
  )
}
