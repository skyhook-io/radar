import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

const volume = { apiVersion: 'longhorn.io/v1beta2', kind: 'Volume', metadata: { name: 'pvc-1', namespace: 'longhorn-system' }, spec: { numberOfReplicas: 3 }, status: { state: 'attached', robustness: 'healthy', kubernetesStatus: { namespace: 'shop', pvcName: 'data-db-0', lastPVCRefAt: '' } } }

async function render(engines: { data?: any[]; error?: unknown; isLoading?: boolean }, namespaces?: { name: string }[], authEnabled?: boolean) {
  vi.resetModules()
  vi.doMock('../../../contexts/CapabilitiesContext', () => ({ useCapabilitiesContext: () => ({ authEnabled }) }))
  vi.doMock('../../../api/client', () => ({
    useResources: () => ({ data: engines.data, error: engines.error ?? null, isError: !!engines.error, isLoading: !!engines.isLoading }),
    useNamespaces: () => ({ data: namespaces }),
  }))
  const { LonghornVolumeRenderer } = await import('./LonghornVolumeRenderer')
  const html = renderToStaticMarkup(<LonghornVolumeRenderer data={volume} />)
  vi.doUnmock('../../../api/client')
  vi.doUnmock('../../../contexts/CapabilitiesContext')
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

  it('does not call the Engine stopped while the lookup is still loading', async () => {
    const html = await render({ isLoading: true })
    expect(html).toContain('? of 3')
    expect(html).not.toContain('Engine not running')
  })

  it('says the Engine is not running once the lookup finds none for this volume', async () => {
    const html = await render({ data: [{ spec: { volumeName: 'other' }, status: { replicaModeMap: { x: 'RW' } } }] })
    expect(html).toContain('? of 3 (Engine not running)')
  })

  it('shows Longhorn\'s recorded PVC only when the caller\'s namespace list includes it', async () => {
    expect(await render({ data: [] }, [{ name: 'shop' }])).toContain('shop/data-db-0')
    expect(await render({ data: [] }, [{ name: 'other' }])).not.toContain('data-db-0')
    expect(await render({ data: [] })).not.toContain('data-db-0')
  })

  it('keeps the history of a deleted namespace when auth is off, and decides from the list while capabilities load', async () => {
    expect(await render({ data: [] }, [{ name: 'other' }], false)).toContain('shop/data-db-0')
    expect(await render({ data: [] }, [{ name: 'other' }], undefined)).not.toContain('data-db-0')
    expect(await render({ data: [] }, [{ name: 'other' }], true)).not.toContain('data-db-0')
  })
})
