// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useVulnerabilities, vulnerabilitiesPath, type VulnerabilitiesResponse, type VulnerabilityWorkloadRef } from '../../api/vulnerabilities'
import { VulnerabilitiesView } from './VulnerabilitiesView'
import { riskLookup } from '../vulnerabilities/parts'

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
const ref = (name: string): VulnerabilityWorkloadRef => ({ kind: 'Deployment', group: 'apps', namespace: 'shop', name, container: 'app' })
const PRIVILEGED = 'Container "app" runs in privileged mode'

// web runs privileged and may run as root; worker may run as root and binds
// a host port. Settings come only for factors the page marks.
function response(view: VulnerabilitiesResponse['view'], withRisk: boolean, counts = { privileged: 1, hostAccess: 0, runsAsRoot: 0, mayRunAsRoot: 2, dangerousCapabilities: 0, privilegeEscalation: 0, hostPort: 1 }): VulnerabilitiesResponse {
  const risk = withRisk
    ? {
      podRisk: {
        counts,
        workloads: [
          { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web', factors: ['privileged' as const, 'mayRunAsRoot' as const], settings: [{ factor: 'privileged' as const, check: 'privileged', message: PRIVILEGED }] },
          { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'worker', factors: ['mayRunAsRoot' as const, 'hostPort' as const], settings: [] },
        ],
      },
    }
    : {}
  const riskFactors = withRisk ? { riskFactors: ['privileged' as const, 'mayRunAsRoot' as const, 'hostPort' as const] } : {}
  return {
    source: 'trivy',
    view,
    summary: { imagesScanned: 1, imagesClean: 0, imagesUnscanned: 0, imagesWithCritical: 1, fixableCriticalHighCves: 1, workloadsWithCriticalOrHigh: 2, workloads: 2, counts: { ...zero, critical: 1 } },
    coverage: { complete: true, gaps: [], inactiveReports: 0 },
    ...risk,
    images: [{
      id: 'img', image: 'example.com/shop/app:1', scannedAt: '2026-11-20T12:00:00Z', counts: { ...zero, critical: 1 }, fixable: { ...zero, critical: 1 },
      matching: 1, workloads: [ref('web'), ref('worker')], workloadsTotal: 2, findings: [], findingsTotal: 1,
      report: { kind: 'VulnerabilityReport', namespace: 'shop', name: 'r' }, ...riskFactors,
    }],
    workloads: [
      { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web', counts: { ...zero, critical: 1 }, fixable: zero, containers: [], ...(withRisk ? { riskFactors: ['privileged' as const, 'mayRunAsRoot' as const] } : {}) },
      { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'worker', counts: { ...zero, critical: 1 }, fixable: zero, containers: [], ...(withRisk ? { riskFactors: ['mayRunAsRoot' as const, 'hostPort' as const] } : {}) },
    ],
  }
}

let container: HTMLDivElement
let root: Root
function Probe() {
  return <output data-testid="location">{useLocation().search}</output>
}
const urlParams = () => new URLSearchParams(container.querySelector('[data-testid="location"]')!.textContent ?? '')

function render(path: string, withRisk = true, counts?: Parameters<typeof response>[2]) {
  vi.mocked(useVulnerabilities).mockImplementation((p) => ({
    data: response(p.view, withRisk, counts), isLoading: false, isFetching: false, isPlaceholderData: false, error: null, dataUpdatedAt: 0, refetch: vi.fn(),
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

const riskChip = () => Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-haspopup="menu"]')).find((b) => /^Risk/.test(b.textContent?.trim() ?? ''))
const pick = (label: string) => Array.from(document.body.querySelectorAll('label')).find((l) => l.textContent?.startsWith(label))!.querySelector<HTMLInputElement>('input[type="checkbox"]')!
const rows = () => Array.from(container.querySelectorAll<HTMLButtonElement>('button[aria-expanded]:not([aria-haspopup])'))
const plus = (el: Element) => Array.from(el.querySelectorAll('[aria-label^="Also:"]')).map((m) => m.textContent)
const lastParams = () => vi.mocked(useVulnerabilities).mock.calls.at(-1)![0]
const marksIn = (el: Element) => Array.from(el.querySelectorAll('[aria-label]')).map((m) => m.textContent ?? '').filter((t) => ['Privileged', 'Host access', 'Runs as root', 'May run as root', 'Dangerous capabilities', 'Host port', 'Allows privilege escalation'].includes(t))

beforeEach(() => {
  localStorage.clear()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.mocked(useVulnerabilities).mockReset()
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

describe('risky Pod settings', () => {
  it('filters through one checklist chip with a count per factor, kept in the URL', () => {
    render('/checks/vulnerabilities?by=image&severity=critical')
    expect(riskChip()!.textContent).toBe('Risk factors')
    act(() => riskChip()!.click())
    const labels = Array.from(document.body.querySelectorAll('label')).map((l) => l.textContent)
    expect(labels).toEqual(['Privileged(1)', 'Host access(0)', 'Runs as root(0)', 'May run as root(2)', 'Dangerous capabilities(0)', 'Allows privilege escalation(0)', 'Host port(1)'])
    act(() => pick('Host port').click())
    expect(urlParams().get('risk')).toBe('hostPort')
    expect(urlParams().get('severity')).toBe('critical')
    expect(lastParams()).toMatchObject({ risk: ['hostPort'], severities: ['critical'] })
    expect(riskChip()!.textContent).toBe('Risk: Host port')
    act(() => pick('Privileged').click())
    expect(urlParams().get('risk')).toBe('privileged,hostPort')
    expect(riskChip()!.textContent).toBe('Risk: Privileged +1')
    expect(container.textContent).toMatch(/1 image match/)
    act(() => Array.from(container.querySelectorAll('button')).find((b) => b.textContent === 'Clear filters')!.click())
    expect(urlParams().has('risk')).toBe(false)
    expect(urlParams().get('by')).toBe('image')
    expect(vulnerabilitiesPath({ view: 'cve', risk: ['privileged', 'hostPort'] })).toBe('/vulnerabilities?view=cve&risk=privileged%2ChostPort')
  })

  it('marks the serious factors, and puts the common ones behind +N', () => {
    render('/checks/vulnerabilities?by=image')
    const row = rows()[0]
    expect(marksIn(row)).toEqual(['Privileged'])
    expect(plus(row)).toEqual(['+2'])
    act(() => row.click())
    const items = Array.from(container.querySelectorAll('li'))
    const web = items.find((li) => li.textContent?.startsWith('web'))!
    const worker = items.find((li) => li.textContent?.startsWith('worker'))!
    expect(marksIn(web)).toEqual(['Privileged'])
    expect(plus(web)).toEqual(['+1'])
    // Only common factors: no badge, and no lone +N.
    expect(marksIn(worker)).toEqual([])
    expect(plus(worker)).toEqual([])
    // The tooltip says which container, in the check's words.
    expect(web.querySelector('[aria-label^="Container \\"app\\" runs in privileged mode"]')).not.toBeNull()
  })

  it('marks a common factor once the filter selects it', () => {
    render('/checks/vulnerabilities?by=workload&risk=hostPort')
    expect(rows().map(marksIn)).toEqual([['Privileged'], ['Host port']])
    expect(plus(rows()[1])).toEqual(['+1'])
  })

  const none = { privileged: 0, hostAccess: 0, runsAsRoot: 0, mayRunAsRoot: 0, dangerousCapabilities: 0, privilegeEscalation: 0, hostPort: 0 }

  it('hides the chip when no workload in scope has a risk factor', () => {
    render('/checks/vulnerabilities?by=image', true, none)
    expect(riskChip()).toBeUndefined()
  })

  it('still shows the chip a link sets, whatever the counts', () => {
    render('/checks/vulnerabilities?by=image&risk=privileged', true, none)
    expect(riskChip()!.textContent).toBe('Risk: Privileged')
  })

  it('keeps same-named workloads of different API groups apart', () => {
    const lookup = riskLookup({
      counts: { privileged: 1, hostAccess: 0, runsAsRoot: 0, mayRunAsRoot: 1, dangerousCapabilities: 0, privilegeEscalation: 0, hostPort: 0 },
      workloads: [
        { kind: 'Rollout', group: 'argoproj.io', namespace: 'shop', name: 'web', factors: ['privileged'], settings: [] },
        { kind: 'Rollout', group: 'example.com', namespace: 'shop', name: 'web', factors: ['mayRunAsRoot'], settings: [] },
      ],
    })
    expect(lookup.size).toBe(2)
    expect(lookup.get('argoproj.io/Rollout/shop/web')?.factors).toEqual(['privileged'])
  })

  it('shows no chip or mark when Radar sends no podRisk', () => {
    render('/checks/vulnerabilities?by=image&risk=privileged', false)
    expect(riskChip()).toBeUndefined()
    expect(marksIn(container)).toEqual([])
  })
})
