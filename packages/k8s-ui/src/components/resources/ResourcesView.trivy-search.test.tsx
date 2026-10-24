// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()

const group = 'aquasecurity.github.io'
const vulnReports: APIResource = { group, version: 'v1alpha1', kind: 'VulnerabilityReport', name: 'vulnerabilityreports', namespaced: true, isCrd: true, verbs: ['get', 'list'] }
const report = (name: string, vulnerabilityID: string) => ({
  apiVersion: `${group}/v1alpha1`, kind: 'VulnerabilityReport',
  metadata: { name, namespace: 'legacy', uid: name },
  report: { artifact: { repository: 'trafex/php-nginx', tag: '3.6.0' }, vulnerabilities: [{ vulnerabilityID, resource: 'php83', installedVersion: '8.3.20-r0', fixedVersion: '8.3.36-r0' }] },
})
// The match sorts last, so selecting the first row proves the filter ran.
const billing = report('replicaset-billing-5cfccf7776-php-nginx', 'CVE-2025-0001')
const web = report('replicaset-web-7685556d86-php', 'CVE-2026-109618')

let root: ReturnType<typeof createRoot>
let host: HTMLDivElement
afterEach(() => { act(() => root?.unmount()); host?.remove(); localStorage.clear(); window.history.replaceState(null, '', '/') })

function render(search: string, onResourceClick = vi.fn()) {
  const locationSearch = `?apiGroup=${group}&search=${encodeURIComponent(search)}`
  window.history.replaceState(null, '', `/resources/vulnerabilityreports${locationSearch}`)
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  act(() => root.render(<KeyboardShortcutProvider><ResourcesView
    namespaces={[]}
    apiResources={[vulnReports]}
    resourceCounts={{ VulnerabilityReport: 2 }}
    selectedKindQuery={{ isLoading: false, data: [web, billing], resourceName: 'vulnerabilityreports', group }}
    locationPathname="/resources/vulnerabilityreports"
    locationSearch={locationSearch}
    onNavigate={() => {}}
    onResourceClick={onResourceClick}
  /></KeyboardShortcutProvider>))
  return onResourceClick
}

describe('Trivy report table search', () => {
  it('finds a report by a CVE in its findings', () => {
    const selected = render('CVE-2026-109618')
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'j', bubbles: true })))
    act(() => document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })))
    expect(selected).toHaveBeenCalledTimes(1)
    expect(selected).toHaveBeenCalledWith(expect.objectContaining({ name: web.metadata.name }))
  })

  it('says what it searched when nothing matches', () => {
    render('CVE-1999-0001')
    expect(host.textContent).toContain('No results for "CVE-1999-0001"')
    expect(host.textContent).toContain('Searched names, namespaces and report findings')
  })
})
