// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  OPEN_CLOUD_FUNNEL_EVENT,
  dismissCloudHint,
  hideIssueHint,
  isCloudHintDismissed,
  isCloudHintEntry,
  isIssueHintHidden,
  issueHintKey,
  readOpenRequest,
  withCloudHintEntry,
} from './cloudHints'
import {
  CONTEXT_SWITCHED_EVENT,
  CONTEXT_SWITCH_WINDOW_MS,
  comparedContexts,
  readSwitchLog,
  recentSwitches,
  recordContextSwitch,
  readTriggeredPair,
} from './contextSwitchLog'
import { clearHintMemory } from './hintStorage'

beforeEach(() => {
  window.localStorage.clear()
  window.sessionStorage.clear()
  clearHintMemory()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('open request', () => {
  it('accepts a known entry and its alert subject', () => {
    const event = new CustomEvent(OPEN_CLOUD_FUNNEL_EVENT, {
      detail: { entry: 'alert-issue', alert: { category: 'Crash loop', kind: 'Pod', name: 'api-1', namespace: 'shop' } },
    })
    expect(readOpenRequest(event)).toEqual({
      entry: 'alert-issue',
      alert: { category: 'Crash loop', kind: 'Pod', name: 'api-1', namespace: 'shop' },
    })
  })

  it('drops a forged entry, and an alert that arrives without a valid entry', () => {
    const forged = new CustomEvent(OPEN_CLOUD_FUNNEL_EVENT, {
      detail: { entry: '"><script>', alert: { kind: 'Pod', name: 'x' } },
    })
    expect(readOpenRequest(forged)).toEqual({ entry: null, alert: null })
  })

  it('drops a malformed alert subject but keeps the entry', () => {
    const event = new CustomEvent(OPEN_CLOUD_FUNNEL_EVENT, { detail: { entry: 'alert-findings', alert: { kind: 3 } } })
    expect(readOpenRequest(event)).toEqual({ entry: 'alert-findings', alert: null })
  })

  it('treats a plain event (the globe button) as no entry', () => {
    expect(readOpenRequest(new Event(OPEN_CLOUD_FUNNEL_EVENT))).toEqual({ entry: null, alert: null })
  })

  it('knows only the listed entries', () => {
    expect(isCloudHintEntry('timeline-history')).toBe(true)
    expect(isCloudHintEntry('timeline')).toBe(false)
    expect(isCloudHintEntry(undefined)).toBe(false)
  })
})

describe('link tagging', () => {
  it('adds utm_term and keeps the existing campaign parameters', () => {
    const tagged = withCloudHintEntry(
      'https://app.radarhq.io/signup?utm_source=radar-oss&utm_content=wizard-signup-button',
      'timeline-history',
    )
    const url = new URL(tagged)
    expect(url.searchParams.get('utm_term')).toBe('timeline-history')
    expect(url.searchParams.get('utm_content')).toBe('wizard-signup-button')
  })

  it('leaves the link alone without an entry, and never breaks an unparsable one', () => {
    expect(withCloudHintEntry('https://app.radarhq.io/signup', null)).toBe('https://app.radarhq.io/signup')
    expect(withCloudHintEntry('not a url', 'settings-ai')).toBe('not a url')
  })
})

describe('dismissal', () => {
  it('persists per surface', () => {
    expect(isCloudHintDismissed('alert-issue')).toBe(false)
    dismissCloudHint('alert-issue')
    expect(isCloudHintDismissed('alert-issue')).toBe(true)
    expect(isCloudHintDismissed('alert-findings')).toBe(false)
  })

  it('still works when storage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError')
    })
    expect(isCloudHintDismissed('context-switch')).toBe(false)
    expect(() => dismissCloudHint('context-switch')).not.toThrow()
    // Holds until reload even though storage refused it.
    expect(isCloudHintDismissed('context-switch')).toBe(true)
    expect(() => recordContextSwitch('a', 'b')).not.toThrow()
  })

  it('hides an issue prompt only for that issue on that context', () => {
    hideIssueHint(issueHintKey('prod', 'issue-1'))
    expect(isIssueHintHidden(issueHintKey('prod', 'issue-1'))).toBe(true)
    expect(isIssueHintHidden(issueHintKey('staging', 'issue-1'))).toBe(false)
    expect(isIssueHintHidden(issueHintKey('prod', 'issue-2'))).toBe(false)
  })
})

describe('context switch log', () => {
  it('records real switches and announces each one', () => {
    const onSwitch = vi.fn()
    window.addEventListener(CONTEXT_SWITCHED_EVENT, onSwitch)
    recordContextSwitch(undefined, 'prod', 500)
    recordContextSwitch('prod', 'staging', 1000)
    recordContextSwitch('staging', 'staging', 2000)
    window.removeEventListener(CONTEXT_SWITCHED_EVENT, onSwitch)
    expect(readSwitchLog()).toEqual([{ at: 1000, from: 'prod', to: 'staging' }])
    expect(onSwitch).toHaveBeenCalledTimes(1)
  })

  it('fills in where a quick follow-up switch started from', () => {
    recordContextSwitch('prod', 'staging', 1000)
    recordContextSwitch(undefined, 'prod', 2000)
    expect(readSwitchLog()).toEqual([
      { at: 1000, from: 'prod', to: 'staging' },
      { at: 2000, from: 'staging', to: 'prod' },
    ])
  })

  it('stores the compared pair once the third switch lands', () => {
    recordContextSwitch('prod', 'staging', 1000)
    recordContextSwitch('staging', 'prod', 2000)
    expect(readTriggeredPair()).toBeNull()
    recordContextSwitch('prod', 'staging', 3000)
    expect(readTriggeredPair()).toEqual(['prod', 'staging'])
  })

  it('counts only switches inside the window', () => {
    const now = 100 * 60 * 1000
    const log = [
      { at: now - CONTEXT_SWITCH_WINDOW_MS - 1, from: 'a', to: 'b' },
      { at: now - 5000, from: 'b', to: 'a' },
      { at: now, from: 'a', to: 'b' },
    ]
    expect(recentSwitches(log, now)).toHaveLength(2)
  })

  it('names the two clusters being compared, latest destination last', () => {
    expect(
      comparedContexts([
        { at: 1, from: 'prod', to: 'staging' },
        { at: 2, from: 'staging', to: 'prod' },
        { at: 3, from: 'prod', to: 'staging' },
      ]),
    ).toEqual(['prod', 'staging'])
    expect(comparedContexts([])).toBeNull()
  })
})
