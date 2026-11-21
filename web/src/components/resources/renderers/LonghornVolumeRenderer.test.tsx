import { beforeAll, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

const volume = { apiVersion: 'longhorn.io/v1beta2', kind: 'Volume', metadata: { name: 'pvc-1', namespace: 'longhorn-system' }, spec: { numberOfReplicas: 3 }, status: { state: 'attached', robustness: 'healthy' } }

async function render(engines: { data?: any[]; error?: unknown; isLoading?: boolean; coverage?: { status: string } }) {
  vi.resetModules()
  vi.doMock('../../../api/client', () => ({
    useResources: () => ({ data: engines.data, coverage: engines.coverage, error: engines.error ?? null, isError: !!engines.error, isLoading: !!engines.isLoading }),
  }))
  const { LonghornVolumeRenderer } = await import('./LonghornVolumeRenderer')
  const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume} />)
  vi.doUnmock('../../../api/client')
  return html
}

describe('LonghornVolumeRenderer (host)', () => {
  // The first dynamic import transforms the renderer's whole k8s-ui graph; pay
  // that once here so each test's own timeout still catches a real hang.
  beforeAll(async () => {
    await render({})
  }, 30_000)

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

  it('does not call the Engine stopped while the lookup is still loading', async () => {
    const html = await render({ isLoading: true })
    expect(html).toContain('? of 3')
    expect(html).not.toContain('Engine not running')
  })

  it('says the Engine is not running once the lookup finds none for this volume', async () => {
    const html = await render({ data: [{ spec: { volumeName: 'other' }, status: { replicaModeMap: { x: 'RW' } } }], coverage: { status: 'complete' } })
    expect(html).toContain('? of 3 (Engine not running)')
  })

  it('does not call the Engine stopped when the read was partial or its coverage unknown', async () => {
    for (const coverage of [{ status: 'partial' }, { status: 'unknown' }, undefined]) {
      const html = await render({ data: [], coverage })
      expect(html).toContain('? of 3')
      expect(html).not.toContain('Engine not running')
    }
  })

  it('reads unknown when more than one Engine is marked active', async () => {
    const html = await render({ data: [
      { spec: { volumeName: 'pvc-1', active: true }, status: { replicaModeMap: { a: 'RW' } } },
      { spec: { volumeName: 'pvc-1', active: true }, status: { replicaModeMap: { a: 'RW', b: 'RW' } } },
    ], coverage: { status: 'complete' } })
    expect(html).toContain('? of 3')
    expect(html).not.toContain('1 of 3')
    expect(html).not.toContain('2 of 3')
  })

  it('drops the last count when a refresh fails', async () => {
    const html = await render({ data: [{ spec: { volumeName: 'pvc-1', active: true }, status: { replicaModeMap: { a: 'RW', b: 'RW', c: 'RW' } } }], error: new Error('boom') })
    expect(html).toContain('? of 3')
    expect(html).not.toContain('3 of 3')
    expect(html).toContain('Could not check')
  })
})
