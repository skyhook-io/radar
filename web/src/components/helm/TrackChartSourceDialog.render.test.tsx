// @vitest-environment jsdom
import { act, type InputHTMLAttributes, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ArtifactHubSourceCandidate, ChartSourceStatus, HelmRepository } from '../../types'
import { TrackChartSourceDialog } from './TrackChartSourceDialog'

const state = vi.hoisted(() => ({
  status: undefined as ChartSourceStatus | undefined,
  repositories: [] as HelmRepository[],
  isOwner: true,
  addRepository: vi.fn(),
  discoverArtifactHub: vi.fn(),
  recoverArtifactHub: vi.fn(),
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
    useHelmSourceStatus: () => ({ data: state.status, error: null, refetch: vi.fn() }),
    useHelmRepositories: () => ({ data: state.repositories, isLoading: false }),
    useHelmOCISources: () => ({ data: [] }),
    useClusterInfo: () => ({ data: { inCluster: false } }),
    useCapabilities: () => ({ data: { configManagement: 'local' } }),
    useCloudRole: () => ({ canAtLeast: () => state.isOwner }),
    useAddHelmRepository: () => ({ mutate: state.addRepository, isPending: false }),
    useAddOCISource: () => idle,
    useRemoveOCISource: () => idle,
    useSetHelmSource: () => idle,
    useDiscoverArtifactHubSources: () => ({ mutateAsync: state.discoverArtifactHub, isPending: false }),
    useRecoverArtifactHubSource: () => ({ mutate: state.recoverArtifactHub, isPending: false }),
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
  state.addRepository.mockReset()
  state.discoverArtifactHub.mockReset()
  state.recoverArtifactHub.mockReset()
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(async () => {
  await act(async () => root.unmount())
  document.body.replaceChildren()
})

async function renderDialog() {
  await act(async () => root.render(<TrackChartSourceDialog open onClose={() => undefined} chartName="podinfo" namespace="web" releaseName="storefront" />))
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

const classicCandidate: ArtifactHubSourceCandidate = {
  type: 'repository',
  reference: 'community',
  url: 'https://charts.example.test',
  repositoryName: 'community',
  publisher: 'Example Org',
  official: true,
  verifiedPublisher: true,
}

describe('TrackChartSourceDialog ArtifactHub recovery', () => {
  beforeEach(() => {
    state.status = { configured: false, available: false }
  })

  it('shows trust metadata without verifying during search', async () => {
    state.discoverArtifactHub.mockResolvedValue([classicCandidate])
    await renderDialog()
    const search = Array.from(document.querySelectorAll('button')).find(button => button.textContent === 'Search ArtifactHub')
    await act(async () => search?.click())

    expect(state.discoverArtifactHub).toHaveBeenCalledOnce()
    expect(state.recoverArtifactHub).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('Example Org')
    expect(document.body.textContent).toContain('Official')
    expect(document.body.textContent).toContain('Verified publisher')
    expect(document.body.textContent).toContain('https://charts.example.test')
  })

  it('verifies only the candidate the operator selects', async () => {
    const ociCandidate: ArtifactHubSourceCandidate = {
      type: 'oci', reference: 'oci://ghcr.io/example/charts/podinfo', repositoryName: 'oci', publisher: 'Other Org', official: false, verifiedPublisher: false,
    }
    state.discoverArtifactHub.mockResolvedValue([classicCandidate, ociCandidate])
    await renderDialog()
    await act(async () => Array.from(document.querySelectorAll('button')).find(button => button.textContent === 'Search ArtifactHub')?.click())

    const verifyButtons = Array.from(document.querySelectorAll('button')).filter(button => button.textContent === 'Verify and use')
    expect(verifyButtons).toHaveLength(2)
    expect(state.recoverArtifactHub).not.toHaveBeenCalled()
    await act(async () => verifyButtons[1]?.click())
    expect(state.recoverArtifactHub).toHaveBeenCalledWith(ociCandidate, expect.anything())
  })

  it('does not offer ArtifactHub while a configured exact source exists', async () => {
    state.status = { configured: false, available: false, candidates: [{ type: 'repository', reference: 'local', url: 'https://local.example.test' }] }
    await renderDialog()
    expect(document.querySelector('[data-testid="artifacthub-recovery"]')).toBeNull()
  })
})
