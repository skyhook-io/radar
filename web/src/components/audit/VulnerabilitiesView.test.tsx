// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useVulnerabilities, type VulnerabilitiesResponse, type VulnerabilityWorkloadRef } from '../../api/vulnerabilities'
import { VulnerabilitiesView } from './VulnerabilitiesView'

vi.mock('../../api/client', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../api/client')>(),
  useCapabilities: () => ({ data: { features: {} } }),
  useRadarFeature: () => ({ support: 'unsupported', guard: <T,>(fn: () => T) => fn() }),
}))
vi.mock('../../api/apiResources', () => ({ useAPIResources: () => ({ data: [{ group: 'aquasecurity.github.io', name: 'vulnerabilityreports' }] }) }))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { context: 'test', clusterName: 'test', state: 'connected' } }) }))
vi.mock('../../hooks/useDesktopDownload', () => ({ useDesktopDownload: () => undefined }))
vi.mock('./ChecksViewTabs', () => ({ ChecksViewTabs: () => null }))
vi.mock('../../api/vulnerabilities', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../api/vulnerabilities')>(),
  useVulnerabilities: vi.fn(),
}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const zero = { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 }
const ref = (name: string, namespace = 'shop'): VulnerabilityWorkloadRef => ({ kind: 'Deployment', group: 'apps', namespace, name, container: 'app' })
const route = { via: 'LoadBalancer', namespace: 'shop', name: 'web' }

function response(view: VulnerabilitiesResponse['view'], withExposure: boolean): VulnerabilitiesResponse {
  const base: VulnerabilitiesResponse = {
    source: 'trivy',
    view,
    summary: { imagesScanned: 1, imagesClean: 0, imagesUnscanned: 0, imagesWithCritical: 1, fixableCriticalHighCves: 1, workloadsWithCriticalOrHigh: 2, workloads: 3, counts: { ...zero, critical: 1 } },
    coverage: { complete: true, gaps: [], inactiveReports: 0 },
  }
  const exposed = withExposure ? { exposed: [{ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web', routes: [route] }], exposedTotal: 1 } : {}
  const image = {
    id: 'img', image: 'example.com/shop/app:1', scannedAt: '2026-11-20T12:00:00Z', counts: { ...zero, critical: 1 }, fixable: { ...zero, critical: 1 },
    matching: 1, workloads: [ref('web'), ref('worker')], workloadsTotal: 2, findings: [], findingsTotal: 1,
    report: { kind: 'VulnerabilityReport', namespace: 'shop', name: 'r' }, ...exposed,
  }
  return {
    ...base,
    ...(withExposure ? { exposure: { gaps: [{ code: 'user_forbidden', kinds: { count: 1, names: [{ kind: 'Ingress', group: 'networking.k8s.io' }] }, namespaces: { count: 1, names: ['hidden'] } }] } } : {}),
    images: [image],
    fixes: [{
      package: 'zlib', from: ['1.0'], to: '1.1', severity: 'critical', counts: { ...zero, critical: 1 }, cves: ['CVE-1'], cvesTotal: 1, images: 1, workloads: 2,
      affected: [{ imageId: 'img', image: image.image, installedVersions: ['1.0'], workloads: image.workloads, workloadsTotal: 2 }], ...exposed,
    }],
    workloads: [
      { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web', counts: { ...zero, critical: 1 }, fixable: zero, containers: [], ...(withExposure ? { routes: [route, { via: 'Ingress', namespace: 'shop', name: 'web' }] } : {}) },
      { kind: 'Deployment', group: 'apps', namespace: 'hidden', name: 'secret', counts: { ...zero, critical: 1 }, fixable: zero, containers: [] },
      { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'worker', counts: { ...zero, critical: 1 }, fixable: zero, containers: [] },
    ],
  }
}

let container: HTMLDivElement
let root: Root
function Probe() {
  return <output data-testid="location">{useLocation().search}</output>
}

const urlParams = () => new URLSearchParams(container.querySelector('[data-testid="location"]')!.textContent ?? '')

function render(path: string, withExposure = true) {
  vi.mocked(useVulnerabilities).mockImplementation((p) => ({
    data: response(p.view, withExposure), isLoading: false, isFetching: false, isPlaceholderData: false, error: null, dataUpdatedAt: 0, refetch: vi.fn(),
  } as unknown as ReturnType<typeof useVulnerabilities>))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } })
  act(() => root.render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <VulnerabilitiesView namespaces={[]} onNavigateToResource={vi.fn()} />
        <Probe />
      </MemoryRouter>
    </QueryClientProvider>,
  ))
}

const button = (label: string) => Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.trim() === label)
const lastParams = () => vi.mocked(useVulnerabilities).mock.calls.at(-1)![0]
const marks = () => container.querySelectorAll('svg[aria-label^="Exposed via"]')

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.mocked(useVulnerabilities).mockReset()
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('VulnerabilitiesView', () => {
  it('opens By fix by default and sets by only for the other views', () => {
    render('/checks/vulnerabilities')
    expect(lastParams().view).toBe('fix')
    expect(button('By fix')?.getAttribute('aria-pressed')).toBe('true')

    act(() => button('By image')!.click())
    expect(urlParams().get('by')).toBe('image')
    expect(lastParams().view).toBe('image')

    act(() => button('By fix')!.click())
    expect(urlParams().has('by')).toBe(false)
    expect(lastParams().view).toBe('fix')
  })

  it('filters to exposed rows through the URL and the request', () => {
    render('/checks/vulnerabilities?by=cve')
    expect(lastParams().exposed).toBe(false)
    act(() => button('Exposed')!.click())
    expect(urlParams().get('exposed')).toBe('1')
    expect(lastParams().exposed).toBe(true)
    act(() => button('Exposed')!.click())
    expect(urlParams().has('exposed')).toBe(false)
  })

  it('marks only the exposed workload when a row runs in both', () => {
    render('/checks/vulnerabilities?by=image')
    // Collapsed "Running in": web leads with the mark, worker has none.
    expect(marks()).toHaveLength(1)
    const running = marks()[0].closest('span.truncate')!
    expect(running.textContent).toBe('web, worker')
    act(() => container.querySelector<HTMLButtonElement>('button[aria-expanded]')!.click())
    // Expanded: the mark sits beside web's link only.
    const marked = Array.from(marks()).map((m) => m.closest('li')?.textContent ?? '').filter(Boolean)
    expect(marked).toHaveLength(1)
    expect(marked[0]).toContain('web')
    expect(marked[0]).not.toContain('worker')
  })

  it('counts the exposed workloads beside the mark on rows that span workloads', () => {
    render('/checks/vulnerabilities')
    expect(container.textContent).toContain('1 exposed')
    expect(container.textContent).not.toMatch(/\d+ of \d+ exposed/)
    render('/checks/vulnerabilities', false)
    expect(container.textContent).not.toContain('exposed')
  })

  it('shows the route, nothing, or Exposure unknown per workload', () => {
    render('/checks/vulnerabilities?by=workload')
    const rows = Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-expanded]'))
    const cell = (i: number) => rows[i].querySelector('div.grid')!.children[2].textContent
    expect(cell(0)).toBe('LoadBalancer shop/web and 1 more')
    expect(cell(1)).toBe('Exposure unknown')
    expect(cell(2)).toBe('')
  })

  it('leaves out the chip, sort hint, mark and route column when Radar reports no exposure', () => {
    render('/checks/vulnerabilities', false)
    expect(button('Exposed')).toBeUndefined()
    expect(container.textContent).not.toContain('Severity, then exposure')
    expect(marks()).toHaveLength(0)

    render('/checks/vulnerabilities?by=workload', false)
    expect(container.textContent).not.toContain('Exposed via')
    expect(container.textContent).not.toContain('Exposure unknown')
  })

  it('keeps the sort rule in a hint, not a line above the table', () => {
    render('/checks/vulnerabilities')
    expect(container.textContent).toContain('Severity, then exposure')
    expect(container.textContent).not.toContain('Sorted:')
    expect(container.querySelector('[aria-label^="Sort: Most severe first"]')).not.toBeNull()
  })

  it('names the rebuild caveat on the Upgrade header instead of a line', () => {
    render('/checks/vulnerabilities')
    expect(container.textContent).not.toContain('Upgrading a package')
  })

})
