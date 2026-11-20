import { describe, expect, it } from 'vitest'
import { getContainerSquareStates } from './resource-utils'

const waiting = (name: string, reason: string) => ({ name, ready: false, restartCount: 0, state: { waiting: { reason } } })

describe('getContainerSquareStates', () => {
  it('reads containers held at PodInitializing or ContainerCreating as starting, not failed', () => {
    const pod = {
      spec: { containers: [{ name: 'app' }, { name: 'proxy' }], initContainers: [{ name: 'migrate' }, { name: 'seed' }] },
      status: {
        initContainerStatuses: [
          { name: 'migrate', ready: false, restartCount: 0, state: { running: { startedAt: '2026-11-15T10:00:00Z' } } },
          waiting('seed', 'PodInitializing'),
        ],
        containerStatuses: [waiting('app', 'PodInitializing'), waiting('proxy', 'ContainerCreating')],
      },
    }
    expect(getContainerSquareStates(pod).map(s => [s.name, s.status])).toEqual([
      ['migrate', 'running'], ['seed', 'starting'], ['app', 'starting'], ['proxy', 'starting'],
    ])
  })

  it('keeps a real waiting failure as waiting', () => {
    const pod = { spec: { containers: [{ name: 'app' }] }, status: { containerStatuses: [waiting('app', 'CrashLoopBackOff')] } }
    expect(getContainerSquareStates(pod)[0].status).toBe('waiting')
    const init = { spec: { containers: [{ name: 'app' }] }, status: { initContainerStatuses: [waiting('migrate', 'ImagePullBackOff')], containerStatuses: [waiting('app', 'PodInitializing')] } }
    expect(getContainerSquareStates(init).map(s => s.status)).toEqual(['waiting', 'starting'])
  })
})
