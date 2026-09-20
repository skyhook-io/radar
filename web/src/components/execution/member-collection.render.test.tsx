import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkloadRun, WorkloadRunsResponse } from '../../api/client'
import { BatchExecutionFullscreen } from './BatchExecutionView'
import { ScheduledWorkloadLogsViewer, workloadRunLogsKey } from '../logs/ScheduledWorkloadLogsViewer'

const state = vi.hoisted(() => ({
  response: { collection: 'members', runs: [], total: 0, truncated: false } as WorkloadRunsResponse,
  useResource: vi.fn((..._args: unknown[]) => ({ data: { spec: {} } })),
}))

vi.mock('../../api/client', () => ({
  useResource: (...args: unknown[]) => state.useResource(...args),
  useWorkloadPods: () => ({ data: { pods: [], total: 0, truncated: false } }),
  useWorkloadRuns: () => ({ data: state.response }),
}))
vi.mock('../logs/WorkloadLogsViewer', () => ({
  WorkloadLogsViewer: ({ name }: { name: string }) => <div>Logs for {name}</div>,
}))

const member: WorkloadRun = {
  group: 'batch', kind: 'jobs', namespace: 'training', name: 'distributed-workers-0',
  phase: 'Running', active: true,
  jobset: { replicatedJob: 'workers', jobIndex: '0', replicatedJobReplicas: '2', restartAttempt: '0', jobRestartAttempt: '0' },
}

function overview(selectedRunKey?: string) {
  return renderToStaticMarkup(<BatchExecutionFullscreen kind="JobSet" apiKind="jobsets" namespace="training" name="distributed" resource={{ spec: {} }} selectedRunKey={selectedRunKey} />)
}
function logs(selectedRunKey?: string) {
  return renderToStaticMarkup(<ScheduledWorkloadLogsViewer kind="JobSet" namespace="training" name="distributed" selectedRunKey={selectedRunKey} />)
}

beforeEach(() => {
  state.response = { collection: 'members', runs: [member], total: 1, truncated: false }
  state.useResource.mockClear()
})

describe('member collection consumers', () => {
  it('renders nested role, zero index and selected member in Overview and Logs', () => {
    const html = overview('jobs/training/distributed-workers-0')
    expect(html).toContain('Member Jobs')
    expect(html).toContain('workers #0')
    expect(html).toContain('JobSet restart attempt')
    expect(html).not.toContain('Run history')
    expect(state.useResource).toHaveBeenCalledWith('jobs', 'training', member.name, 'batch', expect.objectContaining({ enabled: true }))
    const logHTML = logs('jobs/training/distributed-workers-0')
    expect(logHTML).toContain('workers #0')
    expect(logHTML).toContain(`Logs for ${member.name}`)
  })

  it('does not silently select a different member outside a truncated window', () => {
    state.response = { collection: 'members', runs: [member], total: 201, truncated: true }
    const missing = 'jobs/training/omitted-worker'
    expect(overview(missing)).toContain('Selected Job is not among the shown members')
    const html = logs(missing)
    expect(html).toContain('Select a shown member Job')
    expect(html).not.toContain('Logs for')
  })

  it('uses collection semantics even when there are no members', () => {
    state.response = { collection: 'members', runs: [], total: 0, truncated: false }
    expect(overview()).toContain('Member Jobs')
    expect(overview()).toContain('No child Jobs currently retained')
    expect(logs()).toContain('No child Jobs found')
  })

  it('reads response semantics instead of guessing membership from the parent kind', () => {
    state.response = { collection: 'runs', runs: [{ ...member, jobset: undefined }], total: 1, truncated: false }
    expect(overview()).toContain('Run history')
    expect(overview()).not.toContain('Member Jobs')
    expect(logs()).not.toContain('workers #0')
  })

  it('restarts logs for either native retry scope at the same Job address', () => {
    expect(workloadRunLogsKey({ ...member })).toBe(workloadRunLogsKey(member))
    expect(workloadRunLogsKey({ ...member, jobset: { ...member.jobset, restartAttempt: '1' } })).not.toBe(workloadRunLogsKey(member))
    expect(workloadRunLogsKey({ ...member, jobset: { ...member.jobset, jobRestartAttempt: '1' } })).not.toBe(workloadRunLogsKey(member))
    expect(workloadRunLogsKey({ ...member, jobset: {} })).not.toBe(workloadRunLogsKey(member))
  })
})
