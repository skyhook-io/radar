// @vitest-environment jsdom
//
// Local timestamp choices name the browser zone used to display logs.
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { LogCore } from './LogCore'

vi.mock('react-virtuoso', () => ({ Virtuoso: ({ data, itemContent }: { data: unknown[]; itemContent: (index: number, value: unknown) => ReactNode }) => <div>{data.map((value, index) => <div key={index}>{itemContent(index, value)}</div>)}</div> }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  localStorage.removeItem('radar-logs-ts-format')
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

it('names the zone beside local formats in the timestamp menu', async () => {
  const noop = () => {}
  await act(async () => root.render(<LogCore entries={[]} isLoading={false} isStreaming={false} onStopStream={noop} onRefresh={noop} onDownload={noop} />))
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const picker = element.querySelector<HTMLButtonElement>('button[aria-label="Pick timestamp format"]')!
  expect(picker.textContent).toBe('Local time')
  await act(async () => picker.click())
  const labels = Array.from(element.querySelectorAll('button'), (button) => button.textContent)
  expect(labels.filter((label) => label?.includes('(local)'))).toEqual([`Time (local)${zone}✓`, `ISO (local)${zone}`])
  const time = element.querySelector<HTMLButtonElement>(`button[aria-label="Time (local), ${zone}"]`)!
  const iso = element.querySelector<HTMLButtonElement>(`button[aria-label="ISO (local), ${zone}"]`)!
  expect(time.getAttribute('aria-pressed')).toBe('true')
  expect(iso.getAttribute('aria-pressed')).toBe('false')
  await act(async () => iso.click())
  await act(async () => picker.click())
  expect(element.querySelector(`button[aria-label="ISO (local), ${zone}"]`)?.getAttribute('aria-pressed')).toBe('true')
  expect(labels).toContain('Relative')
})

it('uses the current time zone when the picker opens after a system change', async () => {
  const noop = () => {}
  await act(async () => root.render(<LogCore entries={[]} isLoading={false} isStreaming={false} onStopStream={noop} onRefresh={noop} onDownload={noop} />))
  const picker = element.querySelector<HTMLButtonElement>('button[aria-label="Pick timestamp format"]')!
  const initial = Intl.DateTimeFormat().resolvedOptions()
  const nextZone = initial.timeZone === 'Asia/Tokyo' ? 'America/New_York' : 'Asia/Tokyo'
  const resolvedOptions = vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockReturnValue({ ...initial, timeZone: nextZone })
  try {
    await act(async () => picker.click())
    expect([...element.querySelectorAll('button')].map((button) => button.textContent)).toContain(`Time (local)${nextZone}✓`)
  } finally {
    resolvedOptions.mockRestore()
  }
})

it('uses the current time zone when the format tooltip opens', async () => {
  const noop = () => {}
  await act(async () => root.render(<LogCore entries={[]} isLoading={false} isStreaming={false} onStopStream={noop} onRefresh={noop} onDownload={noop} />))
  const picker = element.querySelector<HTMLButtonElement>('button[aria-label="Pick timestamp format"]')!
  const initial = Intl.DateTimeFormat().resolvedOptions()
  const nextZone = initial.timeZone === 'Asia/Tokyo' ? 'America/New_York' : 'Asia/Tokyo'
  const resolvedOptions = vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockReturnValue({ ...initial, timeZone: nextZone })
  vi.useFakeTimers()
  try {
    act(() => picker.focus())
    act(() => { vi.advanceTimersByTime(200) })
    expect(document.body.textContent).toContain(`Timestamp format: Time (local) · ${nextZone}`)
  } finally {
    resolvedOptions.mockRestore()
    vi.useRealTimers()
  }
})
