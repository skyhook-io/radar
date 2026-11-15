import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

const volume = { apiVersion: 'longhorn.io/v1beta2', kind: 'Volume', metadata: { name: 'pvc-1', namespace: 'longhorn-system' }, spec: { numberOfReplicas: 3 }, status: { state: 'attached', robustness: 'healthy' } }

async function render(engines: { data?: any[]; error?: unknown }) {
  vi.resetModules()
  vi.doMock('../../../api/client', () => ({
    useResources: () => ({ data: engines.data, error: engines.error ?? null, isError: !!engines.error }),
  }))
  const { LonghornVolumeRenderer } = await import('./LonghornVolumeRenderer')
  const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume} />)
  vi.doUnmock('../../../api/client')
  return html
}

describe('LonghornVolumeRenderer (host)', () => {
  it('counts healthy replicas from the volume\'s own Engine', async () => {
    const html = await render({ data: [
      { spec: { volumeName: 'other' }, status: { replicaModeMap: { x: 'RW' } } },
      { spec: { volumeName: 'pvc-1' }, status: { replicaModeMap: { a: 'RW', b: 'RW', c: 'RW' } } },
    ] })
    expect(html).toContain('3 of 3')
  })

  it('says when it could not read the Engine instead of looking checked', async () => {
    const html = await render({ error: new Error('boom') })
    expect(html).toContain('? of 3')
    expect(html).toContain('Could not check')
    expect(html).not.toContain('Engine not running')
  })

  it('reads the active Engine while a volume migrates, and unknown when none is active', async () => {
    const migrating = await render({ data: [
      { spec: { volumeName: 'pvc-1', active: false }, status: { replicaModeMap: { a: 'RW' } } },
      { spec: { volumeName: 'pvc-1', active: true }, status: { replicaModeMap: { a: 'RW', b: 'RW', c: 'RW' } } },
    ] })
    expect(migrating).toContain('3 of 3')
    const ambiguous = await render({ data: [
      { spec: { volumeName: 'pvc-1', active: false }, status: { replicaModeMap: { a: 'RW' } } },
      { spec: { volumeName: 'pvc-1', active: false }, status: { replicaModeMap: { a: 'RW', b: 'RW' } } },
    ] })
    expect(ambiguous).toContain('? of 3')
  })
})
