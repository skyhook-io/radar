// @vitest-environment jsdom
import { act, type ComponentProps, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LogCore } from './LogCore'

vi.mock('react-virtuoso', () => ({ Virtuoso: ({ data, itemContent }: { data: unknown[]; itemContent: (index: number, value: unknown) => ReactNode }) => <div>{data.map((value, index) => <div key={index}>{itemContent(index, value)}</div>)}</div> }))

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
