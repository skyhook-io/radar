// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkloadLogsTab } from './WorkloadLogsTab'

const state = vi.hoisted(() => ({ replicaSetSupport: true, supportCheckFailed: false, targets: [] as Array<{ kind: string; name: string; role?: string; snapshotOnly?: boolean }> }))
vi.mock('../../api/client', () => ({
  useRadarFeature: () => state.supportCheckFailed
    ? { support: 'unknown', blockedReason: "Couldn't check this cluster's Radar support. Refresh to try again." }
    : state.replicaSetSupport ? { support: 'supported' } : { support: 'unsupported', upgrade: { feature: 'Combined ReplicaSet logs' } },
  useResource: (_kind: string, _namespace: string, name: string) => ({
    data: { metadata: { uid: name }, spec: { replicatedJobs: [{ name: name === 'first' ? 'workers' : 'leader' }] } },
  }),
  useWorkloadRuns: (_kind: string, namespace: string, name: string) => ({
    data: { collection: 'members', runs: [{ group: 'batch', kind: 'jobs', namespace, name: `${name}-0`, phase: 'Running', active: true }], total: 1 },
  }),
}))
vi.mock('../../contexts/CapabilitiesContext', () => ({ useNamespacedCapabilities: () => ({ canViewLogs: true }) }))
vi.mock('../logs/WorkloadLogsViewer', () => ({
  WorkloadLogsViewer: (props: { kind: string; name: string; role?: string; snapshotOnly?: boolean }) => {
    state.targets.push(props)
    return <div data-log-target={`${props.kind}/${props.name}`} />
  },
}))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let root: Root
let element: HTMLDivElement
beforeEach(() => {
  state.targets = []
  state.replicaSetSupport = true
  state.supportCheckFailed = false
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})
async function render(name: string, kind = 'JobSet') {
  await act(async () => root.render(<WorkloadLogsTab namespace="training" workloadKind={kind} workloadName={name} />))
}

describe('dock JobSet logs', () => {
  it.each(['JobSet', 'jobsets'])('routes %s to selected member logs and supports role snapshots', async kind => {
    await render('first', kind)
    expect(element.querySelector('[data-log-target="jobs/first-0"]')).not.toBeNull()
    const scope = element.querySelector<HTMLSelectElement>('#jobset-log-scope')!
    await act(async () => {
      scope.value = 'role'
      scope.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(state.targets.at(-1)).toMatchObject({ kind: 'jobsets', name: 'first', role: 'workers', snapshotOnly: true })
  })

  it('resets aggregate scope and role before rendering another JobSet log source', async () => {
    await render('first')
    const scope = element.querySelector<HTMLSelectElement>('#jobset-log-scope')!
    await act(async () => {
      scope.value = 'role'
      scope.dispatchEvent(new Event('change', { bubbles: true }))
    })
    state.targets = []
    await render('second')
    expect(element.querySelector<HTMLSelectElement>('#jobset-log-scope')?.value).toBe('selected')
    expect(element.textContent).not.toContain('workers')
    expect(state.targets.every(target => target.name === 'second-0' && target.role === undefined)).toBe(true)
    expect(element.querySelector('[data-log-target="jobs/second-0"]')).not.toBeNull()
  })
})

describe('dock ReplicaSet logs', () => {
  it('checks the connected Radar before following a ReplicaSet', async () => {
    await render('web-v2', 'ReplicaSet')
    expect(state.targets.at(-1)).toMatchObject({ kind: 'replicasets', name: 'web-v2' })
    state.targets = []
    state.replicaSetSupport = false
    await render('web-v2', 'replicasets')
    expect(state.targets).toEqual([])
    expect(element.textContent).toContain('newer Radar')
    state.supportCheckFailed = true
    await render('web-v2', 'replicasets')
    expect(state.targets).toEqual([])
    expect(element.textContent).toContain("Couldn't check")
    expect(element.textContent).not.toContain('newer Radar')
  })
})
