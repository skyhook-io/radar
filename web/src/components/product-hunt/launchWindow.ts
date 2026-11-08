import type { VersionInfo } from '../../api/client'

// The launch as the update check last described it (internal/launchday), so it
// can move or be called off without a release.
export type ProductHuntLaunch = { enabled: boolean; url: string; start: number; end: number }

// Used only when Radar's version check doesn't answer. Product Hunt's day runs
// midnight to midnight Pacific; PDT is UTC-7 until Nov 1.
export const FALLBACK_LAUNCH: ProductHuntLaunch = {
  enabled: true,
  url: 'https://www.producthunt.com/products/radar-7?launch=radar-101',
  start: Date.parse('2026-10-29T07:00:00Z'),
  end: Date.parse('2026-10-30T07:00:00Z'),
}

export const LAUNCH_NOTICE_DISMISSED_EVENT = 'radar:product-hunt-notice-dismissed'
// Sent by the sidebar's Product Hunt item to bring a dismissed card back.
export const LAUNCH_NOTICE_REOPEN_EVENT = 'radar:product-hunt-notice-reopen'
// The sidebar item the card opens from and closes into.
export const LAUNCH_RAIL_ITEM_ID = 'product-hunt-rail-item'

export type LaunchPhase = 'upcoming' | 'live'

// In-cluster Radar is often shared by people who didn't install it, so they
// get only the launch day.
export function launchShownIn(deploymentMode: string | undefined, phase: LaunchPhase): boolean {
  return deploymentMode === 'local' || (deploymentMode === 'in-cluster' && phase === 'live')
}

export function resolveLaunch(fromServer: VersionInfo['productHuntLaunch']): ProductHuntLaunch {
  if (!fromServer) return FALLBACK_LAUNCH
  return { enabled: fromServer.enabled, url: fromServer.url, start: Date.parse(fromServer.start), end: Date.parse(fromServer.end) }
}

export function launchPhase(launch: ProductHuntLaunch, now: number): LaunchPhase | null {
  if (!launch.enabled || !(now < launch.end)) return null
  return now >= launch.start ? 'live' : 'upcoming'
}

// "Oct 29": the launch day on Product Hunt's own clock.
export function launchDayLabel(launch: ProductHuntLaunch): string {
  return new Date(launch.start).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'America/Los_Angeles' })
}

// Keyed by launch day, so a moved launch is announced again.
function dismissedKey(launch: ProductHuntLaunch, phase: LaunchPhase): string {
  return `radar-product-hunt-dismissed-${new Date(launch.start).toISOString().slice(0, 10)}-${phase}`
}

// Covers this page when localStorage is unavailable.
const dismissedThisPage = new Set<string>()

export function isPhaseDismissed(launch: ProductHuntLaunch, phase: LaunchPhase): boolean {
  const key = dismissedKey(launch, phase)
  if (dismissedThisPage.has(key)) return true
  try {
    return localStorage.getItem(key) !== null
  } catch {
    return false
  }
}

export function markPhaseDismissed(launch: ProductHuntLaunch, phase: LaunchPhase): void {
  const key = dismissedKey(launch, phase)
  dismissedThisPage.add(key)
  try {
    localStorage.setItem(key, '1')
  } catch {
    // localStorage unavailable: dismissedThisPage keeps it for this page
  }
  window.dispatchEvent(new Event(LAUNCH_NOTICE_DISMISSED_EVENT))
}

// On launch day the Product Hunt card goes first: the update notice and the
// first-run usage prompt wait until it is dismissed or the day is over.
export function launchNoticeHoldsCorner(launch: ProductHuntLaunch, now: number): boolean {
  return launchPhase(launch, now) === 'live' && !isPhaseDismissed(launch, 'live')
}
