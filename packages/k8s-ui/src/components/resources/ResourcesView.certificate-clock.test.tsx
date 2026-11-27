// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { VirtuosoMockContext } from 'react-virtuoso'
import { afterEach, expect, it, vi } from 'vitest'
import { ResourcesView } from './ResourcesView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import type { APIResource } from '../../types'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
window.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })
Element.prototype.scrollIntoView = vi.fn()
afterEach(() => { vi.useRealTimers(); localStorage.clear() })

it.each(['secrets', 'certificates', 'knativecertificates'])('updates %s expiry without new resource data', async (name) => {
  const now = Date.parse('2026-11-27T00:00:00Z')
  vi.useFakeTimers().setSystemTime(now)
  const kind: APIResource = { group: name === 'secrets' ? '' : name === 'certificates' ? 'cert-manager.io' : 'networking.internal.knative.dev', version: 'v1', kind: name === 'secrets' ? 'Secret' : 'Certificate', name, namespaced: true, isCrd: name !== 'secrets', verbs: ['get', 'list'] }
  const expiry = new Date(now + 240_000).toISOString()
  const policy = { warningSeconds: 180, highSeconds: 60, liveWarningSeconds: 180 }
  const warningSelector = name === 'knativecertificates' ? '.status-degraded' : '.text-yellow-400'
  const highSelector = name === 'knativecertificates' ? '.status-unhealthy' : '.text-red-400'
  const resource = { apiVersion: `${kind.group}/v1`, kind: kind.kind, type: 'kubernetes.io/tls', metadata: { name: 'tls', namespace: 'app' }, status: { notAfter: expiry, conditions: [{ type: 'Ready', status: 'True' }] } }
  const host = document.createElement('div'); document.body.append(host)
  const root = createRoot(host)
  try {
    await act(async () => root.render(<VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 48 }}><KeyboardShortcutProvider><ResourcesView namespaces={['app']} apiResources={[kind]} resourceCounts={{ [kind.kind]: 1 }} selectedKindQuery={{ isLoading: false, data: [resource], resourceName: name, group: kind.group }} locationPathname={`/resources/${name}`} locationSearch={kind.group ? `?apiGroup=${kind.group}` : ''} certExpiry={{ 'app/tls': { expiry, daysLeft: 0, expiryPolicy: policy } }} certificateExpiryPolicy={policy} onNavigate={() => {}} /></KeyboardShortcutProvider></VirtuosoMockContext.Provider>))
    const table = host.querySelector('table')
    expect(table).not.toBeNull()
    expect(table?.querySelector(warningSelector)).toBeNull()
    await act(async () => { vi.advanceTimersByTime(90_000) })
    expect(table?.querySelector(warningSelector)).not.toBeNull()
    await act(async () => { vi.advanceTimersByTime(120_000) })
    expect(table?.querySelector(highSelector)).not.toBeNull()
    await act(async () => { vi.advanceTimersByTime(30_000) })
    expect(table?.textContent).toContain('Expired')
  } finally { await act(async () => root.unmount()); host.remove() }
})
