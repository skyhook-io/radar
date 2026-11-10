// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { FilterRailPanel } from './FilterRailPanel'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
afterEach(() => { vi.unstubAllGlobals() })

function stubPhone(matches: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({ matches, media: query, addEventListener: () => {}, removeEventListener: () => {} }))
}

function render(activeCount: number) {
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<FilterRailPanel activeCount={activeCount} clear={<button type="button">Clear</button>}><div>Status facet</div></FilterRailPanel>))
  return host
}

it('keeps the plain header and an open body off a phone', () => {
  stubPhone(false)
  const host = render(2)
  expect(host.querySelector('[aria-expanded]')).toBeNull()
  expect(host.textContent).toContain('Status facet')
  expect(host.textContent).not.toContain('2')
})

it('collapses the facets behind a Filters button with the active count on a phone', () => {
  stubPhone(true)
  const host = render(2)
  const button = host.querySelector<HTMLButtonElement>('button[aria-expanded]')!
  expect(button.textContent).toContain('Filters')
  expect(button.textContent).toContain('2')
  expect(button.getAttribute('aria-expanded')).toBe('false')
  const panel = host.querySelector(`#${button.getAttribute('aria-controls')}`)!
  expect(panel.querySelector('[inert]')).not.toBeNull()
  act(() => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('true')
  expect(panel.querySelector('[inert]')).toBeNull()
})
