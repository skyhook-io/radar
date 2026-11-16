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
    expect(html).not.toContain('Engine not running')
  })

  it('keeps the Backups section readable when Longhorn records no backup', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'healthy', lastBackupAt: '' })} />)
    expect(html).toContain('Last backup')
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

  it('reads a healthy volume\'s routine False conditions as fine, not failing', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'healthy', conditions: [
      { type: 'Scheduled', status: 'True' }, { type: 'Restore', status: 'False' }, { type: 'TooManySnapshots', status: 'False' },
      { type: 'WaitForBackingImage', status: 'False' }, { type: 'OfflineRebuilding', status: 'False' },
    ] })} />)
    expect(html).not.toMatch(/failing/i)
  })

  it('says degraded without claiming a rebuild the engine does not report', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'degraded' })} engine={{ status: { replicaModeMap: { a: 'RW', b: 'RW' } } }} />)
    expect(html).toContain('Degraded')
    expect(html).not.toContain('Rebuilding')
  })

  it('says Longhorn cannot place a replica when scheduling failed', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'attached', robustness: 'degraded', conditions: [{ type: 'Scheduled', status: 'False', reason: 'ReplicaSchedulingFailure' }] })} />)
    expect(html).toContain('can&#x27;t place')
    expect(html).not.toContain('Rebuilding')
  })

  it('explains an unknown replica count on a detached volume', () => {
    const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume({ state: 'detached', robustness: 'unknown' })} />)
    expect(html).toContain('? of 3')
    expect(html).toContain('Engine not running')
  })
})
