import { describe, expect, it } from 'vitest'
import type { WorkloadPodContainerInfo, WorkloadPodInfo } from '../types/core'
import { initContainerNames, initializingPodsNotice, containerRole, viewMixesContainers } from './workload-log-sources'

const app = (state: WorkloadPodContainerInfo['state'], reason?: string): WorkloadPodContainerInfo => ({ name: 'kserve-container', ready: state === 'running', restartCount: 0, state, reason })
const init = (state: WorkloadPodContainerInfo['state'], reason?: string, extra: Partial<WorkloadPodContainerInfo> = {}): WorkloadPodContainerInfo => ({ name: 'storage-initializer', init: true, ready: false, restartCount: 3, state, reason, ...extra })
const pod = (name: string, statuses: WorkloadPodContainerInfo[]): WorkloadPodInfo => ({ name, containers: statuses.map(s => s.name), ready: false, containerStatuses: statuses })
const stuck = (name: string) => pod(name, [init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])
const running = (name: string): WorkloadPodContainerInfo => ({ name, ready: true, restartCount: 0, state: 'running' })

describe('initializingPodsNotice', () => {
  it('names the app container and the init container holding it up', () => {
    expect(initializingPodsNotice([stuck('p-1')]))
      .toBe("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    expect(initializingPodsNotice([pod('p-1', [init('running'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer is still running.")
    expect(initializingPodsNotice([pod('p-1', [init('terminated', 'Error'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer exited (Error).")
  })

  it('skips init containers that completed and sidecars that started, and says a starting sidecar is starting', () => {
    const migrate = { ...init('terminated', 'Completed'), name: 'migrate' }
    const proxy = { ...init('running', undefined, { sidecar: true, started: true }), name: 'istio-proxy' }
    expect(initializingPodsNotice([pod('p-1', [migrate, proxy, init('waiting', 'CrashLoopBackOff'), app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    expect(initializingPodsNotice([pod('p-1', [{ ...proxy, started: false }, app('waiting', 'PodInitializing')])]))
      .toBe("kserve-container hasn't started: sidecar istio-proxy hasn't finished starting.")
  })

  it('scopes the claim to the affected Pods', () => {
    const healthy = pod('p-2', [init('terminated', 'Completed'), app('running')])
    expect(initializingPodsNotice([stuck('api-7984cc66dc-tt8nw'), healthy], { podLabel: p => p.name.split('-').slice(-2).join('-') }))
      .toBe("Pod 7984cc66dc-tt8nw: kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    expect(initializingPodsNotice([stuck('p-1'), stuck('p-3'), healthy]))
      .toBe("2 of 3 Pods: kserve-container hasn't started: init container storage-initializer is in CrashLoopBackOff.")
    expect(initializingPodsNotice([stuck('p-1'), pod('p-3', [init('running'), app('waiting', 'PodInitializing')])]))
      .toBe('2 of 2 Pods have app containers waiting on init containers.')
  })

  it('stops once the app container has produced lines', () => {
    expect(initializingPodsNotice([stuck('p-1')], { containersWithOutput: new Set(['p-1/kserve-container']) })).toBe('')
    expect(initializingPodsNotice([stuck('p-1')], { containersWithOutput: new Set(['p-1/storage-initializer']) })).not.toBe('')
  })

  it('stays quiet for running Pods, other selected containers and Radars without container states', () => {
    expect(initializingPodsNotice([pod('p-1', [init('terminated', 'Completed'), app('running')])])).toBe('')
    expect(initializingPodsNotice([stuck('p-1')], { selectedContainer: 'storage-initializer' })).toBe('')
    expect(initializingPodsNotice([stuck('p-1')], { selectedContainer: 'kserve-container' })).not.toBe('')
    expect(initializingPodsNotice([pod('p-1', [{ name: 'storage-initializer', init: true, ready: false, restartCount: 1 }, { name: 'app', ready: false, restartCount: 0 }])])).toBe('')
  })
})

describe('init container identity', () => {
  it('marks a line by Pod and container, and a picker name only when it is init everywhere', () => {
    const proxy = { ...init('running', undefined, { sidecar: true }), name: 'istio-proxy' }
    const other = pod('p-2', [running('storage-initializer')])
    const pods = [pod('p-1', [init('waiting'), proxy, app('waiting')]), other]
    expect(containerRole(pods, 'p-1', 'storage-initializer')).toBe('init')
    expect(containerRole(pods, 'p-2', 'storage-initializer')).toBeUndefined()
    expect(containerRole(pods, 'p-1', 'istio-proxy')).toBe('sidecar')
    expect(containerRole(pods, 'gone', 'storage-initializer')).toBeUndefined()
    expect([...initContainerNames([pod('p-1', [init('waiting'), proxy, app('waiting')])])]).toEqual(['storage-initializer'])
    expect([...initContainerNames([pod('p-1', [init('waiting'), app('waiting')]), other])]).toEqual([])
  })
})

describe('viewMixesContainers', () => {
  it('mixes when a retained line came from an init container or sidecar', () => {
    expect(viewMixesContainers([], [{ pod: 'gone-1', container: 'migrate', containerRole: 'init' }])).toBe(true)
    expect(viewMixesContainers([], [{ pod: 'gone-1', container: 'istio-proxy', containerRole: 'sidecar' }])).toBe(true)
  })

  const completed = { ...init('terminated', 'Completed'), name: 'copyutil' }

  it('treats one app container beside a finished init container as a single source', () => {
    expect(viewMixesContainers([pod('repo-1', [completed, running('repo-server')])], [{ pod: 'repo-1', container: 'repo-server' }])).toBe(false)
  })

  it('mixes for several app containers, an initializing Pod, init output, or lines from several containers', () => {
    expect(viewMixesContainers([pod('kserve-1', [running('manager'), running('kube-rbac-proxy')])], [])).toBe(true)
    expect(viewMixesContainers([stuck('p-1')], [])).toBe(true)
    expect(viewMixesContainers([pod('repo-1', [completed, running('repo-server')])], [{ pod: 'repo-1', container: 'copyutil' }])).toBe(true)
    expect(viewMixesContainers([pod('api-2', [running('app')])], [{ pod: 'api-1', container: 'metrics' }, { pod: 'api-2', container: 'app' }])).toBe(true)
  })

  it('does not count a native sidecar as a second app container', () => {
    const proxy = { ...init('running', undefined, { sidecar: true, started: true }), name: 'istio-proxy' }
    expect(viewMixesContainers([pod('api-1', [proxy, running('api')])], [{ pod: 'api-1', container: 'api' }])).toBe(false)
  })
})
