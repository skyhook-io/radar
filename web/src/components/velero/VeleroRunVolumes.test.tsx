// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { guardRadarFeature, type RadarFeatureSupport } from '../../api/radarFeatures'
import type { VeleroRunVolumesResponse, VeleroVolumeOperationsResponse } from '../../api/velero'
import { setApiBase } from '../../api/config'

const state = vi.hoisted(() => ({ support: 'supported' as RadarFeatureSupport, fetch: vi.fn() }))
vi.mock('../../api/client', async (original) => ({
  ...(await original<typeof import('../../api/client')>()),
  useRadarFeature: (feature: 'veleroRunVolumes') => ({
    gatedKey: [state.support],
    guard: <T,>(request: () => Promise<T>) => guardRadarFeature(feature, state.support, { currentVersion: 'v1.16.0' }, request),
  }),
  fetchJSON: (path: string, init: RequestInit) => state.fetch(path, init),
}))

import { ApiError } from '../../api/client'
import { VeleroRunVolumes } from './VeleroRunVolumes'
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let host: HTMLDivElement
let root: Root
let client: QueryClient
const props = { kind: 'backups' as const, namespace: 'velero', name: 'nightly' }
const record = (patch: Partial<VeleroRunVolumesResponse> = {}): VeleroRunVolumesResponse => ({
  volumes: [
    { pvcName: 'native', pvcNamespace: 'app', pvName: 'pv-native', method: 'NativeSnapshot', preserveLocalSnapshot: false, result: 'succeeded', snapshotDataMoved: false, nativeSnapshotInfo: { snapshotHandle: 'snap-123' } },
    { pvcName: 'csi', method: 'CSISnapshot', preserveLocalSnapshot: true, snapshotDataMoved: false, csiSnapshotInfo: { driver: 'ebs.csi.aws.com', size: 1024 } },
    { pvcName: 'moved', method: 'CSISnapshot', snapshotDataMoved: true, snapshotDataMovementInfo: { dataMover: 'velero', operationID: 'op-moved' } },
    { method: 'PodVolumeBackup', result: 'failed', snapshotDataMoved: false, podVolumeInfo: { podName: 'worker', podNamespace: 'app', volumeName: 'data' } },
    { pvcName: 'skipped', snapshotDataMoved: false, skipped: true, skippedReason: 'excluded by policy' },
  ], total: 5, truncated: false, ...patch,
})
const live = (patch: Partial<VeleroVolumeOperationsResponse> = {}): VeleroVolumeOperationsResponse => ({
  observedAt: '2026-11-15T12:00:00Z', runPhase: 'Completed', coverage: { podVolumeBackups: { state: 'full' }, dataUploads: { state: 'full' } }, operations: [], ...patch,
})
function respond(volumes = record(), operations = live()) {
  state.fetch.mockImplementation((path: string) => Promise.resolve(path.includes('volume-operations') ? operations : volumes))
}
async function mount(node: ReactNode = <VeleroRunVolumes {...props} />) {
  await act(async () => { root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>) })
}
async function settle(test: () => boolean) {
  for (let i = 0; i < 50 && !test(); i++) await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)) })
  expect(test()).toBe(true)
}
beforeEach(() => {
  state.support = 'supported'
  state.fetch.mockReset()
  host = document.createElement('div'); document.body.append(host); root = createRoot(host)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(async () => {
  await act(async () => root.unmount())
  client.clear(); host.remove(); setApiBase('/api'); vi.useRealTimers()
})

describe('Volumes tab', () => {
  it('loads on opening: durable POST and operations GET', async () => {
    state.fetch.mockImplementation(() => new Promise(() => {}))
    expect(state.fetch).not.toHaveBeenCalled()
    await mount()
    expect(host.textContent).toContain('Loading volume record')
    expect(host.textContent).toContain('Loading volume operations')
    expect(state.fetch.mock.calls.find(([path]) => path.endsWith('/volumes?'))?.[1].method).toBe('POST')
    expect(state.fetch.mock.calls.some(([path]) => path.endsWith('/volume-operations'))).toBe(true)
  })

  it.each(['inFlight', 'retainedOperations', 'completedEmpty', 'syncing'] as const)('polls operations only when needed: %s', async (scenario) => {
    vi.useFakeTimers()
    respond(record(), live({ runPhase: scenario === 'inFlight' ? 'InProgress' : 'Completed', coverage: { dataUploads: { state: scenario === 'syncing' ? 'syncing' : 'full' } }, operations: scenario === 'retainedOperations' ? [{ kind: 'DataUpload', name: 'done', phase: 'Completed', longRunning: false }] : [] }))
    await mount()
    await act(async () => { await vi.advanceTimersByTimeAsync(50) })
    expect(host.textContent).toContain('snap-123')
    await act(async () => { await vi.advanceTimersByTimeAsync(10_050) })
    expect(state.fetch.mock.calls.filter(([path]) => path.endsWith('/volumes?'))).toHaveLength(1)
    expect(state.fetch.mock.calls.filter(([path]) => path.endsWith('/volume-operations'))).toHaveLength(scenario === 'completedEmpty' ? 1 : 2)
  })

  it('shows all methods, explicit outcomes, skipped reasons and unknown CSI results', async () => {
    respond(); await mount(); await settle(() => host.textContent!.includes('snap-123'))
    for (const text of ['Native snapshot', 'CSI snapshot', 'Copy to the bucket (data mover)', 'File-system backup', 'excluded by policy', 'ebs.csi.aws.com', '1 KB', 'op-moved', 'succeeded', 'failed']) expect(host.textContent).toContain(text)
    const csi = [...host.querySelectorAll('tr')].find((row) => row.textContent!.includes('ebs.csi.aws.com'))!
    expect(csi.textContent).toContain('Not recorded')
    expect(csi.textContent).not.toContain('succeeded')
    const native = [...host.querySelectorAll('tr')].find((row) => row.textContent!.includes('snap-123'))!
    expect(native.textContent).not.toContain('Local snapshot retained')
    expect(csi.textContent).toContain('Local snapshot retained: Yes')
    expect(host.querySelector('a')).toBeNull()
  })

  it('describes restore intent and never renders a result or skipped column', async () => {
    respond(record({ volumes: [{ method: 'PodVolumeRestore', snapshotDataMoved: false, podVolumeInfo: { podName: 'recovered', volumeName: 'data' } }], total: 1 }))
    await mount(<VeleroRunVolumes {...props} kind="restores" />)
    await settle(() => host.textContent!.includes('recovered'))
    expect(host.textContent).toContain('How each volume was to be restored')
    expect(host.textContent).toContain('before plugin operations finish')
    expect(host.textContent).toContain('File-system restore')
    expect([...host.querySelectorAll('th')].map((e) => e.textContent)).not.toContain('Result')
    expect(host.textContent).not.toContain('succeeded')
  })

  it.each([['Velero has no volume record for this run', 404], ['no access to this Velero Backup', 403], ['the object store refused it', 503]])('keeps artifact error distinct from operation data: %s', async (message, status) => {
    state.fetch.mockImplementation((path: string) => path.includes('volume-operations') ? Promise.resolve(live()) : Promise.reject(new ApiError(message, status)))
    await mount(); await settle(() => host.textContent!.includes(message))
    expect(host.textContent).toContain('Retry')
    expect(host.textContent).not.toContain('Velero recorded no volumes')
  })

  it('gates both reads on the new capability', async () => {
    state.support = 'unsupported'
    await mount(); await settle(() => host.textContent!.includes('needs a newer Radar'))
    expect(state.fetch).not.toHaveBeenCalled()
  })

  it('says once that a finished run recorded no volumes', async () => {
    respond(record({ volumes: [], total: 0 }))
    await mount(); await settle(() => host.textContent!.includes('Velero recorded no volumes for this run.'))
    expect(host.textContent!.split('Velero recorded no volumes').length).toBe(2)
    expect(host.textContent).not.toContain('They may have been removed')
    expect(host.textContent).not.toContain('No volume operation objects found yet')
    expect(host.textContent).not.toContain('The volume record does not update with live operations')
    expect(host.textContent).not.toContain('0 recorded volumes')
  })

  it('says operations may have been removed when the record names volumes', async () => {
    respond()
    await mount(); await settle(() => host.textContent!.includes('snap-123') && host.textContent!.includes('They may have been removed'))
    expect(host.textContent).toContain('5 recorded volumes')
    expect(host.textContent).not.toContain('The volume record does not update with live operations')
  })

  it('reports denied, missing and partial operation kinds without implying none were used', async () => {
    respond(record(), live({ coverage: { podVolumeBackups: { state: 'denied' }, dataUploads: { state: 'notInstalled' }, podVolumeRestores: { state: 'partial', allowedNamespaces: ['velero'] } } }))
    await mount(); await settle(() => host.textContent!.includes('Not available now'))
    expect(host.textContent).toContain('Access denied')
    expect(host.textContent).toContain('Only part of this kind')
    expect(host.textContent).toContain('Unavailable kinds do not mean this run used none')
  })

  it('groups failures first and shows ten with show all, progress, node-agent context and timestamps', async () => {
    respond(record(), live({ runPhase: 'InProgress', operations: [
      { kind: 'DataUpload', name: 'complete', phase: 'Completed', longRunning: false },
      ...Array.from({ length: 12 }, (_, i) => ({ kind: 'DataUpload', name: `failure-${i}`, phase: 'Failed', pvc: 'data', pvcNamespace: 'app', longRunning: false, message: 'copy failed' })),
      { kind: 'PodVolumeBackup', name: 'copy', phase: 'InProgress', longRunning: true, nodeAgent: 'notRunning' as const, node: 'node-a', bytesDone: 512, bytesTotal: 1024, started: '2026-11-15T11:00:00Z' },
    ] }))
    await mount(); await settle(() => host.textContent!.includes('failure-0'))
    expect([...host.querySelectorAll('h3')].map((e) => e.textContent)).toEqual(['Failed · 12', 'InProgress · 1', 'Completed · 1'])
    expect(host.textContent).not.toContain('failure-11')
    expect(host.textContent).toContain('512 B / 1 KB')
    expect(host.textContent).toContain('No Running node-agent pod found on this node')
    expect(host.textContent).toContain('node-a')
    const progress = host.querySelector('progress')!
    expect(progress.value).toBe(512); expect(progress.max).toBe(1024)
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Show all 12')!.click())
    expect(host.textContent).toContain('failure-11')
  })

  it.each(['running', 'unknown'] as const)('shows node-agent %s without guessing progress', async (nodeAgent) => {
    respond(record(), live({ operations: [{ kind: 'DataUpload', name: 'stuck', phase: 'Prepared', node: 'node', longRunning: true, nodeAgent }] }))
    await mount(); await settle(() => host.textContent!.includes('stuck'))
    expect(host.textContent).toContain(nodeAgent === 'running' ? 'Node-agent pod phase: Running on this node' : 'Node-agent status unknown')
    expect(host.textContent).toContain('Unknown / Unknown')
  })

  it('does not retain another API base’s rows during a cluster switch', async () => {
    respond(); await mount(); await settle(() => host.textContent!.includes('snap-123'))
    setApiBase('/c/other/api'); state.fetch.mockImplementation(() => new Promise(() => {}))
    await mount(); expect(host.textContent).not.toContain('snap-123')
  })

  it('marks an in-flight record as provisional and refreshes it explicitly', async () => {
    respond(record(), live({ runPhase: 'WaitingForPluginOperations' }))
    await mount(); await settle(() => host.textContent!.includes('snap-123'))
    expect(host.textContent).toContain('Velero may write or update its volume record later')
    expect(host.textContent).toContain('The volume record does not update with live operations')
    expect(host.textContent).toContain('Record read:')
    respond(record(), live({ runPhase: 'Completed' }))
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh operations')!.click())
    await settle(() => !host.textContent!.includes('This run is still in progress'))
    expect(host.textContent).not.toContain('The volume record does not update with live operations')
    expect(host.textContent).toContain('This run is now finished, but the volume record was requested before completion was confirmed')
    expect(state.fetch.mock.calls.filter(([path]) => path.endsWith('/volumes?'))).toHaveLength(1)
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh record')!.click())
    await settle(() => state.fetch.mock.calls.filter(([path]) => path.endsWith('/volumes?')).length === 2)
    await settle(() => !host.textContent!.includes('This run is now finished, but'))
  })

  it('keeps the provisional note when a record requested in flight resolves after completion was seen', async () => {
    let release!: (value: VeleroRunVolumesResponse) => void
    const held = new Promise<VeleroRunVolumesResponse>((resolve) => { release = resolve })
    let operations = live({ runPhase: 'InProgress' })
    state.fetch.mockImplementation((path: string) => path.includes('volume-operations') ? Promise.resolve(operations) : held)
    await mount(); await settle(() => host.textContent!.includes('This run is still in progress'))
    operations = live({ runPhase: 'Completed' })
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh operations')!.click())
    await settle(() => !host.textContent!.includes('This run is still in progress'))
    await act(async () => { release(record()) })
    await settle(() => host.textContent!.includes('snap-123'))
    expect(host.textContent).toContain('This run is now finished, but the volume record was requested before completion was confirmed')
    respond(record(), operations)
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh record')!.click())
    await settle(() => state.fetch.mock.calls.filter(([path]) => path.endsWith('/volumes?')).length === 2)
    await settle(() => !host.textContent!.includes('This run is now finished, but'))
  })

  it('keeps the provisional note when a refresh after completion fails', async () => {
    respond(record(), live({ runPhase: 'InProgress' }))
    await mount(); await settle(() => host.textContent!.includes('snap-123'))
    respond(record(), live({ runPhase: 'Completed' }))
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh operations')!.click())
    await settle(() => host.textContent!.includes('This run is now finished, but'))
    state.fetch.mockImplementation((path: string) => path.includes('volume-operations') ? Promise.resolve(live()) : Promise.reject(new ApiError('record unavailable', 503)))
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh record')!.click())
    await settle(() => host.textContent!.includes('record unavailable'))
    expect(host.textContent).toContain('This run is now finished, but the volume record was requested before completion was confirmed')
  })

  it('shows truncation and retries operations independently', async () => {
    state.fetch.mockImplementation((path: string) => path.includes('volume-operations') ? Promise.reject(new ApiError('operations denied', 403)) : Promise.resolve(record({ total: 5001, truncated: true })))
    await mount(); await settle(() => host.textContent!.includes('operations denied'))
    expect(host.textContent).toContain('first 5,000 of 5001')
    respond()
    await act(async () => [...host.querySelectorAll('button')].find((b) => b.textContent === 'Refresh operations')!.click())
    await settle(() => !host.textContent!.includes('operations denied'))
    expect(state.fetch.mock.calls.filter(([path]) => path.endsWith('/volumes?'))).toHaveLength(1)
  })
})
