// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { CNPGOperatorConditions } from './CNPGOperatorConditions'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('starts folded and keeps every condition field, including unknown status and exact transition', () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-04T13:34:56Z'))
  const host = document.createElement('div')
  const root = createRoot(host)
  act(() => root.render(<CNPGOperatorConditions conditions={[
    { type: 'Ready', status: 'False', reason: 'Waiting', message: 'Waiting for the primary', lastTransitionTime: '2026-10-04T12:34:56Z' },
    { type: 'ContinuousArchiving', status: 'Unknown', reason: 'Checking', message: 'Archive not checked yet' },
  ]} />))
  const button = host.querySelector('button')!
  expect(button.textContent).toContain('Operator conditions')
  expect(button.textContent).toContain('2 reported')
  expect(button.getAttribute('aria-expanded')).toBe('false')
  act(() => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('true')
  for (const text of ['Ready', 'False', 'Waiting', 'Waiting for the primary', '1h ago', 'ContinuousArchiving', 'Unknown', 'Checking', 'Archive not checked yet', 'Last transition: Not reported']) expect(host.textContent).toContain(text)
  expect(host.querySelector('time')?.getAttribute('datetime')).toBe('2026-10-04T12:34:56Z')
  const badge = [...host.querySelectorAll('.badge-sm')].find((b) => b.textContent === 'False')
  expect(badge).toBeDefined()
  expect(badge?.className).not.toMatch(/success|warning|error/)
  expect(badge?.className).toContain('bg-theme-hover/50 text-theme-text-secondary border-theme-border')
  expect(badge?.className).not.toMatch(/emerald|amber|orange|red|sky-/)
  const time = host.querySelector('time')!
  act(() => time.parentElement!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
  act(() => vi.advanceTimersByTime(1000))
  expect(document.body.textContent).toContain('2026-10-04T12:34:56Z')
  vi.useRealTimers()
  act(() => root.unmount())
})

it('states that conditions have not been reported', () => {
  const host = document.createElement('div')
  const root = createRoot(host)
  act(() => root.render(<CNPGOperatorConditions conditions={[]} />))
  expect(host.textContent).toContain('No conditions reported')
  expect(host.querySelector('button')!.getAttribute('aria-expanded')).toBe('false')
  act(() => root.unmount())
})
