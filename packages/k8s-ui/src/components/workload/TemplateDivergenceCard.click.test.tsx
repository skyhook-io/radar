// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import type { TemplateDivergence } from '../../types'
import { TemplateDivergenceCard } from './TemplateDivergenceCard'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const divergence: TemplateDivergence = {
  summary: '',
  podsCompared: 1,
  podsDiffering: 1,
  baseline: 'ReplicaSet web-1',
  differences: ['containers[app].resources.limits.memory is 16Mi; template specifies 256Mi'],
  sourceRefs: [{ kind: 'LimitRange', namespace: 'shop', name: 'defaults' }],
}

it('opens and closes from a keyboard-focusable button, and stays open for the same resource', async () => {
  sessionStorage.clear()
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  const mount = () => root.render(<TemplateDivergenceCard namespace="shop" divergence={divergence} resourceKey="/pods/shop/web-1-a" />)
  await act(async () => mount())
  const button = element.querySelector('button')!
  expect(button.getAttribute('aria-expanded')).toBe('false')
  expect(element.textContent).not.toContain('Could have changed it')
  button.focus()
  expect(document.activeElement).toBe(button)
  await act(async () => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('true')
  expect(element.textContent).toContain('Could have changed it')
  expect(document.getElementById(button.getAttribute('aria-controls')!)).not.toBeNull()

  // Reopening the drawer on the same resource keeps it open.
  await act(async () => root.unmount())
  const again = createRoot(element)
  await act(async () => again.render(<TemplateDivergenceCard namespace="shop" divergence={divergence} resourceKey="/pods/shop/web-1-a" />))
  expect(element.querySelector('button')!.getAttribute('aria-expanded')).toBe('true')
  await act(async () => element.querySelector('button')!.click())
  expect(element.querySelector('button')!.getAttribute('aria-expanded')).toBe('false')
  expect(element.textContent).not.toContain('Could have changed it')
  await act(async () => again.unmount())

  // Another resource starts collapsed.
  const other = createRoot(element)
  await act(async () => other.render(<TemplateDivergenceCard namespace="shop" divergence={divergence} resourceKey="/pods/shop/web-1-b" />))
  expect(element.querySelector('button')!.getAttribute('aria-expanded')).toBe('false')
  await act(async () => other.unmount())
})
