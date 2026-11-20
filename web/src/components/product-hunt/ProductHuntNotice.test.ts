import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../api/client', () => ({ useCapabilities: () => ({ data: undefined }), useVersionCheck: () => ({ data: undefined }) }))
vi.mock('../../api/usage-data', () => ({ useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }), useMarkUsagePromptShown: () => vi.fn() }))

import { launchLink, shouldShowLaunchNotice } from './ProductHuntNotice'
import { FALLBACK_LAUNCH, isPhaseDismissed, launchDayLabel, launchNoticeHoldsCorner, launchPhase, markPhaseDismissed, resolveLaunch } from './launchWindow'

const launch = FALLBACK_LAUNCH

describe('launchPhase', () => {
  it('is upcoming before Product Hunt day starts in Pacific time', () => {
    expect(launchPhase(launch, Date.parse('2026-10-28T12:00:00Z'))).toBe('upcoming')
    expect(launchPhase(launch, Date.parse('2026-10-28T23:59:59-07:00'))).toBe('upcoming')
  })

  it('is live for the whole Pacific day of Oct 29', () => {
    expect(launchPhase(launch, Date.parse('2026-10-29T00:00:00-07:00'))).toBe('live')
    expect(launchPhase(launch, Date.parse('2026-10-29T09:01:00+02:00'))).toBe('live')
    expect(launchPhase(launch, Date.parse('2026-10-29T23:59:59-07:00'))).toBe('live')
  })

  it('is over once the Pacific day ends', () => {
    expect(launchPhase(launch, Date.parse('2026-10-30T00:00:00-07:00'))).toBe(null)
    expect(launchPhase(launch, Date.parse('2026-11-15T00:00:00Z'))).toBe(null)
  })
})

describe('resolveLaunch', () => {
  it('uses the built-in launch only when the version check says nothing', () => {
    expect(resolveLaunch(undefined)).toBe(FALLBACK_LAUNCH)
  })

  it('follows a moved or cancelled launch from the version check', () => {
    const moved = resolveLaunch({ enabled: true, url: launch.url, start: '2026-11-05T08:00:00Z', end: '2026-11-06T08:00:00Z' })
    expect(launchPhase(moved, Date.parse('2026-10-29T12:00:00Z'))).toBe('upcoming')
    expect(launchPhase(moved, Date.parse('2026-11-05T12:00:00Z'))).toBe('live')
    expect(launchDayLabel(moved)).toBe('Nov 5')

    const cancelled = resolveLaunch({ enabled: false, url: '', start: '0001-01-01T00:00:00Z', end: '0001-01-01T00:00:00Z' })
    expect(launchPhase(cancelled, Date.parse('2026-10-28T12:00:00Z'))).toBe(null)
    expect(launchPhase(cancelled, Date.parse('2026-10-29T12:00:00Z'))).toBe(null)
  })

  it('labels the day on Product Hunt time and tags the link', () => {
    expect(launchDayLabel(launch)).toBe('Oct 29')
    const link = new URL(launchLink(launch))
    expect(link.searchParams.get('launch')).toBe('radar-101')
    expect(link.searchParams.get('utm_source')).toBe('radar-oss')
    expect(link.searchParams.get('utm_campaign')).toBe('ph-launch-2026-10')
  })
})

describe('shouldShowLaunchNotice', () => {
  const base = { phase: 'live' as const, deploymentMode: 'local', dismissed: false, ready: true, cornerBusy: false }

  it('shows on a local install with the corner free', () => {
    expect(shouldShowLaunchNotice(base)).toBe(true)
    expect(shouldShowLaunchNotice({ ...base, phase: 'upcoming' })).toBe(true)
  })

  it('shows in-cluster on launch day only, and never in Cloud or before capabilities load', () => {
    expect(shouldShowLaunchNotice({ ...base, deploymentMode: 'in-cluster' })).toBe(true)
    expect(shouldShowLaunchNotice({ ...base, deploymentMode: 'in-cluster', phase: 'upcoming' })).toBe(false)
    expect(shouldShowLaunchNotice({ ...base, deploymentMode: 'cloud' })).toBe(false)
    expect(shouldShowLaunchNotice({ ...base, deploymentMode: undefined })).toBe(false)
  })

  it('stays away once dismissed, outside the window, before the delay, or while another notice holds the corner', () => {
    expect(shouldShowLaunchNotice({ ...base, dismissed: true })).toBe(false)
    expect(shouldShowLaunchNotice({ ...base, phase: null })).toBe(false)
    expect(shouldShowLaunchNotice({ ...base, ready: false })).toBe(false)
    expect(shouldShowLaunchNotice({ ...base, cornerBusy: true })).toBe(false)
  })
})

describe('isPhaseDismissed', () => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
  })
  afterEach(() => store.clear())

  it('keeps the launch-day card for someone who closed the one before it', () => {
    store.set('radar-product-hunt-dismissed-2026-10-29-upcoming', '1')
    expect(isPhaseDismissed(launch, 'upcoming')).toBe(true)
    expect(isPhaseDismissed(launch, 'live')).toBe(false)
  })

  it('announces a moved launch again', () => {
    store.set('radar-product-hunt-dismissed-2026-10-29-upcoming', '1')
    const moved = resolveLaunch({ enabled: true, url: launch.url, start: '2026-11-05T08:00:00Z', end: '2026-11-06T08:00:00Z' })
    expect(isPhaseDismissed(moved, 'upcoming')).toBe(false)
  })

  it('holds the corner on launch day until the launch-day card is dismissed', () => {
    const before = Date.parse('2026-10-28T12:00:00Z')
    const during = Date.parse('2026-10-29T10:00:00-07:00')
    const after = Date.parse('2026-10-30T01:00:00-07:00')
    expect(launchNoticeHoldsCorner(launch, before)).toBe(false)
    expect(launchNoticeHoldsCorner(launch, during)).toBe(true)
    expect(launchNoticeHoldsCorner(launch, after)).toBe(false)
    store.set('radar-product-hunt-dismissed-2026-10-29-live', '1')
    expect(launchNoticeHoldsCorner(launch, during)).toBe(false)
  })

  it('frees the corner for this page when localStorage refuses the dismissal', () => {
    const later = resolveLaunch({ enabled: true, url: launch.url, start: '2026-11-12T08:00:00Z', end: '2026-11-13T08:00:00Z' })
    const during = Date.parse('2026-11-12T10:00:00-08:00')
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('denied') },
      setItem: () => { throw new Error('denied') },
    })
    vi.stubGlobal('window', { dispatchEvent: vi.fn() })
    expect(launchNoticeHoldsCorner(later, during)).toBe(true)
    markPhaseDismissed(later, 'live')
    expect(launchNoticeHoldsCorner(later, during)).toBe(false)
    expect(isPhaseDismissed(later, 'upcoming')).toBe(false)
  })
})
