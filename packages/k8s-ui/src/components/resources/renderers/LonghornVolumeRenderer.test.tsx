import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { LonghornVolumeRenderer } from './LonghornVolumeRenderer'

const volume = (status: Record<string, unknown>) => ({
  apiVersion: 'longhorn.io/v1beta2', kind: 'Volume', metadata: { name: 'pvc-1', namespace: 'longhorn-system' },
  spec: { size: '10737418240', numberOfReplicas: 3, dataLocality: 'disabled', accessMode: 'rwo', frontend: 'blockdev', dataEngine: 'v1' },
  status: { currentNodeID: 'worker-1', actualSize: 2147483648, kubernetesStatus: { namespace: 'shop', pvcName: 'data-db-0', workloadsStatus: [{ podName: 'db-0', workloadName: 'db', workloadType: 'StatefulSet' }] }, ...status },
})

describe('LonghornVolumeRenderer', () => {
  it('reads degraded as a rebuild in progress with its duration and progress, not as a failure', () => {
    vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-15T12:00:00Z'))
    const engine = { status: { replicaModeMap: { a: 'RW', b: 'RW', c: 'WO' }, rebuildStatus: { 'tcp://10.0.0.3:10000': { isRebuilding: true, progress: 42, state: 'in_progress' } } } }
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'degraded', lastDegradedAt: '2026-11-15T10:00:00Z' })} engine={engine} />)
    vi.useRealTimers()
    expect(html).toContain('Rebuilding')
    expect(html).toContain('2h')
    expect(html).toContain('42%')
    expect(html).toContain('2 of 3')
    expect(html).not.toContain('Faulted')
  })

  it('shows healthy replicas as unknown without the engine, never the desired count', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'healthy' })} />)
    expect(html).toContain('? of 3')
  })

  it('makes faulted the loud one', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'detached', robustness: 'faulted' })} />)
    expect(html).toContain('Faulted')
    expect(html).toContain('No replica')
  })

  it('names no PVC, pod or workload (last-seen history comes separately)', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'healthy' })} />)
    expect(html).not.toContain('data-db-0')
    expect(html).not.toContain('db-0')
    expect(html).not.toContain('shop')
  })
})
