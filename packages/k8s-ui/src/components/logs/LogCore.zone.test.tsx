// @vitest-environment jsdom
//
// Local timestamp choices name the browser zone used to display logs.
import { act, type ComponentProps, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { LogCore } from './LogCore'

vi.mock('react-virtuoso', () => ({ Virtuoso: ({ data, itemContent }: { data: unknown[]; itemContent: (index: number, value: unknown) => ReactNode }) => <div>{data.map((value, index) => <div key={index}>{itemContent(index, value)}</div>)}</div> }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  localStorage.removeItem('radar-logs-ts-format')
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

const noop = () => {}
const entry = {
  id: 1, timestamp: '2026-11-01T05:30:00Z', content: 'first line', container: 'app',
  isJson: false, isLogfmt: false, level: 'info', levelSource: 'keyword',
} satisfies ComponentProps<typeof LogCore>['entries'][number]

async function render(props: Partial<ComponentProps<typeof LogCore>> = {}) {
  await act(async () => root.render(
    <LogCore entries={[entry]} isLoading={false} isStreaming={false} isFollowing={false}
      onStopStream={noop} onRefresh={noop} onDownload={noop} {...props} />,
  ))
  return element.querySelector<HTMLButtonElement>('button[aria-label^="Pick timestamp format"]')!
}

let systemZone: string
function mockSystemZone() {
  systemZone = 'UTC'
  const DateTimeFormat = Intl.DateTimeFormat
  return vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(function (locales, options) {
    return new DateTimeFormat(locales, { ...options, timeZone: options?.timeZone ?? systemZone })
  })
}

const rowTime = (timestamp = entry.timestamp) => element.querySelector(`[title="${timestamp}"]`)?.textContent
const tooltipText = () => document.querySelector('[role="tooltip"]')?.textContent

it('moves focus and selection with the arrow keys and returns focus on confirmation or Escape', async () => {
  localStorage.setItem('radar-logs-ts-format', 'iso-local')
  const picker = await render()
  const press = (key: string) => act(() => {
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  })
  act(() => picker.focus())
  press('Enter')
  const radios = element.querySelectorAll<HTMLButtonElement>('[role="radio"]')
  const expectSelected = (index: number) => {
    expect(document.activeElement).toBe(radios[index])
    expect(Array.from(radios).filter(radio => radio.getAttribute('aria-checked') === 'true')).toEqual([radios[index]])
    expect(Array.from(radios).filter(radio => radio.tabIndex === 0)).toEqual([radios[index]])
  }
  expectSelected(2)
  press('ArrowUp')
  expectSelected(1)
  press('ArrowLeft')
  expectSelected(0)
  press('ArrowUp')
  expectSelected(5)
  press('ArrowRight')
  expectSelected(0)
  press('ArrowDown')
  expectSelected(1)
  press(' ')
  expect(picker.getAttribute('aria-expanded')).toBe('false')
  expect(document.activeElement).toBe(picker)
  expect(picker.getAttribute('aria-label')).toBe('Pick timestamp format: UTC time')
  press('ArrowDown')
  expectSelected(1)
  press('Escape')
  expect(picker.getAttribute('aria-expanded')).toBe('false')
  expect(document.activeElement).toBe(picker)
})

it('dismisses the pointer-opened picker before the dock handles Escape when focus is on the body', async () => {
  const picker = await render()
  const dockKeyDown = vi.fn()
  window.addEventListener('keydown', dockKeyDown)
  try {
    act(() => picker.click())
    expect(picker.getAttribute('aria-expanded')).toBe('true')
    expect(document.activeElement).toBe(document.body)
    const pressEscape = () => act(() => {
      document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    })
    pressEscape()
    expect(picker.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(picker)
    expect(dockKeyDown).not.toHaveBeenCalled()
    pressEscape()
    expect(dockKeyDown).toHaveBeenCalledTimes(1)
  } finally {
    window.removeEventListener('keydown', dockKeyDown)
  }
})

it('keeps Epoch reachable in a short dock as the toolbar wraps and the viewport shrinks', async () => {
  const resizeCallbacks = new Map<Element, () => void>()
  vi.stubGlobal('ResizeObserver', class {
    constructor(private callback: () => void) {}
    observe(target: Element) { resizeCallbacks.set(target, this.callback) }
    disconnect() {}
  })
  const picker = await render()
  const viewer = element.firstElementChild!
  const toolbar = picker.closest('[aria-label="Log display and utilities"]')!.parentElement!
  vi.spyOn(viewer, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 400, 390, 275))
  const anchorRect = vi.spyOn(picker.closest('.relative')!, 'getBoundingClientRect').mockReturnValue(new DOMRect(90, 442, 72, 24))
  act(() => picker.click())
  const menu = element.querySelector<HTMLElement>('[role="radiogroup"][aria-label="Timestamp format"]')!
  expect(menu.style.maxHeight).toBe('201px')
  expect(menu.style.maxWidth).toBe('158px')
  expect(menu.classList.contains('overflow-y-auto')).toBe(true)
  expect(menu.querySelector('[role="radio"]:last-child')?.textContent).toBe('Epoch')
  await render({ entries: [entry, { ...entry, id: 2, isJson: true, content: '{"msg":"structured"}' }] })
  expect(toolbar.querySelector('[aria-label="Pick structured log display mode"]')).toBeTruthy()
  anchorRect.mockReturnValue(new DOMRect(90, 478, 72, 24))
  act(() => resizeCallbacks.get(toolbar)?.())
  expect(menu.style.maxHeight).toBe('165px')
  vi.stubGlobal('innerHeight', 620)
  act(() => window.dispatchEvent(new Event('resize')))
  expect(menu.style.maxHeight).toBe('110px')
})

it.each(['', undefined])('omits an unknown zone (%s) from local choices and the tooltip', async timeZone => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  const resolved = Intl.DateTimeFormat().resolvedOptions()
  vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockReturnValue(Object.defineProperty(resolved, 'timeZone', { value: timeZone }))
  mockSystemZone()
  const picker = await render()
  expect(rowTime()).toBe('05:30:00')
  expect(picker.getAttribute('aria-label')).toBe('Pick timestamp format: Local time')
  act(() => picker.focus())
  act(() => { vi.advanceTimersByTime(200) })
  expect(tooltipText()).toBe('Timestamp format: Time (local)')
  act(() => picker.click())
  const localOptions = Array.from(element.querySelectorAll('[role="radio"]')).filter(option => option.textContent?.includes('(local)'))
  expect(localOptions).toHaveLength(2)
  for (const option of localOptions) {
    expect(option.hasAttribute('aria-label')).toBe(false)
    expect(option.querySelector('span span')).toBeNull()
  }
})

it.each([
  ['time-local', '00:00:00'],
  ['iso-local', '2026-11-02T00:00:00+09:00'],
])('uses an explicit midnight hour of 00 for %s', async (format, expected) => {
  const dateTimeFormat = mockSystemZone()
  systemZone = 'Asia/Tokyo'
  localStorage.setItem('radar-logs-ts-format', format)
  const timestamp = '2026-11-01T15:00:00Z'
  await render({ entries: [{ ...entry, timestamp }] })
  expect(dateTimeFormat).toHaveBeenCalledWith('en-US', expect.objectContaining({ hourCycle: 'h23' }))
  expect(rowTime(timestamp)).toBe(expected)
})

it('labels all six timestamp choices and exposes the selected format when closed', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  mockSystemZone()
  systemZone = 'Asia/Tokyo'
  const picker = await render()
  expect(picker.textContent).toBe('Local time')
  expect(picker.getAttribute('aria-label')).toBe('Pick timestamp format: Local time · Asia/Tokyo')
  expect(picker.getAttribute('aria-expanded')).toBe('false')

  act(() => picker.focus())
  act(() => picker.click())
  act(() => { vi.advanceTimersByTime(200) })
  expect(tooltipText()).toBeUndefined()
  expect(document.activeElement).toBe(picker)
  expect(picker.getAttribute('aria-expanded')).toBe('true')
  const group = element.querySelector('[role="radiogroup"][aria-label="Timestamp format"]')!
  const options = Array.from(group.querySelectorAll<HTMLButtonElement>('[role="radio"]'))
  expect(options.map(option => option.textContent)).toEqual([
    'Time (local)Asia/Tokyo✓', 'Time (UTC)', 'ISO (local)Asia/Tokyo', 'ISO (UTC)', 'Relative', 'Epoch',
  ])
  expect(options.map(option => option.getAttribute('aria-label') ?? option.textContent)).toEqual([
    'Time (local), Asia/Tokyo', 'Time (UTC)', 'ISO (local), Asia/Tokyo', 'ISO (UTC)', 'Relative', 'Epoch',
  ])
  expect(options.filter(option => option.getAttribute('aria-checked') === 'true')).toEqual([options[0]])
  act(() => options[1].click())
  expect(picker.getAttribute('aria-expanded')).toBe('false')
  expect(picker.getAttribute('aria-label')).toBe('Pick timestamp format: UTC time')
  act(() => picker.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
  act(() => { vi.advanceTimersByTime(200) })
  expect(tooltipText()).toBe('Timestamp format: Time (UTC)')
  act(() => picker.click())
  const checked = element.querySelectorAll('[role="radiogroup"][aria-label="Timestamp format"] [aria-checked="true"]')
  expect(Array.from(checked, option => option.textContent)).toEqual(['Time (UTC)✓'])
})

it.each(['focus', 'hover'])('updates the paused row and tooltip together on %s', async interaction => {
  vi.useFakeTimers()
  mockSystemZone()
  const picker = await render()
  expect(rowTime()).toBe('05:30:00')
  systemZone = 'Asia/Tokyo'
  act(() => {
    if (interaction === 'focus') picker.focus()
    else picker.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
  })
  act(() => { vi.advanceTimersByTime(200) })
  expect(tooltipText()).toBe('Timestamp format: Time (local) · Asia/Tokyo')
  expect(rowTime()).toBe('14:30:00')
})

it('refreshes ISO local rows on open and preserves each row’s daylight-saving offset', async () => {
  mockSystemZone()
  localStorage.setItem('radar-logs-ts-format', 'iso-local')
  const second = { ...entry, id: 2, timestamp: '2026-11-01T06:30:00Z' }
  const picker = await render({ entries: [entry, second] })
  expect(rowTime()).toBe('2026-11-01T05:30:00+00:00')
  systemZone = 'America/New_York'
  act(() => picker.click())
  expect(element.querySelector('[aria-label="ISO (local), America/New_York"]')).toBeTruthy()
  expect(rowTime()).toBe('2026-11-01T01:30:00-04:00')
  expect(rowTime(second.timestamp)).toBe('2026-11-01T01:30:00-05:00')
})

it('reuses the displayed zone and formatter for incoming lines with the picker open', async () => {
  const dateTimeFormat = mockSystemZone()
  const picker = await render({ isStreaming: true, isFollowing: true })
  act(() => picker.click())
  dateTimeFormat.mockClear()
  systemZone = 'Asia/Tokyo'
  const second = { ...entry, id: 2, timestamp: '2026-11-01T06:30:00Z' }
  await render({ isStreaming: true, isFollowing: true, entries: [entry, second] })
  expect(rowTime()).toBe('05:30:00')
  expect(rowTime(second.timestamp)).toBe('06:30:00')
  expect(dateTimeFormat).not.toHaveBeenCalled()
})

it.each(['focus', 'visibilitychange'])('refreshes a running stream on %s with the picker closed', async event => {
  const dateTimeFormat = mockSystemZone()
  const streaming = { isStreaming: true, isFollowing: true }
  const picker = await render(streaming)
  expect(rowTime()).toBe('05:30:00')
  systemZone = 'Asia/Tokyo'
  if (event === 'focus') {
    act(() => window.dispatchEvent(new Event('focus')))
  } else {
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    expect(rowTime()).toBe('05:30:00')
    visibility.mockReturnValue('visible')
    act(() => document.dispatchEvent(new Event('visibilitychange')))
  }
  expect(picker.getAttribute('aria-expanded')).toBe('false')
  expect(picker.getAttribute('aria-label')).toBe('Pick timestamp format: Local time · Asia/Tokyo')
  expect(rowTime()).toBe('14:30:00')
  dateTimeFormat.mockClear()
  const second = { ...entry, id: 2, timestamp: '2026-11-01T06:30:00Z' }
  await render({ ...streaming, entries: [entry, second] })
  expect(rowTime(second.timestamp)).toBe('15:30:00')
  expect(dateTimeFormat).not.toHaveBeenCalled()
})
