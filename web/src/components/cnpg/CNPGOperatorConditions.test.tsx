// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'
import { CNPGOperatorConditions } from './CNPGOperatorConditions'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('starts folded and keeps every condition field, including unknown status and exact transition', () => {
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
  for (const text of ['Ready', 'False', 'Waiting', 'Waiting for the primary', '2026-10-04T12:34:56Z', 'ContinuousArchiving', 'Unknown', 'Checking', 'Archive not checked yet', 'Last transition: Not reported']) expect(host.textContent).toContain(text)
  expect(host.querySelector('time')?.getAttribute('datetime')).toBe('2026-10-04T12:34:56Z')
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
