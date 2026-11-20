// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({
  launch: undefined as undefined | { enabled: boolean; url: string; start: string; end: string },
  mode: 'local',
  refetch: vi.fn(),
  usageOnScreen: false,
}))
vi.mock('../../api/client', () => ({
  useCapabilities: () => ({ data: { deployment: { mode: state.mode } } }),
  useVersionCheck: () => ({ data: { currentVersion: '1.18.3', updateAvailable: false, installMethod: 'direct', productHuntLaunch: state.launch }, isPending: false, refetch: state.refetch }),
}))
vi.mock('../usage-data/UsageDataPrompt', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../usage-data/UsageDataPrompt')>()),
  usagePromptOnScreen: () => state.usageOnScreen,
}))
vi.mock('../../api/usage-data', () => ({ useSetUsageData: () => ({ mutate: vi.fn(), isPending: false }), useMarkUsagePromptShown: () => vi.fn() }))

import { ProductHuntNotice } from './ProductHuntNotice'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let element: HTMLDivElement

const ON = { enabled: true, url: 'https://www.producthunt.com/products/radar-7?launch=radar-101', start: '2026-10-29T07:00:00Z', end: '2026-10-30T07:00:00Z' }
const OFF = { enabled: false, url: '', start: '0001-01-01T00:00:00Z', end: '0001-01-01T00:00:00Z' }

function render() {
  act(() => root.render(<ProductHuntNotice usageData={undefined} usageDataPending={false} />))
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-29T17:00:00Z'))
  localStorage.clear()
  state.launch = ON
  state.mode = 'local'
  state.refetch = vi.fn()
  state.usageOnScreen = false
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(() => {
  act(() => root.unmount())
  element.remove()
  vi.useRealTimers()
})

describe('ProductHuntNotice', () => {
  it('shows the launch-day card after the delay', () => {
    render()
    expect(element.textContent).toBe('')
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(element.textContent).toContain('Live on Product Hunt today')
    expect(element.querySelector('a')?.href).toContain('utm_source=radar-oss')
  })

  it('fades out without breaking when the launch is called off on screen', () => {
    render()
    act(() => { vi.advanceTimersByTime(10_000) })
    state.launch = OFF
    render()
    expect(element.textContent).toContain('Live on Product Hunt today')
    act(() => { vi.advanceTimersByTime(1_000) })
    expect(element.textContent).toBe('')
  })

  it('keeps the launch-day wording while it fades at the end of the day', () => {
    render()
    act(() => { vi.advanceTimersByTime(10_000) })
    vi.setSystemTime(new Date('2026-10-30T07:00:30Z'))
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(element.textContent).not.toContain('Launching on Product Hunt')
  })

  it('shows in-cluster only on the day', () => {
    state.mode = 'in-cluster'
    vi.setSystemTime(new Date('2026-10-28T17:00:00Z'))
    render()
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(element.textContent).toBe('')
  })

  it('re-checks the launch hourly while a page stays open', () => {
    render()
    expect(state.refetch).not.toHaveBeenCalled()
    act(() => { vi.advanceTimersByTime(60 * 60_000) })
    expect(state.refetch).toHaveBeenCalledTimes(1)
  })

  it('stays off the corner when re-enabled while the usage prompt is open', () => {
    state.launch = OFF
    render()
    state.usageOnScreen = true
    act(() => { window.dispatchEvent(new Event('radar:usage-prompt-visibility')) })
    state.launch = ON
    render()
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(element.textContent).toBe('')
  })

  it('comes back at once when reopened from the sidebar after being closed', () => {
    render()
    act(() => { vi.advanceTimersByTime(10_000) })
    const close = element.querySelector('button[aria-label="Close"]') as HTMLButtonElement
    act(() => { close.click() })
    act(() => { vi.advanceTimersByTime(1_000) })
    expect(element.textContent).toBe('')
    act(() => { window.dispatchEvent(new Event('radar:product-hunt-notice-reopen')) })
    expect(element.textContent).toContain('Live on Product Hunt today')
    act(() => { (element.querySelector('button[aria-label="Close"]') as HTMLButtonElement).click() })
    act(() => { vi.advanceTimersByTime(1_000) })
    expect(element.textContent).toBe('')
  })
})
