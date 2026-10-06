// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { CNPGWALArchivingFact } from './CNPGClusterSummary'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
it('folds the raw condition and exposes relative age with the exact timestamp on hover', () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-01T13:00:00Z'))
  const stamp = '2026-10-01T12:00:00Z'
  const host = document.createElement('div'); document.body.append(host); const root = createRoot(host)
  act(() => root.render(<CNPGWALArchivingFact fact={{ state: 'no_destination' as const, text: 'Not archived: no destination configured', tone: 'neutral', detail: 'WAL is not archived to recovery storage, so point-in-time recovery is unavailable.', operatorCondition: { type: 'ContinuousArchiving', status: 'True', message: 'Continuous archiving is working', lastTransitionTime: stamp } }} />))
  expect(host.textContent).toContain('WAL is not archived to recovery storage')
  const button = host.querySelector('button')!
  expect(button.getAttribute('aria-expanded')).toBe('false')
  const panel = document.getElementById(button.getAttribute('aria-controls')!)!
  expect(panel.firstElementChild!.hasAttribute('inert')).toBe(true)
  act(() => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('true')
  expect(host.textContent).toContain('ContinuousArchiving: True')
  expect(host.textContent).toContain('Continuous archiving is working')
  expect(host.textContent).toContain('1h ago')
  expect(host.querySelector('time')?.dateTime).toBe(stamp)
  act(() => { host.querySelector('time')!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); vi.advanceTimersByTime(350) })
  expect(document.body.textContent).toContain(stamp)
  act(() => root.unmount()); host.remove(); vi.useRealTimers()
})

it('keeps the compact verdict, consequence and per-row operator disclosure', () => {
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<CNPGWALArchivingFact compact fact={{ state: 'no_destination' as const, text: 'Archive destination absent', tone: 'neutral', detail: 'CloudNativePG still reports archiving as working because it accepts each WAL file without keeping it.', operatorCondition: { type: 'ContinuousArchiving', status: 'True', message: 'working' } }} />))
  expect(host.textContent).toContain('Archive destination absent')
  expect(host.textContent).toContain('No point-in-time recovery')
  expect(host.textContent).not.toContain('because')
  const fold = host.querySelector('button')!
  expect(fold.textContent).toContain('Operator report')
  act(() => fold.click())
  expect(host.textContent).toContain('ContinuousArchiving: True')
  act(() => root.unmount())
})
