// @vitest-environment jsdom
import { act, type InputHTMLAttributes, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChartSourceStatus, HelmRepository, SourceUpgradeSelection } from '../../types'
import { TrackChartSourceDialog } from './TrackChartSourceDialog'

const state = vi.hoisted(() => ({
  status: undefined as ChartSourceStatus | undefined,
  repositories: [] as HelmRepository[],
  isOwner: true,
  featureSupported: true,
  associateSource: vi.fn(),
  addRepository: vi.fn(),
}))

vi.mock('@skyhook-io/k8s-ui/components/ui/DialogPortal', () => ({
  DialogPortal: ({ open, children }: { open: boolean; children: ReactNode }) => (open ? <div>{children}</div> : null),
}))
vi.mock('@skyhook-io/k8s-ui', () => ({
  Input: (props: InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
}))
vi.mock('../settings/OperatorManagedNotice', () => ({ OperatorManagedNotice: () => null }))
vi.mock('../../api/client', () => {
  const idle = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }
  return {
    useRadarFeature: () => ({ support: state.featureSupported ? 'supported' : 'unsupported' }),
    useHelmSourceStatus: () => ({ data: state.status, error: null, refetch: vi.fn() }),
    useHelmRepositories: () => ({ data: state.repositories, isLoading: false }),
    useHelmOCISources: () => ({ data: [] }),
    useClusterInfo: () => ({ data: { inCluster: false } }),
    useCapabilities: () => ({ data: { configManagement: 'local' } }),
    useCloudRole: () => ({ canAtLeast: () => state.isOwner }),
    useAddHelmRepository: () => ({ mutate: state.addRepository, isPending: false }),
    useAddOCISource: () => idle,
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
  state.isOwner = true
  state.featureSupported = true
  state.associateSource.mockReset()
  state.addRepository.mockReset()
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
})

async function renderDialog(onSelectUpgradeSource?: (selection: SourceUpgradeSelection) => void) {
  await act(async () => root.render(<TrackChartSourceDialog onSelectUpgradeSource={onSelectUpgradeSource} open onClose={() => undefined} chartName="podinfo" namespace="web" releaseName="storefront" />))
}

function addRecordedButton() {
  return Array.from(document.querySelectorAll('button')).find(button => button.textContent?.includes('and use it'))
}

describe('TrackChartSourceDialog one-click add of the recorded repository', () => {
  it('adds the recorded URL under the recorded name when that name is free', async () => {
    await renderDialog()
    const button = addRecordedButton()
    expect(button?.textContent).toBe('Add stefanprodan.github.io/podinfo and use it')
    expect(button?.disabled).toBe(false)
    expect(document.body.textContent).toContain('Saved in Helm as podinfo, the name it was recorded under.')

    await act(async () => button?.click())
    expect(state.addRepository).toHaveBeenCalledWith(
      { name: 'podinfo', url: recordedURL, namespace: 'web', releaseName: 'storefront' },
      expect.anything(),
    )
  })

  it('never reuses a recorded name that points at a different repository here', async () => {
    state.repositories = [
      { name: 'podinfo', url: 'https://impostor.example.test/charts' },
      { name: 'podinfo-2', url: 'https://other.example.test' },
    ]
    await renderDialog()
    expect(document.body.textContent).toContain('Saved in Helm as podinfo-3. The name podinfo already points to a different repository here.')

    await act(async () => addRecordedButton()?.click())
    expect(state.addRepository).toHaveBeenCalledWith(
      { name: 'podinfo-3', url: recordedURL, namespace: 'web', releaseName: 'storefront' },
      expect.anything(),
    )
  })

  it('is disabled for a non-owner, who is told who can add sources', async () => {
    state.isOwner = false
    await renderDialog()
    expect(addRecordedButton()?.disabled).toBe(true)
    expect(document.body.textContent).toContain('Only organization owners can add or remove chart sources.')
  })

  it('is not offered when the recorded source is configured, OCI, or not recorded', async () => {
    for (const status of [
      { ...movedMachine, configured: true },
      { recorded: { type: 'oci' as const, reference: 'oci://ghcr.io/example/charts' }, configured: false, available: false },
      { configured: false, available: false },
    ]) {
      state.status = status
      await renderDialog()
      expect(addRecordedButton()).toBeUndefined()
    }
  })
})


describe('explicit source choices', () => {
  it('keeps the recorded source visible and selects a different publisher without associating it', async () => {
    const recorded = { type: 'repository' as const, reference: 'official', url: 'https://official.example.test/charts' }
    const other = { type: 'repository' as const, reference: 'other', url: 'https://other.example.test/charts' }
    state.status = { recorded, configured: true, available: true, confirmed: true, revision: 4, candidates: [recorded, other], suggestions: [
      { source: recorded, current: true, hasInstalledVersion: true, state: 'available' },
      { source: other, current: false, hasInstalledVersion: true, state: 'available' },
    ] }
    const select = vi.fn()
    await renderDialog(select)
    const choices = Array.from(document.querySelectorAll('button')).filter(button => button.textContent?.includes('Select for upgrade'))
    expect(choices).toHaveLength(2)
    expect(choices[0].getAttribute('aria-label')).toContain(recorded.url)
    expect(choices[1].parentElement?.textContent).toContain('Other source')
    await act(async () => choices[1].click())
    expect(select).toHaveBeenCalledWith({source: other, expectedRevision: 4, expectedSource: recorded})
    expect(state.associateSource).not.toHaveBeenCalled()
  })

  it('offers a pruned mirror for an explicit upgrade without claiming it supplied the installed version', async () => {
    const mirror = { type: 'repository' as const, reference: 'mirror', url: 'https://mirror.example.test/charts' }
    state.status = { configured: false, available: false, confirmed: false, revision: 1, candidates: [], suggestions: [
      { source: mirror, current: false, hasInstalledVersion: false, state: 'available' },
    ] }
    const select = vi.fn()
    await renderDialog(select)
    const button = Array.from(document.querySelectorAll('button')).find(item => item.textContent?.includes('Select for upgrade'))
    expect(button).toBeDefined()
    await act(async () => button?.click())
    expect(select).toHaveBeenCalledWith({source: mirror, expectedRevision: 1, expectedSource: null})
    expect(state.associateSource).not.toHaveBeenCalled()
  })

  it('does not offer source switching on a backend without the capability', async () => {
    state.featureSupported = false
    state.status = { configured: false, available: false, revision: 1, suggestions: [
      { source: {type: 'repository', reference: 'mirror', url: 'https://mirror.example.test/charts'}, current: false, hasInstalledVersion: false, state: 'available' },
    ] }
    await renderDialog(vi.fn())
    expect(document.body.textContent).not.toContain('Select for upgrade')
  })
})


describe('OCI original-source verification', () => {
  it('can verify and record an unchecked registered OCI source without upgrading', async () => {
    const source = { type: 'oci' as const, reference: 'oci://registry.example.test/team/podinfo' }
    state.status = { configured: false, available: false, revision: 1, confirmed: false, candidates: [], suggestions: [
      {source, current: false, hasInstalledVersion: false, state: 'unchecked'},
    ] }
    const select = vi.fn()
    await renderDialog(select)
    const button = Array.from(document.querySelectorAll('button')).find(item => item.textContent === 'Verify and record original source')
    expect(button).toBeDefined()
    await act(async () => button?.click())
    expect(state.associateSource).toHaveBeenCalledWith(source, expect.anything())
    expect(select).not.toHaveBeenCalled()
  })
})

it('shows the no-source hint when the backend omits empty suggestions', async () => {
 state.status = { configured: false, available: false, revision: 1 }
 await renderDialog(vi.fn())
 expect(document.body.textContent).toContain('No configured sources found. Add a repository or OCI source.')
})
