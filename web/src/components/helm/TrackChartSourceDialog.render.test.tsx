// @vitest-environment jsdom
import { act, type InputHTMLAttributes, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ArtifactHubChart, ChartSourceStatus, HelmRepository, SourceUpgradeSelection } from '../../types'
import { TrackChartSourceDialog } from './TrackChartSourceDialog'

const state = vi.hoisted(() => ({
  status: undefined as ChartSourceStatus | undefined,
  repositories: [] as HelmRepository[],
  isOwner: true,
  configManaged: false,
  statusFetching: false,
  featureSupported: true,
  associateSource: vi.fn(),
  addRepository: vi.fn(),
  addOCI: vi.fn(),
  searchCharts: [] as ArtifactHubChart[],
  ociSources: [] as string[],
}))

vi.mock('@skyhook-io/k8s-ui/components/ui/DialogPortal', () => ({
  DialogPortal: ({ open, children }: { open: boolean; children: ReactNode }) => (open ? <div>{children}</div> : null),
}))
vi.mock('@skyhook-io/k8s-ui', () => ({
  Input: (props: InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
  Badge: ({ children }: { children: ReactNode }) => <span>{children}</span>,
}))
vi.mock('../settings/OperatorManagedNotice', () => ({ OperatorManagedNotice: () => null }))
vi.mock('../../api/client', () => {
  const idle = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }
  return {
    useRadarFeature: () => ({ support: state.featureSupported ? 'supported' : 'unsupported' }),
    useHelmSourceStatus: () => ({ data: state.status, error: null, isFetching: state.statusFetching, refetch: vi.fn() }),
    useHelmRepositories: () => ({ data: state.repositories, isLoading: false }),
    useHelmOCISources: () => ({ data: state.ociSources }),
    useClusterInfo: () => ({ data: { inCluster: false } }),
    useCapabilities: () => ({ data: { configManagement: state.configManaged ? 'operator' : 'local' } }),
    useCloudRole: () => ({ canAtLeast: () => state.isOwner }),
    useAddHelmRepository: () => ({ mutate: state.addRepository, isPending: false }),
    useAddOCISource: () => ({ ...idle, mutate: state.addOCI }),
    useArtifactHubSearch: () => ({ data: { charts: state.searchCharts }, isFetching: false, error: null, refetch: vi.fn() }),
    useRemoveOCISource: () => idle,
    useSetHelmSource: () => ({ ...idle, mutate: state.associateSource }),
  }
})

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const recordedURL = 'https://stefanprodan.github.io/podinfo'
const movedMachine: ChartSourceStatus = {
  recorded: { type: 'repository', reference: 'podinfo', url: recordedURL },
  configured: false,
  available: false,
}

let root: Root

beforeEach(() => {
  state.status = movedMachine
  state.repositories = []
  state.configManaged = false
  state.statusFetching = false
  state.isOwner = true
  state.featureSupported = true
  state.associateSource.mockReset()
  state.addRepository.mockReset()
  state.addOCI.mockReset()
  state.searchCharts = []
  state.ociSources = []
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
})

async function renderDialog(onSelectUpgradeSource?: (selection: SourceUpgradeSelection) => void, open = true, onClose = () => undefined) {
  await act(async () => root.render(<TrackChartSourceDialog onSelectUpgradeSource={onSelectUpgradeSource} open={open} onClose={onClose} chartName="podinfo" namespace="web" releaseName="storefront" />))
}

function button(text: string) {
  const result = Array.from(document.querySelectorAll('button')).find(item => item.textContent === text)
  if (!result) throw new Error(`Missing button: ${text}`)
  return result
}

async function click(text: string) { await act(async () => button(text).click()) }

async function fill(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function chooseRecorded() {
  const item = document.querySelector<HTMLButtonElement>(`button[aria-label="Choose configured source: ${recordedURL}"]`)
  expect(item).not.toBeNull()
  await act(async () => item?.click())
}

async function searchPublic() {
  await click('ArtifactHub')
  await click('Search ArtifactHub')
}

async function choosePublic(url: string) {
  const item = document.querySelector<HTMLButtonElement>(`button[aria-label="Choose source: ${url}"]`)
  expect(item).not.toBeNull()
  await act(async () => item?.click())
}

describe('compact source picker', () => {
  it.each(['public HTTP', 'public OCI', 'manual HTTP', 'manual OCI', 'recorded restore', 'source check'])('ignores a stale %s completion after closing and reopening the same release', async scenario => {
    const onClose = vi.fn()
    let complete: () => void = () => { throw new Error('Mutation was not started') }
    const capture = (_input: unknown, callbacks: { onSuccess: (result: { name: string }) => void }) => {
      complete = () => callbacks.onSuccess({ name: 'podinfo' })
    }
    state.addRepository.mockImplementation(capture)
    state.addOCI.mockImplementation(capture)
    state.associateSource.mockImplementation(capture)
    if (scenario === 'source check') state.status = { ...movedMachine, recorded: undefined, revision: 1, candidates: [{ type: 'repository', reference: 'podinfo', url: recordedURL }] }
    await renderDialog(vi.fn(), true, onClose)
    if (scenario.startsWith('public')) {
      const url = scenario === 'public OCI' ? 'oci://ghcr.io/stefanprodan/charts/podinfo' : recordedURL
      state.searchCharts = [{ packageId: 'podinfo', name: 'podinfo', version: '6.15.0', stars: 0, repository: { name: 'podinfo', url } }]
      await searchPublic()
      await choosePublic(url)
      await click(scenario === 'public OCI' ? 'Register OCI source' : 'Add repository')
    } else if (scenario.startsWith('manual')) {
      await click('Add manually')
      if (scenario === 'manual OCI') {
        await click('OCI registry')
        const input = document.querySelector<HTMLInputElement>('input')
        if (!input) throw new Error('Missing OCI prefix input')
        await fill(input, 'oci://ghcr.io/stefanprodan/charts')
      } else {
        const fields = document.querySelectorAll<HTMLInputElement>('input')
        await fill(fields[0], 'podinfo')
        await fill(fields[1], recordedURL)
      }
      await act(async () => document.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    } else {
      await chooseRecorded()
      await click(scenario === 'recorded restore' ? 'Add repository and use it' : 'Check and record source')
    }
    await renderDialog(vi.fn(), false, onClose)
    await renderDialog(vi.fn(), true, onClose)
    if (document.querySelector('form')) await click('Back to sources')
    await click('Add manually')
    await click('Helm repository')
    const field = document.querySelector<HTMLInputElement>('input')
    if (!field) throw new Error('Missing repository name')
    await fill(field, 'new-session-input')
    await act(async () => complete())
    expect(document.querySelector('form')).not.toBeNull()
    expect(document.querySelector<HTMLInputElement>('input')?.value).toBe('new-session-input')
    expect(onClose).not.toHaveBeenCalled()
  })
  it('shows one source list and keeps setup and search behind their own views', async () => {
    state.status = { ...movedMachine, revision: 1 }
    await renderDialog(vi.fn())
    expect(document.querySelectorAll('button[aria-label^="Choose configured source:"]')).toHaveLength(1)
    expect(document.querySelector('input')).toBeNull()
    expect(document.body.textContent).not.toContain('Search ArtifactHub')
    expect(button('Select for upgrade').disabled).toBe(true)
    await click('Add manually')
    expect(document.querySelectorAll('form')).toHaveLength(1)
    expect(document.querySelector('[role="tablist"]')).toBeNull()
    expect(document.querySelector('[placeholder="Repository name"]')).not.toBeNull()
    expect(document.querySelector('[placeholder^="oci://"]')).toBeNull()
    await click('OCI registry')
    expect(document.querySelector('[placeholder="Repository name"]')).toBeNull()
    expect(document.querySelector('[placeholder^="oci://"]')).not.toBeNull()
  })

  it('restores a recorded source only after selection, retaining the existing associate shortcut', async () => {
    await renderDialog()
    expect(state.addRepository).not.toHaveBeenCalled()
    await chooseRecorded()
    await click('Add repository and use it')
    expect(state.addRepository).toHaveBeenCalledWith({ name: 'podinfo', url: recordedURL, namespace: 'web', releaseName: 'storefront' }, expect.anything())
  })

  it('picks a free alias and adds without binding when the recorded source needs confirmation', async () => {
    state.status = { ...movedMachine, confirmed: false }
    state.repositories = [{ name: 'podinfo', url: 'https://other.example.test' }]
    await renderDialog()
    await chooseRecorded()
    expect(document.body.textContent).toContain('podinfo-2')
    await click('Add repository')
    expect(state.addRepository).toHaveBeenCalledWith({ name: 'podinfo-2', url: recordedURL }, expect.anything())
    expect(state.associateSource).not.toHaveBeenCalled()
  })

  it.each(['owner', 'operator'])('prevents configuration writes when restricted by %s policy', async policy => {
    state.isOwner = policy !== 'owner'
    state.configManaged = policy === 'operator'
    await renderDialog()
    await chooseRecorded()
    expect(button('Add repository and use it').disabled).toBe(true)
    await click('Add manually')
    expect(button('Add repository').disabled).toBe(true)
    expect(document.querySelector<HTMLInputElement>('input')?.disabled).toBe(true)
    await click('OCI registry')
    expect(document.querySelector<HTMLInputElement>('input')?.disabled).toBe(true)
    expect(button('Register OCI source').disabled).toBe(true)
  })

  it('links the active source tab to its panel and supports keyboard navigation', async () => {
    await renderDialog()
    const tabs = document.querySelectorAll<HTMLButtonElement>('[role="tab"]')
    const panel = document.querySelector('[role="tabpanel"]')
    expect(tabs[0].getAttribute('aria-controls')).toBe(panel?.id)
    expect(panel?.getAttribute('aria-labelledby')).toBe(tabs[0].id)
    await act(async () => tabs[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true })))
    expect(tabs[1].getAttribute('aria-selected')).toBe('true')
    expect(document.activeElement).toBe(tabs[1])
    expect(document.querySelector('[role="tabpanel"]')?.getAttribute('aria-labelledby')).toBe(tabs[1].id)
    await act(async () => tabs[1].dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true })))
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(document.activeElement).toBe(tabs[0])
  })

  it('keeps explicit source selection and revision expectations without associating the release', async () => {
    const current = { type: 'repository' as const, reference: 'current', url: recordedURL }
    const other = { type: 'repository' as const, reference: 'other', url: 'https://other.example.test' }
    state.status = { recorded: current, configured: true, available: true, confirmed: true, revision: 4, suggestions: [
      { source: current, current: true, hasInstalledVersion: true, state: 'available' },
      { source: other, current: false, hasInstalledVersion: false, state: 'available' },
    ] }
    const select = vi.fn()
    await renderDialog(select)
    await act(async () => document.querySelector<HTMLButtonElement>(`button[aria-label="Choose configured source: ${other.url}"]`)?.click())
    await click('Select for upgrade')
    expect(select).toHaveBeenCalledWith({ source: other, expectedRevision: 4, expectedSource: current })
    expect(state.associateSource).not.toHaveBeenCalled()
  })

  it('preserves installed-version verification as a separate selected-source action', async () => {
    const source = { type: 'repository' as const, reference: 'podinfo', url: recordedURL }
    state.status = { configured: false, available: false, confirmed: false, revision: 1, candidates: [source] }
    await renderDialog(vi.fn())
    await chooseRecorded()
    await click('Check and record source')
    expect(state.associateSource).toHaveBeenCalledWith(source, expect.anything())
  })

  it('allows original-source verification for unchecked OCI and blocks it after a failed source change', async () => {
    const source = { type: 'oci' as const, reference: 'oci://registry.example.test/team/podinfo' }
    state.status = { configured: false, available: false, confirmed: false, revision: 1, suggestions: [{ source, current: false, hasInstalledVersion: false, state: 'unchecked' }] }
    await renderDialog(vi.fn())
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label^="Choose configured source:"]')?.click())
    await click('Check and record source')
    expect(state.associateSource).toHaveBeenCalledWith(source, expect.anything())
    state.status = { ...state.status, sourceChangeRequired: true }
    await renderDialog(vi.fn())
    expect(document.body.textContent).not.toContain('Check and record source')
  })

  it('disables release actions while source status is refreshing', async () => {
    state.status = { ...movedMachine, configured: true, revision: 1 }
    state.statusFetching = true
    await renderDialog(vi.fn())
    await chooseRecorded()
    expect(button('Select for upgrade').disabled).toBe(true)
  })

  it('does not expose public discovery or source switching on an unsupported backend', async () => {
    state.featureSupported = false
    await renderDialog(vi.fn())
    expect(document.body.textContent).not.toContain('ArtifactHub')
    expect(document.body.textContent).not.toContain('Select for upgrade')
  })

  it('resets a selected source and the active view when the release changes', async () => {
    await renderDialog(vi.fn())
    await chooseRecorded()
    await click('Add manually')
    await act(async () => root.render(<TrackChartSourceDialog open onClose={() => undefined} chartName="podinfo" namespace="web" releaseName="another-release" onSelectUpgradeSource={vi.fn()} />))
    expect(document.querySelector('form')).toBeNull()
    expect(document.querySelector('[aria-pressed="true"]')).toBeNull()
  })

  it('filters a long configured list without hiding an off-page selection in the action footer', async () => {
    state.status = { configured: false, available: false, revision: 1, suggestions: Array.from({ length: 60 }, (_, i) => ({ source: { type: 'repository' as const, reference: `repo-${i}`, url: `https://repo-${i}.example.test` }, current: false, hasInstalledVersion: true, state: 'available' })) }
    await renderDialog(vi.fn())
    const filter = document.querySelector<HTMLInputElement>('input[aria-label="Filter configured sources"]')
    expect(filter).not.toBeNull()
    expect(document.querySelector('[aria-label="Configured chart sources"]')?.className).toContain('overflow-y-auto')
    await act(async () => document.querySelector<HTMLButtonElement>('button[aria-label^="Choose configured source:"]')?.click())
    expect(button('Select for upgrade').disabled).toBe(false)
    if (!filter) throw new Error('Missing configured-source filter')
    await fill(filter, 'repo-59.')
    expect(document.querySelectorAll('button[aria-label^="Choose configured source:"]')).toHaveLength(1)
    expect(button('Select for upgrade').disabled).toBe(true)
  })
})

describe('public discovery keeps the existing configuration contract', () => {
  const chart: ArtifactHubChart = { packageId: 'podinfo', name: 'podinfo', version: '6.15.0', stars: 0, repository: { name: 'podinfo', url: recordedURL } }

  it('adds only the selected HTTP result without binding or selecting an upgrade', async () => {
    state.searchCharts = [chart]
    state.repositories = [{ name: 'podinfo', url: 'https://other.example.test' }]
    const select = vi.fn()
    await renderDialog(select)
    await searchPublic()
    expect(button('Add repository').disabled).toBe(true)
    await choosePublic(recordedURL)
    await click('Add repository')
    expect(state.addRepository).toHaveBeenCalledWith({ name: 'podinfo-2', url: recordedURL }, expect.anything())
    expect(state.associateSource).not.toHaveBeenCalled()
    expect(select).not.toHaveBeenCalled()
  })

  it('registers the OCI parent rather than duplicating the chart name', async () => {
    const url = 'oci://ghcr.io/stefanprodan/charts/podinfo'
    state.searchCharts = [{ ...chart, repository: { name: 'podinfo', url } }]
    await renderDialog()
    await searchPublic()
    await choosePublic(url)
    await click('Register OCI source')
    expect(state.addOCI).toHaveBeenCalledWith('oci://ghcr.io/stefanprodan/charts', expect.anything())
    expect(state.associateSource).not.toHaveBeenCalled()
  })

  it('sends an already configured result back to its existing source instead of adding it again', async () => {
    state.searchCharts = [chart]
    state.repositories = [{ name: 'existing', url: `${recordedURL}/` }]
    await renderDialog()
    await searchPublic()
    await choosePublic(recordedURL)
    await click('Show in My Repos')
    expect(document.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toBe('My Repos')
    expect(state.addRepository).not.toHaveBeenCalled()
  })

  it('does not let non-owners add a discovered source', async () => {
    state.searchCharts = [chart]
    state.isOwner = false
    await renderDialog()
    await searchPublic()
    await choosePublic(recordedURL)
    expect(button('Add repository').disabled).toBe(true)
  })

  it('recognizes equivalent OCI hosts while keeping repository path case significant', async () => {
    const url = 'oci://registry.example.test/Team/podinfo'
    state.searchCharts = [{ ...chart, repository: { name: 'podinfo', url } }]
    state.ociSources = ['oci://REGISTRY.example.test/Team/']
    await renderDialog()
    await searchPublic()
    await choosePublic(url)
    expect(button('Show in My Repos')).toBeDefined()
    state.ociSources = ['oci://registry.example.test/team']
    await renderDialog()
    expect(button('Register OCI source')).toBeDefined()
  })
})
