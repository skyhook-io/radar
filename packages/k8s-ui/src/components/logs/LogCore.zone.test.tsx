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
    return new DateTimeFormat(locales, { timeZone: systemZone, ...options })
  })
}

const rowTime = (timestamp = entry.timestamp) => element.querySelector(`[title="${timestamp}"]`)?.textContent
const tooltipText = () => document.querySelector('[role="tooltip"]')?.textContent

it('names the zone beside local formats in the timestamp menu', async () => {
  const picker = await render()
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
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
