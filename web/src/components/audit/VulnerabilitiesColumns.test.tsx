// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../api/client'
import { useVulnerabilities, vulnerabilitiesPath, type VulnerabilitiesResponse } from '../../api/vulnerabilities'
import { daysSince, isoDate } from '../vulnerabilities/columns'
import { VulnerabilitiesView } from './VulnerabilitiesView'

// Whether this Radar has the CSV export, which puts the Download items in the menu.
const feature = vi.hoisted(() => ({ exportSupport: 'unsupported' }))
vi.mock('../../api/client', async (importOriginal) => ({
  ...await importOriginal<typeof import('../../api/client')>(),
  useCapabilities: () => ({ data: { features: {} } }),
  useRadarFeature: () => ({ support: feature.exportSupport, guard: <T,>(fn: () => T) => fn() }),
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

// eolCount null is an older Radar, which sends no end-of-life count.
function response(view: VulnerabilitiesResponse['view'], eolCount: number | null): VulnerabilitiesResponse {
  return {
    source: 'trivy-operator',
    view,
    summary: { imagesScanned: 1, imagesClean: 0, imagesUnscanned: 0, imagesWithCritical: 1, fixableCriticalHighCves: 1, workloadsWithCriticalOrHigh: 1, workloads: 1, counts: { ...zero, critical: 1 }, ...(eolCount === null ? {} : { imagesOsEndOfLife: eolCount }) },
    coverage: { complete: true, gaps: [], inactiveReports: 0 },
    images: [{
      id: 'img', image: 'nginx:1.19.0', os: 'debian 10.4', osEndOfLife: true, scanner: { name: 'Trivy', version: '0.76.1' }, scannedAt: '2026-11-20T12:00:00Z',
      counts: { ...zero, critical: 1 }, fixable: { ...zero, critical: 1 }, matching: 1, workloads: [], workloadsTotal: 0, findingsTotal: 1,
      findings: [{ id: 'CVE-1', severity: 'critical', package: 'openssl', installedVersion: '1.1', packageUrl: 'pkg:deb/debian/openssl@1.1' }],
      report: { kind: 'VulnerabilityReport', namespace: 'shop', name: 'r' },
    }],
    cves: [{ id: 'CVE-1', severity: 'critical', packages: ['openssl'], fixable: true, fixableImages: 1, images: 1, workloads: 1, publishedAt: '2020-12-10T04:15:11Z', affected: [] }],
  }
}

let container: HTMLDivElement
let root: Root
function Probe() {
  return <output data-testid="location">{useLocation().search}</output>
}
const urlParams = () => new URLSearchParams(container.querySelector('[data-testid="location"]')!.textContent ?? '')

function render(path: string, eolCount: number | null = 3, shape: (r: VulnerabilitiesResponse) => VulnerabilitiesResponse | undefined = (r) => r, error: unknown = null) {
  vi.mocked(useVulnerabilities).mockImplementation((p) => ({
    data: shape(response(p.view, eolCount)), isLoading: false, isFetching: false, isPlaceholderData: false, error, dataUpdatedAt: 0, refetch: vi.fn(),
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
const menuButton = () => container.querySelector<HTMLButtonElement>('button[aria-label="More actions"]')
const menuItems = () => Array.from(container.querySelectorAll<HTMLButtonElement>('[role="menu"] [role^="menuitem"]'))
const menuItem = (label: string) => menuItems().find((b) => b.textContent === label)
const heads = () => Array.from(container.querySelectorAll('.uppercase')).map((h) => h.textContent ?? '')
const lastParams = () => vi.mocked(useVulnerabilities).mock.calls.at(-1)![0]

beforeEach(() => {
  feature.exportSupport = 'unsupported'
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

describe('Vulnerabilities optional columns', () => {
  it('keeps them off until turned on from the page menu, and remembers the choice in this browser', () => {
    render('/checks/vulnerabilities?by=cve')
    expect(heads().some((h) => h.includes('Published'))).toBe(false)
    expect(container.textContent).not.toContain('2020-12-10')

    act(() => menuButton()!.click())
    const show = menuItem('Show Published')!
    expect(show.getAttribute('role')).toBe('menuitemcheckbox')
    expect(show.getAttribute('aria-checked')).toBe('false')
    act(() => show.click())
    // A setting, not an action: the menu stays open and shows it checked.
    expect(menuItem('Show Published')!.getAttribute('aria-checked')).toBe('true')
    expect(heads().some((h) => h.includes('Published'))).toBe(true)
    expect(container.textContent).toContain('2020-12-10')
    expect(JSON.parse(localStorage.getItem('radar-vulnerability-columns')!)).toEqual(['cve.published'])

    act(() => root.unmount())
    root = createRoot(container)
    render('/checks/vulnerabilities?by=cve')
    expect(container.textContent).toContain('2020-12-10')
  })

  it('groups the menu: downloads, the view\'s columns, then optional filters, each group after a divider', () => {
    feature.exportSupport = 'supported'
    render('/checks/vulnerabilities?by=cve')
    act(() => menuButton()!.click())
    expect(menuItems().map((b) => b.textContent)).toEqual([
      'Download By CVE as CSV', 'Download all findings as CSV', 'Show Published', 'Show Age', 'Show OS end of life filter',
    ])
    expect(container.querySelectorAll('[role="menu"] .h-px')).toHaveLength(2)

    act(() => { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })) })
    act(() => button('By fix')!.click())
    act(() => menuButton()!.click())
    expect(menuItems().map((b) => b.textContent)).toEqual(['Download By fix as CSV', 'Download all findings as CSV', 'Show OS end of life filter'])
    expect(container.querySelectorAll('[role="menu"] .h-px')).toHaveLength(1)
  })

  it('leaves out a group with nothing in it, and the menu when every group is empty', () => {
    render('/checks/vulnerabilities?by=cve')
    act(() => menuButton()!.click())
    expect(menuItems().map((b) => b.textContent)).toEqual(['Show Published', 'Show Age', 'Show OS end of life filter'])
    expect(container.querySelectorAll('[role="menu"] .h-px')).toHaveLength(1)

    // An older Radar: no end-of-life count, so no filter to offer, and By fix
    // has no columns either.
    act(() => root.unmount())
    root = createRoot(container)
    render('/checks/vulnerabilities?by=fix', null)
    expect(menuButton()).toBeNull()
  })

  it('shows OS and Scanner on By image, the badge moving into the OS column', () => {
    render('/checks/vulnerabilities?by=image')
    act(() => menuButton()!.click())
    expect(menuItems().map((b) => b.textContent)).toEqual(['Show OS', 'Show Scanner', 'Show Package URL', 'Show OS end of life filter'])
    act(() => menuItem('Show OS')!.click())
    act(() => menuItem('Show Scanner')!.click())
    expect(heads().some((h) => h.includes('OS') && h.includes('Scanner'))).toBe(true)
    expect(container.textContent).toContain('Trivy 0.76.1')
    const badges = Array.from(container.querySelectorAll('.badge-sm')).filter((b) => b.textContent === 'OS end of life')
    expect(badges).toHaveLength(1)
    expect(badges[0].closest('span.flex-col')?.textContent).toBe('debian 10.4OS end of life')
  })

  it('shows each finding\'s package URL in an expanded image', () => {
    localStorage.setItem('radar-vulnerability-columns', JSON.stringify(['image.packageUrl']))
    render('/checks/vulnerabilities?by=image')
    act(() => container.querySelector<HTMLButtonElement>('button[aria-expanded]:not([aria-label="More actions"])')!.click())
    expect(container.textContent).toContain('pkg:deb/debian/openssl@1.1')
  })

  it('shows Age in days next to Published', () => {
    localStorage.setItem('radar-vulnerability-columns', JSON.stringify(['cve.published', 'cve.age']))
    render('/checks/vulnerabilities?by=cve')
    const days = daysSince('2020-12-10T04:15:11Z')!
    expect(container.textContent).toContain(`2020-12-10${days.toLocaleString('en-US')} d`)
  })

  it('explains Published on its column head', () => {
    localStorage.setItem('radar-vulnerability-columns', JSON.stringify(['cve.published']))
    render('/checks/vulnerabilities?by=cve')
    const head = Array.from(container.querySelectorAll('span.underline')).find((h) => h.textContent === 'Published')
    expect(head).toBeTruthy()
  })

  it('shows the OS with its end-of-life badge under the image name', () => {
    render('/checks/vulnerabilities?by=image')
    const badges = Array.from(container.querySelectorAll('.badge-sm')).filter((s) => s.textContent === 'OS end of life')
    expect(badges).toHaveLength(1)
    expect(badges[0].closest('span.flex')?.textContent).toContain('debian 10.4')
  })

  it('counts the days since publication, not since detection', () => {
    expect(daysSince('2020-01-10T00:00:00Z', Date.parse('2026-01-10T12:00:00Z'))).toBe(2192)
    expect(daysSince('2030-01-01T00:00:00Z', Date.parse('2026-01-10T12:00:00Z'))).toBe(0)
    expect(daysSince('not a date')).toBeUndefined()
    expect(isoDate('2020-12-10T04:15:11Z')).toBe('2020-12-10')
  })
})

describe('OS end of life filter', () => {
  it('filters through the URL and the request', () => {
    render('/checks/vulnerabilities?by=image')
    const chip = () => Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.startsWith('OS end of life'))
    expect(chip()?.textContent).toContain('3')
    expect(lastParams().osEndOfLife).toBe(false)
    act(() => chip()!.click())
    expect(urlParams().get('eol')).toBe('1')
    expect(lastParams().osEndOfLife).toBe(true)
    expect(vulnerabilitiesPath(lastParams())).toContain('eol=1')

    // It counts as a filter: the match line offers to clear it.
    act(() => button('Clear filters')!.click())
    expect(urlParams().has('eol')).toBe(false)
    expect(urlParams().get('by')).toBe('image')
    expect(lastParams().osEndOfLife).toBe(false)
  })

  it('can be hidden from the menu, per browser, and still shows for a link that sets it', () => {
    const chip = () => Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.startsWith('OS end of life'))
    render('/checks/vulnerabilities?by=image')
    act(() => menuButton()!.click())
    expect(menuItem('Show OS end of life filter')!.getAttribute('aria-checked')).toBe('true')
    act(() => menuItem('Show OS end of life filter')!.click())
    expect(chip()).toBeUndefined()
    expect(JSON.parse(localStorage.getItem('radar-vulnerability-filters')!)).toEqual({ eol: false })

    act(() => root.unmount())
    root = createRoot(container)
    render('/checks/vulnerabilities?by=image&eol=1')
    expect(chip()?.getAttribute('aria-pressed')).toBe('true')
  })

  it('leaves the chip out for a Radar that sends no end-of-life count, or when there is none', () => {
    for (const count of [null, 0]) {
      render('/checks/vulnerabilities?by=image', count)
      expect(Array.from(container.querySelectorAll('button')).some((b) => b.textContent?.startsWith('OS end of life'))).toBe(false)
    }
  })
})

describe('No data', () => {
  it('explains the page and offers one install step when the cluster has no Trivy Operator', () => {
    render('/checks/vulnerabilities', 3, () => undefined, new ApiError('kind unknown', 404, { error_code: 'kind_unknown' }))
    const panel = container.querySelector('section[aria-label="Trivy Operator not installed"]')!
    expect(panel.textContent).toContain("Trivy Operator isn't installed in this cluster")
    expect(panel.querySelectorAll('li')).toHaveLength(4)
    expect(Array.from(panel.querySelectorAll('a')).map((a) => a.textContent?.trim())).toEqual(['Install Trivy Operator'])
    expect(container.textContent).toContain("Known CVEs in the images that run now, from Trivy Operator's reports.")
    expect(menuButton()).toBeNull()
  })

  it('offers a Clear filters action when nothing matches', () => {
    render('/checks/vulnerabilities?by=cve&q=nothing', 3, (r) => ({ ...r, cves: [] }))
    expect(container.textContent).toContain('Nothing matches these filters')
    const clear = Array.from(container.querySelectorAll('button')).filter((b) => b.textContent === 'Clear filters')
    expect(clear.length).toBeGreaterThan(0)
    act(() => clear.at(-1)!.click())
    expect(urlParams().has('q')).toBe(false)
    expect(urlParams().get('by')).toBe('cve')
  })

  it('offers no column the answer has no data for, and draws none a browser had on', () => {
    localStorage.setItem('radar-vulnerability-columns', JSON.stringify(['image.scanner', 'image.os']))
    // An older Radar: no scanner, no purls, no end-of-life count.
    render('/checks/vulnerabilities?by=image', null, (r) => ({ ...r, images: r.images!.map(({ scanner: _s, ...i }) => ({ ...i, findings: i.findings.map(({ packageUrl: _p, ...f }) => f) })) }))
    expect(heads().some((h) => h.includes('Scanner'))).toBe(false)
    expect(heads().some((h) => h.includes('OS'))).toBe(true)
    act(() => menuButton()!.click())
    expect(menuItems().map((b) => b.textContent)).toEqual(['Show OS'])

    act(() => root.unmount())
    root = createRoot(container)
    render('/checks/vulnerabilities?by=cve', null, (r) => ({ ...r, cves: r.cves!.map(({ publishedAt: _d, ...c }) => c) }))
    expect(menuButton()).toBeNull()
  })
})

describe('Owner column', () => {
  const workload = (name: string) => ({ kind: 'Deployment', group: 'apps', namespace: 'shop', name, counts: { ...zero, high: 1 }, fixable: zero, containers: [] })
  function renderWorkloads(ownership: boolean, owners = true) {
    vi.mocked(useVulnerabilities).mockImplementation((p) => ({
      data: {
        ...response(p.view, 0),
        workloads: [workload('web'), workload('plain')],
        ...(ownership ? {
          ownership: { teamLabel: 'team' },
          owners: !owners ? [] : [{ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web', manager: { tool: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'web-release', chart: 'web-1.2.0' }, team: 'payments', teamFrom: 'workload' }],
        } : {}),
      },
      isLoading: false, isFetching: false, isPlaceholderData: false, error: null, dataUpdatedAt: 0, refetch: vi.fn(),
    } as unknown as ReturnType<typeof useVulnerabilities>))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } })
    act(() => root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/checks/vulnerabilities?by=workload']}>
          <VulnerabilitiesView namespaces={[]} onNavigateToResource={vi.fn()} />
        </MemoryRouter>
      </QueryClientProvider>,
    ))
  }

  it('is off by default and shows only what Radar knows when on', () => {
    renderWorkloads(true)
    expect(heads().some((h) => h.includes('Owner'))).toBe(false)
    expect(container.textContent).not.toContain('web-release')
    act(() => menuButton()!.click())
    act(() => menuItem('Show Owner')!.click())
    expect(heads().some((h) => h.includes('Owner'))).toBe(true)
    expect(container.textContent).toContain('web-release')
    expect(container.textContent).toContain('Helm · web-1.2.0')
    expect(container.textContent).toContain('team: payments')
    // The unmanaged workload's owner cell is empty, not a guess.
    expect(container.textContent).not.toContain('Unknown')
  })

  it('says in one line that none was found, instead of a column of blanks', () => {
    renderWorkloads(true, false)
    act(() => menuButton()!.click())
    act(() => menuItem('Show Owner')!.click())
    expect(heads().some((h) => h.includes('Owner'))).toBe(false)
    expect(container.textContent).toContain('No owner found for these workloads')
    act(() => menuItem('Show Owner')!.click())
    expect(container.textContent).not.toContain('No owner found')
  })

  it('is not offered by a Radar that reads no owners', () => {
    renderWorkloads(true)
    act(() => menuButton()!.click())
    expect(menuItem('Show Owner')).toBeTruthy()
    act(() => root.unmount())
    root = createRoot(container)
    renderWorkloads(false)
    act(() => menuButton()?.click())
    expect(menuItem('Show Owner')).toBeUndefined()
  })
})
