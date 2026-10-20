// @vitest-environment jsdom
import { act, type ComponentProps, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LogCore } from './LogCore'
import { copyText } from '../../utils/clipboard'

vi.mock('../../utils/clipboard', () => ({ copyText: vi.fn(async () => true) }))

let atBottom: ((bottom: boolean) => void) | undefined
let totalHeightChanged: (() => void) | undefined
vi.mock('react-virtuoso', () => ({ Virtuoso: ({ data, itemContent, atBottomStateChange, totalListHeightChanged, scrollerRef }: { data: unknown[]; itemContent: (index: number, value: unknown) => ReactNode; atBottomStateChange: (bottom: boolean) => void; totalListHeightChanged: () => void; scrollerRef: (element: HTMLElement | null) => void }) => {
  atBottom = atBottomStateChange
  totalHeightChanged = totalListHeightChanged
  return <div ref={scrollerRef} data-virtuoso-scroller>{data.map((value, index) => <div key={index}>{itemContent(index, value)}</div>)}</div>
} }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  localStorage.clear()
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})

const noop = () => {}
async function render(props: Partial<ComponentProps<typeof LogCore>>) {
  await act(async () =>
    root.render(
      <LogCore
        entries={[]}
        isLoading={false}
        isStreaming={false}
        onStopStream={noop}
        onRefresh={noop}
        onDownload={noop}
        {...props}
      />,
    ),
  )
}
const toggle = () => element.querySelector<HTMLButtonElement>('button[aria-label^="Switch log viewer"]')

describe('LogCore theme', () => {
  it('defaults to dark when no theme hint is given', async () => {
    await render({})
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to light mode')
  })

  it('follows defaultDark and keeps the toggle available', async () => {
    await render({ defaultDark: false })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to dark mode')
    await render({ defaultDark: true })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to light mode')
  })

  it('prefers the saved toggle choice over defaultDark', async () => {
    localStorage.setItem('radar-logs-dark', 'true')
    await render({ defaultDark: false })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to light mode')
  })

  it('returns to the saved choice when a forceDark pin is lifted', async () => {
    localStorage.setItem('radar-logs-dark', 'false')
    await render({ forceDark: true, defaultDark: false })
    expect(toggle()).toBeNull()
    await render({ defaultDark: false })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to dark mode')
  })

  it('ignores a saved value that is not a palette choice', async () => {
    localStorage.setItem('radar-logs-dark', 'garbage')
    await render({ defaultDark: false })
    expect(toggle()?.getAttribute('aria-label')).toBe('Switch log viewer to dark mode')
  })

  it('hides the toggle when forceDark is set', async () => {
    await render({ forceDark: true, defaultDark: false })
    expect(toggle()).toBeNull()
  })
})

it('dims unavailable streaming, explains why, suppresses its hint and keyboard action', async () => {
  vi.useFakeTimers()
  const start = vi.fn()
  await render({ onStartStream: start, sourceUnavailable: true })
  const stream = [...element.querySelectorAll('button')].find((b) => b.textContent === 'Stream')!
  expect(stream.disabled).toBe(true)
  expect(stream.classList.contains('disabled:opacity-50')).toBe(true)
  expect([...element.querySelectorAll('kbd')].some((k) => k.textContent === 'S')).toBe(false)
  act(() => { stream.parentElement!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); vi.advanceTimersByTime(500) })
  expect(document.body.textContent).toContain('Available once an instance is running')
  act(() => window.dispatchEvent(new KeyboardEvent('keydown', { key: 's' })))
  expect(start).not.toHaveBeenCalled()
  vi.useRealTimers()
})
it('keeps a non-CNPG caller streamable without Pods and wraps its utilities together', async () => {
  const start = vi.fn()
  await render({ onStartStream: start, onClear: noop, toolbarExtra: <span>Deployment nginx sources</span>, entries: [{ id: 1, content: '{"msg":"nginx started","level":"info"}', timestamp: '', container: 'nginx', pod: 'nginx-1', isJson: true, isLogfmt: false, level: 'info', levelSource: 'structured' }] })
  const stream = [...element.querySelectorAll('button')].find((b) => b.textContent === 'Stream')!
  expect(stream.disabled).toBe(false)
  expect([...element.querySelectorAll('kbd')].some((k) => k.textContent === 'S')).toBe(true)
  act(() => stream.click())
  expect(start).toHaveBeenCalledOnce()
  const utilities = element.querySelector('[aria-label="Log display and utilities"]')!
  expect(utilities.classList.contains('flex-nowrap')).toBe(true)
  expect(utilities.classList.contains('shrink-0')).toBe(true)
  for (const icon of ['lucide-trash-2', 'lucide-download', 'lucide-search', 'lucide-clock', 'lucide-braces']) expect(utilities.querySelector(`.${icon}`)).not.toBeNull()
})

it('still stops an existing stream when its source becomes unavailable', async () => {
  const stop = vi.fn()
  await render({ onStartStream: noop, onStopStream: stop, isStreaming: true, sourceUnavailable: true })
  const button = [...element.querySelectorAll('button')].find((b) => b.textContent === 'Stop')!
  expect(button.disabled).toBe(false)
  act(() => window.dispatchEvent(new KeyboardEvent('keydown', { key: 's' })))
  expect(stop).toHaveBeenCalledOnce()
})

it.each([true, false])('wraps non-CNPG plain logs at words with ANSI enabled=%s', async (ansi) => {
  localStorage.setItem('radar-logs-ansi', String(ansi))
  await render({ entries: [{ id: 1, timestamp: '', content: 'nginx serving requests', container: 'nginx', isJson: false, isLogfmt: false, level: 'info', levelSource: 'keyword' }] })
  const line = [...element.querySelectorAll('span')].find((el) => el.textContent === 'nginx serving requests' && el.classList.contains('whitespace-pre-wrap'))!
  expect(line.classList.contains('[overflow-wrap:anywhere]')).toBe(true)
  expect(line.classList.contains('break-all')).toBe(false)
})
it('keeps normal word wrapping when searching a structured log', async () => {
  await render({ entries: [{ id: 1, timestamp: '', content: '{"msg":"nginx serving requests"}', container: 'nginx', isJson: true, isLogfmt: false, level: 'info', levelSource: 'structured' }] })
  await act(async () => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'f', ctrlKey: true })))
  const input = element.querySelector<HTMLInputElement>('input[placeholder="Search logs..."]')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'nginx')
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  const highlight = element.querySelector('mark')!
  expect(highlight).not.toBeNull()
  expect(highlight.parentElement!.classList.contains('[overflow-wrap:anywhere]')).toBe(true)
  expect(highlight.parentElement!.classList.contains('break-all')).toBe(false)
})

it('disables empty Export and Clear while preserving Refresh and enables them after logs load', async () => {
  await render({ onClear: noop })
  const exportButton = () => element.querySelector<HTMLButtonElement>('[aria-label="Export logs"]')!
  const clear = () => element.querySelector<HTMLButtonElement>('[aria-label="Clear logs"]')!
  expect(exportButton().disabled).toBe(true)
  expect(clear().disabled).toBe(true)
  expect(element.querySelector<HTMLButtonElement>('[aria-label="Refresh logs"]')?.disabled).toBe(false)
  await render({ onClear: noop, entries: [{ id: 1, container: 'postgres', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false, content: 'hello', timestamp: '2026-10-01T00:00:00Z' }] })
  expect(exportButton().disabled).toBe(false)
  expect(clear().disabled).toBe(false)
})

it('keeps Export and Clear usable when a Pod filter hides a nonempty buffer', async () => {
  await render({ entries: [], allEntries: [{ id: 1, container: 'postgres', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false, content: 'hello', timestamp: '' }], onClear: noop })
  expect(element.querySelector<HTMLButtonElement>('[aria-label="Export logs"]')!.disabled).toBe(false)
  expect(element.querySelector<HTMLButtonElement>('[aria-label="Clear logs"]')!.disabled).toBe(false)
})

it('keeps return to live available when a Pod filter hides the saved reading window', async () => {
  const onFollowingChange = vi.fn()
  await render({ entries: [], isFollowing: false, pendingLineCount: 17, onFollowingChange })
  const button = element.querySelector<HTMLButtonElement>('[aria-label="Scroll to bottom"]')
  expect(button?.textContent).toContain('17 new lines')
  await act(async () => button?.click())
  expect(onFollowingChange).toHaveBeenCalledWith(true)
})

it('collects retained windows only when exporting and downloads both windows', async () => {
  const saved = { id: 1, container: 'app', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false, content: 'saved record', timestamp: '' } satisfies ComponentProps<typeof LogCore>['entries'][number]
  const live = { ...saved, id: 30, content: 'live record' }
  const getRetainedEntries = vi.fn(() => [saved, live])
  const onDownload = vi.fn()
  await render({ entries: [saved], getRetainedEntries, retainedLineCount: 2, missingLineCount: 28, onDownload })
  expect(getRetainedEntries).not.toHaveBeenCalled()
  await act(async () => element.querySelector<HTMLButtonElement>('[aria-label="Export logs"]')!.click())
  expect(getRetainedEntries).toHaveBeenCalled()
  const all = [...element.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find(button => button.textContent === 'All 2')!
  await act(async () => all.click())
  expect(element.textContent).toContain('Some logs between your saved view and the latest logs are no longer available.')
  const download = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Download')!
  await act(async () => download.click())
  expect(onDownload).toHaveBeenCalledWith(expect.objectContaining({ content: expect.stringContaining('saved record') }))
  expect(onDownload).toHaveBeenCalledWith(expect.objectContaining({ content: expect.stringContaining('live record') }))
  await act(async () => element.querySelector<HTMLButtonElement>('[aria-label="Export logs"]')!.click())
  const copy = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Copy')!
  await act(async () => copy.click())
  expect(copyText).toHaveBeenLastCalledWith(onDownload.mock.calls[0][0].content)

})

it('does not resume at the saved end while newer records wait outside the reading view', async () => {
  const onFollowingChange = vi.fn()
  const entry = { id: 1, timestamp: '', content: 'INFO saved', container: 'app', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false } satisfies ComponentProps<typeof LogCore>['entries'][number]
  await render({ entries: [entry], isFollowing: false, pendingLineCount: 300, onFollowingChange })
  const scroller = element.querySelector('[data-virtuoso-scroller]')!
  await act(async () => {
    scroller.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: 40 }))
    atBottom?.(true)
  })
  expect(onFollowingChange).not.toHaveBeenCalled()
  await render({ entries: [entry], isFollowing: false, pendingLineCount: 0, onFollowingChange })
  await act(async () => {
    scroller.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: 40 }))
    atBottom?.(true)
  })
  expect(onFollowingChange).toHaveBeenCalledWith(true)
})

it('pauses and reveals focused row controls without pausing for toolbar focus', async () => {
  const onFollowingChange = vi.fn()
  const reveal = vi.fn()
  const entry = { id: 1, timestamp: '', content: 'INFO saved', container: 'app', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false } satisfies ComponentProps<typeof LogCore>['entries'][number]
  await render({ entries: [entry], isFollowing: true, onFollowingChange })
  await act(async () => element.querySelector<HTMLButtonElement>('[aria-label="Refresh logs"]')!.focus())
  expect(onFollowingChange).not.toHaveBeenCalled()
  const copy = element.querySelector<HTMLButtonElement>('[title="Copy line"]')!
  copy.scrollIntoView = reveal
  await act(async () => copy.focus())
  expect(onFollowingChange).toHaveBeenCalledWith(false)
  expect(reveal).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })
})

it('pauses following before text selection or middle-button scrolling in the log area', async () => {
  const onFollowingChange = vi.fn()
  const entry = { id: 1, timestamp: '', content: 'INFO saved', container: 'app', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false } satisfies ComponentProps<typeof LogCore>['entries'][number]
  await render({ entries: [entry], isFollowing: true, onFollowingChange })
  const scroller = element.querySelector<HTMLElement>('[data-virtuoso-scroller]')!
  scroller.getBoundingClientRect = () => new DOMRect(0, 0, 500, 300)
  for (const button of [0, 1]) {
    onFollowingChange.mockClear()
    await act(async () => scroller.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, button, clientX: 100 })))
    expect(onFollowingChange).toHaveBeenCalledWith(false)
  }
})

it('schedules one bottom update per height change without polling idle frames', async () => {
  const frames: FrameRequestCallback[] = []
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.push(callback); return frames.length })
  vi.stubGlobal('cancelAnimationFrame', vi.fn())
  const entry = { id: 1, timestamp: '', content: 'INFO saved', container: 'app', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false } satisfies ComponentProps<typeof LogCore>['entries'][number]
  await render({ entries: [entry] })
  expect(frames).toHaveLength(1)
  await act(async () => frames.shift()?.(0))
  expect(frames).toHaveLength(0)
  await act(async () => { totalHeightChanged?.(); totalHeightChanged?.() })
  expect(frames).toHaveLength(1)
  await act(async () => frames.shift()?.(0))
  expect(frames).toHaveLength(0)
})

it('pauses for browser text selection inside logs but ignores selection outside them', async () => {
  const onFollowingChange = vi.fn()
  const entry = { id: 1, timestamp: '', content: 'INFO selected record', container: 'app', level: 'info', levelSource: 'keyword', isJson: false, isLogfmt: false } satisfies ComponentProps<typeof LogCore>['entries'][number]
  await render({ entries: [entry], isFollowing: true, onFollowingChange })
  const outside = document.createTextNode('outside logs')
  document.body.appendChild(outside)
  const selection = document.getSelection()!
  const range = document.createRange()
  range.selectNodeContents(outside)
  selection.removeAllRanges()
  selection.addRange(range)
  await act(async () => document.dispatchEvent(new Event('selectionchange')))
  expect(onFollowingChange).not.toHaveBeenCalled()
  const rowText = [...element.querySelectorAll('span')].find(span => span.textContent === entry.content)!
  range.selectNodeContents(rowText)
  selection.removeAllRanges()
  selection.addRange(range)
  await act(async () => document.dispatchEvent(new Event('selectionchange')))
  expect(onFollowingChange).toHaveBeenCalledWith(false)
  selection.removeAllRanges()
  outside.remove()
})
