// @vitest-environment jsdom
import { act, type ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isHostManagedSource, SOURCE_META, SOURCE_ORDER } from '../../utils/applications'
import { ProvenanceBadge } from './AppChips'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root | undefined
function render(el: ReactElement) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() => root!.render(el))
  return host
}
afterEach(() => {
  act(() => root?.unmount())
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('ProvenanceBadge', () => {
  it('shows grouping evidence for a cluster-derived source', () => {
    expect(render(<ProvenanceBadge tier={3} appKey="argocd/web" confidence="high" />).textContent).toBe('Argo CD')
  })

  it('shows ungrouped when the app has no provenance', () => {
    expect(render(<ProvenanceBadge appKey="shop/web" />).textContent).toBe('ungrouped')
  })

  it.each([undefined, 3])('uses the managed source label for the badge and tooltip at tier %s', (tier) => {
    vi.useFakeTimers()
    const source = SOURCE_ORDER.find(isHostManagedSource)!
    const { label } = SOURCE_META[source]
    const host = render(<ProvenanceBadge tier={tier} appKey="shop/web" source={source} />)
    expect(host.textContent).toBe(label)
    act(() => host.firstElementChild!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
    act(() => vi.advanceTimersByTime(150))
    expect(document.body.lastElementChild?.textContent).toBe(`Managed by ${label}.`)
  })
})
