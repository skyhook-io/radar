// @vitest-environment jsdom
//
// The slot hosts use to put something before an app's name, such as a star. It
// has to stay out of the row's way, and a host that doesn't use it sees no change.
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import { buildSingleAppEntry, type AppRow } from '../../utils/applications'
import { ApplicationsView } from './ApplicationsView'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const app = (name: string, identityKey?: string): AppRow => ({
  key: `shop/${name}`,
  name,
  namespace: 'shop',
  health: 'healthy',
  workloads: [{ kind: 'Deployment', namespace: 'shop', name, workload_class: 'service', health: 'healthy', ready: 1, desired: 1, restarts: 0 }],
  identity: identityKey ? { key: identityKey, env: name, confidence: 'high', evidence: 'declared source path' } : undefined,
})

let root: Root | undefined
function show(rows: AppRow[], props: Partial<Parameters<typeof ApplicationsView>[0]> = {}) {
  const onSelect = vi.fn()
  const host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  act(() => root!.render(
    <KeyboardShortcutProvider>
      <ApplicationsView entries={rows.map((row) => buildSingleAppEntry(row))} variant="single" onSelect={onSelect} {...props} />
    </KeyboardShortcutProvider>,
  ))
  return { host, onSelect }
}
const star = (onPress: (name: string) => void = () => {}) => (entry: { row: AppRow }, group?: { row: AppRow }[]): ReactNode => (
  <button type="button" data-testid="star" data-members={group?.length ?? 0} onClick={() => onPress(entry.row.name)}>
    star
  </button>
)
const rowOf = (host: Element, name: string) => [...host.querySelectorAll('tbody tr')].find((tr) => tr.textContent?.includes(name)) as HTMLElement

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} unobserve() {} })
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} }))
  Element.prototype.scrollIntoView = () => {}
})
afterEach(() => {
  act(() => root?.unmount())
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('ApplicationsView renderRowLeading', () => {
  it('renders before the name of each row', () => {
    const { host } = show([app('checkout')], { renderRowLeading: star() })
    const row = rowOf(host, 'checkout')
    const button = row.querySelector('[data-testid="star"]')!
    const name = [...row.querySelectorAll('span')].find((span) => span.textContent === 'checkout')!
    expect(button.compareDocumentPosition(name) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('passes a folded group its first member and all of them', () => {
    const { host } = show([app('checkout-dev', 'checkout'), app('checkout-prod', 'checkout')], { renderRowLeading: star() })
    const button = host.querySelector('tbody [data-testid="star"]')!
    expect(button.getAttribute('data-members')).toBe('2')
  })

  it("doesn't open the app when the slot is clicked", () => {
    const pressed = vi.fn()
    const { host, onSelect } = show([app('checkout')], { renderRowLeading: star(pressed) })
    act(() => rowOf(host, 'checkout').querySelector<HTMLElement>('[data-testid="star"]')!.click())
    expect(pressed).toHaveBeenCalledWith('checkout')
    expect(onSelect).not.toHaveBeenCalled()
    act(() => rowOf(host, 'checkout').click())
    expect(onSelect).toHaveBeenCalledWith('shop/checkout')
  })

  it('leaves Enter to whatever has focus in the slot', () => {
    const { host, onSelect } = show([app('checkout')], { renderRowLeading: star() })
    const press = (target: Element) => {
      const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
      act(() => { target.dispatchEvent(enter) })
      return enter
    }
    act(() => { document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })) })
    const enter = press(rowOf(host, 'checkout').querySelector('[data-testid="star"]')!)
    expect(onSelect).not.toHaveBeenCalled()
    expect(enter.defaultPrevented).toBe(false)
    press(document.body)
    expect(onSelect).toHaveBeenCalledWith('shop/checkout')
  })

  it('leaves no gap when the host has nothing for a row', () => {
    const { host } = show([app('checkout')], { renderRowLeading: () => null })
    const withSlot = rowOf(host, 'checkout').innerHTML
    act(() => root?.unmount())
    document.body.innerHTML = ''
    const { host: plain } = show([app('checkout')])
    expect(withSlot).toBe(rowOf(plain, 'checkout').innerHTML)
  })
})
