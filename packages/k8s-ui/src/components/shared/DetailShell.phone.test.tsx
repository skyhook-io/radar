// @vitest-environment jsdom
//
// On a phone, a tab that opts into a compact header shows the host's compact
// identity in place of the full one (whose links aren't rendered at all);
// the breadcrumb stays, since some hosts navigate through it.
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { DetailShell } from './DetailShell'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
afterEach(() => { vi.unstubAllGlobals() })

function render({ phone, activeTab }: { phone: boolean; activeTab: 'overview' | 'logs' }) {
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: phone, media: query, addEventListener: () => {}, removeEventListener: () => {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const tabs = [{ id: 'overview' as const, label: 'Overview' }, { id: 'logs' as const, label: 'Logs', phoneCompactHeader: true }]
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(
    <DetailShell
      breadcrumb={<nav>Workspace crumb</nav>}
      identity={<div>web <a href="#owner">Owner link</a></div>}
      compactIdentity={<span>web (compact)</span>}
      headerActions={<button type="button">Investigate</button>}
      subheader={<p>Status summary</p>}
      tabs={tabs}
      activeTab={activeTab}
      onTabChange={() => {}}
    >
      content
    </DetailShell>,
  ))
  const text = host.textContent ?? ''
  act(() => root.unmount())
  return text
}

it('shows only the compact identity on a phone while the tab opts in', () => {
  const text = render({ phone: true, activeTab: 'logs' })
  expect(text).toContain('web (compact)')
  expect(text).toContain('Workspace crumb')
  expect(text).not.toContain('Owner link')
  expect(text).not.toContain('Investigate')
  expect(text).not.toContain('Status summary')
})

it('keeps the full header on other tabs and off a phone', () => {
  for (const args of [{ phone: true, activeTab: 'overview' as const }, { phone: false, activeTab: 'logs' as const }]) {
    const text = render(args)
    expect(text).toContain('Owner link')
    expect(text).toContain('Investigate')
    expect(text).toContain('Status summary')
    expect(text).not.toContain('web (compact)')
  }
})
