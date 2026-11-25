import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { BatchExecutionFullscreen } from './BatchExecutionView'

const run: { group: string; kind: string; namespace: string; name: string; phase: string; active: boolean } = { group: 'argoproj.io', kind: 'workflows', namespace: 'ci', name: 'nightly-abc12', phase: 'Running', active: true }
const child = { apiVersion: 'argoproj.io/v1alpha1', kind: 'Workflow', metadata: { name: 'nightly-abc12', namespace: 'ci', uid: 'uid-1' }, spec: {}, status: { phase: 'Running' } }
const selected = vi.hoisted(() => ({ data: undefined as unknown, error: undefined as unknown }))

vi.mock('../../api/client', () => ({
  useResource: () => ({ data: selected.data, error: selected.error }),
  useWorkloadPods: () => ({}),
  useJobSetResources: () => ({ data: undefined, isLoading: false }),
  useWorkloadRuns: () => ({ data: { collection: 'runs', runs: [run], total: 1, truncated: false } }),
}))
vi.mock('./WorkflowRunStopAction', () => ({
  WorkflowRunStopAction: ({ workflow, namespace, name }: { workflow?: { metadata: { uid: string } }; namespace: string; name: string }) =>
    <span data-stop-action={`${namespace}/${name}/${workflow?.metadata.uid ?? 'none'}`} />,
}))

const key = 'workflows/ci/nightly-abc12'

describe('Stop in the run view', () => {
  it('offers Stop for the selected child Workflow of a CronWorkflow', () => {
    selected.data = child
    selected.error = undefined
    const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="CronWorkflow" apiKind="cronworkflows" namespace="ci" name="nightly" resource={{ spec: {} }} selectedRunKey={key} />)
    expect(html).toContain('data-stop-action="ci/nightly-abc12/uid-1"')
  })

  it('shows a requested shutdown beside the run state', () => {
    selected.data = { ...child, spec: { shutdown: 'Terminate' } }
    selected.error = undefined
    const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="CronWorkflow" apiKind="cronworkflows" namespace="ci" name="nightly" resource={{ spec: {} }} selectedRunKey={key} />)
    expect(html).toContain('Terminating')
  })

  it('accepts a run row without a group, as an older Radar sends it', () => {
    selected.data = child
    selected.error = undefined
    run.group = undefined as unknown as string
    try {
      const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="CronWorkflow" apiKind="cronworkflows" namespace="ci" name="nightly" resource={{ spec: {} }} selectedRunKey={key} />)
      expect(html).toContain('data-stop-action="ci/nightly-abc12/uid-1"')
    } finally {
      run.group = 'argoproj.io'
    }
  })

  it('withholds the object after a failed refetch, keeping only the last copy', () => {
    selected.data = child
    selected.error = new Error('workflow not found')
    const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="CronWorkflow" apiKind="cronworkflows" namespace="ci" name="nightly" resource={{ spec: {} }} selectedRunKey={key} />)
    expect(html).toContain('data-stop-action="ci/nightly-abc12/none"')
    selected.error = undefined
  })

  it('waits for the selected run’s own object, never acting on a previous selection', () => {
    selected.data = { ...child, metadata: { ...child.metadata, name: 'nightly-old' } }
    selected.error = undefined
    const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="CronWorkflow" apiKind="cronworkflows" namespace="ci" name="nightly" resource={{ spec: {} }} selectedRunKey={key} />)
    // Mounted, so a request in flight keeps its dialog, but given no object to act on.
    expect(html).toContain('data-stop-action="ci/nightly-abc12/none"')
  })

  it('leaves a standalone Workflow to its action bar', () => {
    const html = renderToStaticMarkup(<BatchExecutionFullscreen kind="Workflow" apiKind="workflows" namespace="ci" name="nightly-abc12" resource={child} selectedRunKey={key} />)
    expect(html).not.toContain('data-stop-action')
  })
})
