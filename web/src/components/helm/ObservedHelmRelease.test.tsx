// @vitest-environment jsdom
import { act, type ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HelmInventory, ObservedHelmRelease } from '../../types'
import { ApiError, isHelmReleaseReadDenied } from '../../api/client'
import { compareRows, HelmView } from './HelmView'
import { ObservedHelmNotice } from './ObservedHelmNotice'

const inventoryState = vi.hoisted(() => ({
  current: { data: undefined, isPending: false, error: null } as { data?: unknown; isPending: boolean; error: unknown },
  refetchUpgrades: vi.fn(),
}))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  useHelmInventory: () => ({ ...inventoryState.current, dataUpdatedAt: 0, isFetching: false, refetch: vi.fn() }),
  useHelmBatchUpgradeInfo: () => ({ data: undefined, isLoading: false, error: null, refetch: inventoryState.refetchUpgrades }),
}))
vi.mock('../../context/ConnectionContext', () => ({ useConnection: () => ({ connection: { state: 'connected' } }) }))
vi.mock('../../hooks/useKeyboardShortcuts', () => ({ useRegisterShortcuts: () => undefined }))
vi.mock('./ChartBrowser', () => ({ ChartBrowser: () => null }))
vi.mock('@skyhook-io/k8s-ui', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@skyhook-io/k8s-ui')>()),
  FreshnessControl: ({ onRefresh }: { onRefresh: () => void }) => <button onClick={onRefresh}>Refresh</button>,
}))
vi.mock('./InstallWizard', () => ({ InstallWizard: () => null }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | undefined

function mount(element: ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root!.render(element))
  return container
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

const observed: ObservedHelmRelease = { name: 'api', namespace: 'team-b', chart: 'api', chartVersion: '1.2.3', appVersion: '9.9', resourceCount: 3, resourceHealth: 'healthy', healthSummary: '1/1 ready' }

function denied(rows: ObservedHelmRelease[]): HelmInventory {
  return { releases: [], observed: rows, storageDenied: { count: 2, names: ['team-a', 'team-b'] }, objectCoverage: { status: 'complete', gaps: [] } }
}

function renderView(state: Partial<typeof inventoryState.current>) {
  inventoryState.current = { data: undefined, isPending: false, error: null, ...state }
  return mount(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <HelmView namespaces={[]} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function row(container: HTMLElement, name: string): string {
  const match = [...container.querySelectorAll('tbody tr')].find((tr) => tr.textContent?.startsWith(name))
  expect(match).toBeDefined()
  return match!.textContent ?? ''
}

describe('Helm list without Secret access', () => {
  it('never shows a status, revision or update time for an observed row', () => {
    const text = row(renderView({ data: denied([observed]) }), 'api')
    expect(text).toContain('Unknown')
    expect(text).toContain('api-1.2.3')
    expect(text).not.toMatch(/deployed/i)
  })

  it('names several charts instead of picking one, and an unlabelled chart as unknown', () => {
    const container = renderView({ data: denied([
      { name: 'umbrella', namespace: 'team-b', otherCharts: ['parent-2.0.0', 'sub-0.5.0'], resourceCount: 4 },
      { name: 'plain', namespace: 'team-b', resourceCount: 1 },
    ]) })
    expect(row(container, 'umbrella')).toContain('Several charts')
    expect(row(container, 'plain').match(/Unknown/g)).toHaveLength(2)
  })

  it('says versions are mixed rather than picking one', () => {
    const text = row(renderView({ data: denied([
      { name: 'api', namespace: 'team-b', chart: 'api', chartVersions: ['1.0.0', '2.0.0'], appVersions: ['1', '2'], resourceCount: 4 },
    ]) }), 'api')
    expect(text).toContain('api, mixed versions')
    expect(text).toContain('Mixed')
    expect(text).toContain('versions differ')
    expect(text).not.toContain('api-2.0.0')
    expect(row(renderView({ data: denied([observed]) }), 'api')).not.toContain('versions differ')
  })

  it('finds a release with several charts by any of their names', () => {
    const container = renderView({ data: denied([
      { name: 'umbrella', namespace: 'team-b', otherCharts: ['parent-2.0.0', 'sub-0.5.0'], resourceCount: 4 },
      observed,
    ]) })
    const search = container.querySelector<HTMLInputElement>('input[placeholder="Search releases..."]')!
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    act(() => {
      setValue.call(search, 'parent')
      search.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const names = [...container.querySelectorAll('tbody tr')].map((tr) => tr.textContent ?? '')
    expect(names.some((text) => text.startsWith('umbrella'))).toBe(true)
    expect(names.some((text) => text.startsWith('api'))).toBe(false)
  })

  it('does not let an empty list read as no releases', () => {
    const text = renderView({ data: denied([]) }).textContent
    expect(text).toContain('No releases found on the objects you can read')
    expect(text).toContain('doesn’t mean there are no releases')
    expect(text).not.toContain('No Helm releases found')
    expect(text).not.toContain('Browse charts to install')
  })

  it('shows no callout when everything came from Helm', () => {
    const text = renderView({ data: { releases: [], observed: [] } }).textContent
    expect(text).toContain('No Helm releases found')
    expect(text).not.toContain('Built from labels and annotations')
  })

  it('does not refresh upgrade checks when no row came from Helm', () => {
    inventoryState.refetchUpgrades.mockClear()
    const container = renderView({ data: denied([observed]) })
    const refresh = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Refresh')!
    act(() => refresh.click())
    expect(inventoryState.refetchUpgrades).not.toHaveBeenCalled()
  })

  it('still refreshes upgrade checks for rows from Helm', () => {
    inventoryState.refetchUpgrades.mockClear()
    const release = { name: 'x', namespace: 'ns', chart: 'c', chartVersion: '1', appVersion: '1', status: 'deployed', revision: 1, updated: '2026-01-01T00:00:00Z' }
    const container = renderView({ data: { releases: [release], observed: [] } })
    const refresh = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Refresh')!
    act(() => refresh.click())
    expect(inventoryState.refetchUpgrades).toHaveBeenCalledOnce()
  })

  it('waits rather than saying there are no releases while the read has not started', () => {
    expect(renderView({ isPending: true }).textContent).not.toContain('No Helm releases found')
  })

  it('keeps the restricted state for a Radar without the inventory', () => {
    expect(renderView({ error: new ApiError('forbidden', 403) }).textContent).toContain('Access Restricted')
  })
})

describe('columns only Helm can fill', () => {
  const headers = (container: HTMLElement) => [...container.querySelectorAll('th')].map((th) => th.textContent)

  it('drops revision and update time when no row has them', () => {
    const labels = headers(renderView({ data: denied([observed]) }))
    expect(labels.join()).toContain('Status')
    expect(labels.join()).not.toMatch(/Rev|Updated/)
  })

  it('keeps them when any row came from Helm', () => {
    const data = { ...denied([observed]), releases: [{ name: 'web', namespace: 'team-a', chart: 'web', chartVersion: '1', appVersion: '1', status: 'deployed', revision: 2, updated: '2026-01-01T00:00:00Z' }] }
    expect(headers(renderView({ data })).join()).toMatch(/Rev.*Updated/)
  })
})

describe('health on an observed row', () => {
  it('states healthy objects as a count, with no green mark beside an unknown status', () => {
    const container = renderView({ data: denied([observed]) })
    expect(row(container, 'api')).toContain('1/1 ready')
    expect(container.querySelector('tbody .badge-sm .rounded-full')).toBeNull()
  })

  it('keeps the mark on a real problem', () => {
    const container = renderView({ data: denied([{ ...observed, resourceHealth: 'unhealthy', healthSummary: '0/1 ready' }]) })
    expect(row(container, 'api')).toContain('0/1 ready')
    expect(container.querySelector('tbody .badge-sm .rounded-full')).not.toBeNull()
  })
})

describe('ObservedHelmNotice', () => {
  it('names what it reads and that it can miss things, before any expanding', () => {
    const text = mount(<ObservedHelmNotice inventory={denied([observed])} />).textContent
    expect(text).toContain('Built from labels and annotations, not Helm’s own records')
    for (const name of ['meta.helm.sh/release-name', 'meta.helm.sh/release-namespace', 'helm.sh/chart', 'app.kubernetes.io/version']) {
      expect(text).toContain(name)
    }
    expect(text).toContain('source of truth Helm itself uses, so this view can miss things, and a failed release can look fine')
    expect(text).toContain('in team-a, team-b')
  })

  it('keeps the detail behind the disclosure', () => {
    const container = mount(<ObservedHelmNotice inventory={denied([observed])} />)
    const toggle = container.querySelector('button')!
    expect(toggle.textContent).toContain('What’s affected')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(container.textContent).toContain('How to get the full view: get and list access to Secrets')
    expect(container.textContent).toContain('That opens every Secret there')
    act(() => toggle.click())
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(container.textContent).toContain('Releases that failed before creating any object.')
    for (const heading of ['Not shown', 'Not listed', 'Can differ from Helm']) {
      expect(container.querySelector(`section[aria-label="${heading}"]`)).not.toBeNull()
    }
  })

  it('counts namespaces it cannot name', () => {
    const text = mount(<ObservedHelmNotice inventory={{ ...denied([]), storageDenied: { count: 80 } }} />).textContent
    expect(text).toContain('in 80 namespaces')
  })

  it('speaks of the whole view when everything was denied', () => {
    const text = mount(<ObservedHelmNotice inventory={{ ...denied([]), storageDenied: { extent: 'all' } }} />).textContent
    expect(text).toContain('Radar can’t read Helm’s release Secrets here')
  })

  it('says where the rows come from when only some are observed', () => {
    const inventory = { ...denied([observed]), releases: [{ name: 'x' } as HelmInventory['releases'][number]], storageDenied: { count: 1, names: ['team-b'] } }
    expect(mount(<ObservedHelmNotice inventory={inventory} />).textContent).toContain('releases there come from')
  })

  it('adds what the object read itself did not reach', () => {
    const inventory: HelmInventory = { ...denied([]), objectCoverage: { status: 'partial', gaps: [{ code: 'kind_sync_pending', action: 'retry' }] } }
    expect(mount(<ObservedHelmNotice inventory={inventory} />).textContent).toContain('Some objects weren’t read (still loading)')
  })
})

describe('compareRows', () => {
  const full = (name: string, status: string, revision: number) => ({
    observed: false as const,
    release: { name, namespace: 'ns', chart: 'c', chartVersion: '1', appVersion: '1', status, revision, updated: '2026-01-01T00:00:00Z' } as HelmInventory['releases'][number],
  })
  const seen = (name: string) => ({ observed: true as const, release: { name, namespace: 'ns', resourceCount: 1 } })

  it('sorts an unknown value last in both directions', () => {
    for (const direction of [1, -1] as const) {
      const sorted = [seen('b'), full('a', 'deployed', 2), seen('a'), full('b', 'failed', 1)]
        .sort((x, y) => compareRows(x, y, 'revision', direction))
        .map((r) => r.observed)
      expect(sorted).toEqual([false, false, true, true])
    }
  })

  it('sorts what an observed row does know alongside the rest', () => {
    const sorted = [seen('b'), full('a', 'deployed', 2)].sort((x, y) => compareRows(x, y, 'name', 1))
    expect(sorted[0].release.name).toBe('a')
  })
})

describe('isHelmReleaseReadDenied', () => {
  it('matches only the caller being refused Helm storage', () => {
    expect(isHelmReleaseReadDenied(new ApiError('no', 403, { error_code: 'release_read_denied' }))).toBe(true)
    expect(isHelmReleaseReadDenied(new ApiError('no', 403))).toBe(false)
    expect(isHelmReleaseReadDenied(new ApiError('no', 403, { error_code: 'other' }))).toBe(false)
    expect(isHelmReleaseReadDenied(new ApiError('no', 500, { error_code: 'release_read_denied' }))).toBe(false)
  })
})
