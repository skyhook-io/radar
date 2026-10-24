import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { BatchExecutionFullscreen } from './BatchExecutionView'
import { ScheduledWorkloadLogsViewer } from '../logs/ScheduledWorkloadLogsViewer'
import { WorkloadLogsTab } from '../dock/WorkloadLogsTab'

const runs = vi.hoisted(() => vi.fn(() => ({ data: { collection: 'runs', runs: [], total: 0, truncated: false } })))

vi.mock('../../api/client', () => ({
  useResource: () => ({}),
  useWorkloadPods: () => ({}),
  useJobSetResources: () => ({ data: undefined, isLoading: false }),
  useWorkloadRuns: runs,
}))
vi.mock('../logs/WorkloadLogsViewer', () => ({ WorkloadLogsViewer: () => null }))

const view = ['team-a', 'team-b']

function runsOptions() {
  return (runs.mock.calls.at(-1) as unknown[] | undefined)?.[4]
}

beforeEach(() => runs.mockClear())

// A ClusterWorkflowTemplate's runs span namespaces; every surface listing them
// narrows them to the view's selection.
describe('cluster-scoped template runs follow the view selection', () => {
  it('in the execution overview', () => {
    renderToStaticMarkup(<BatchExecutionFullscreen kind="ClusterWorkflowTemplate" apiKind="clusterworkflowtemplates" namespace="" name="build" resource={{ spec: {} }} viewNamespaces={view} />)
    expect(runsOptions()).toMatchObject({ clusterScoped: true, namespaces: view })
  })

  it('in the logs tab', () => {
    renderToStaticMarkup(<ScheduledWorkloadLogsViewer kind="clusterworkflowtemplates" namespace="" name="build" viewNamespaces={view} />)
    expect(runsOptions()).toMatchObject({ clusterScoped: true, namespaces: view })
  })

  it('in the dock', () => {
    renderToStaticMarkup(<WorkloadLogsTab namespace="" workloadKind="ClusterWorkflowTemplate" workloadName="build" viewNamespaces={view} />)
    expect(runsOptions()).toMatchObject({ clusterScoped: true, namespaces: view })
  })
})
