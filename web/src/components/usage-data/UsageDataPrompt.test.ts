import { afterEach, describe, expect, it, vi } from 'vitest'
import type { UsageDataStatus } from '../../api/usage-data'

vi.mock('../../api/client', () => ({ useVersionCheck: () => ({ data: undefined }) }))
vi.mock('../../api/usage-data', () => ({ useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }), markUsagePromptShown: vi.fn() }))

import { shouldShowFirstRunPrompt, updateNoticeVisible } from './UsageDataPrompt'

const status = (firstRunPrompt: boolean) => ({ firstRunPrompt } as UsageDataStatus)

describe('shouldShowFirstRunPrompt', () => {
  it('shows only when the server offers it, after the delay, with the corner free', () => {
    expect(shouldShowFirstRunPrompt({ status: status(true), ready: true, updateNoticeShowing: false })).toBe(true)
    expect(shouldShowFirstRunPrompt({ status: status(false), ready: true, updateNoticeShowing: false })).toBe(false)
    expect(shouldShowFirstRunPrompt({ status: status(true), ready: false, updateNoticeShowing: false })).toBe(false)
    expect(shouldShowFirstRunPrompt({ status: status(true), ready: true, updateNoticeShowing: true })).toBe(false)
    expect(shouldShowFirstRunPrompt({ status: undefined, ready: true, updateNoticeShowing: false })).toBe(false)
  })
})

describe('updateNoticeVisible', () => {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
  })
  afterEach(() => store.clear())

  it('is visible until that version is dismissed', () => {
    expect(updateNoticeVisible('1.16.0', true)).toBe(true)
    store.set('radar-update-dismissed', '1.16.0')
    expect(updateNoticeVisible('1.16.0', true)).toBe(false)
    expect(updateNoticeVisible('1.16.0', false)).toBe(false)
  })
})
