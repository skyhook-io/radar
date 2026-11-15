import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { controlNamespacesOf, loggingsReadComplete, missingOutputRefs } from './LoggingFlowHostRenderer'

describe('controlNamespacesOf', () => {
  it('lists each Logging control namespace once', () => {
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a' } }, { spec: { controlNamespace: 'a' } }, { spec: {} }], true)).toEqual(['a'])
  })
  it('is empty when a complete read found no Logging', () => {
    expect(controlNamespacesOf([], true)).toEqual([])
  })
  it('is undefined while unknown, so the drawer claims nothing', () => {
    expect(controlNamespacesOf(undefined, true)).toBeUndefined()
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a' } }], false)).toBeUndefined()
  })
  it('is undefined when a Logging takes cluster resources from every namespace', () => {
    expect(controlNamespacesOf([{ spec: { controlNamespace: 'a', allowClusterResourcesFromAllNamespaces: true } }], true)).toBeUndefined()
  })
})

describe('missingOutputRefs', () => {
  const flow = { kind: 'Flow', metadata: { name: 'app', namespace: 'team' }, spec: { globalOutputRefs: ['loki'], localOutputRefs: ['out'] } }
  it('names refs no output of the Flow\'s Logging backs', () => {
    expect(missingOutputRefs(flow, [{ metadata: { name: 'loki', namespace: 'logging' }, spec: { loggingRef: 'other' } }], [{ metadata: { name: 'out', namespace: 'team' }, spec: {} }])).toEqual(['ClusterOutput loki'])
    expect(missingOutputRefs(flow, [{ metadata: { name: 'loki', namespace: 'logging' }, spec: {} }], [{ metadata: { name: 'out', namespace: 'other' }, spec: {} }])).toEqual(['Output out'])
  })
  it('never calls a ref missing when its kind was not read completely', () => {
    expect(missingOutputRefs(flow, undefined, undefined)).toEqual([])
  })
})

describe('loggingsReadComplete', () => {
  it('trusts only a read that says it is complete', () => {
    expect(loggingsReadComplete({ isError: false, coverage: { status: 'complete' } })).toBe(true)
    expect(loggingsReadComplete({ isError: false, coverage: { status: 'partial' } })).toBe(false)
    expect(loggingsReadComplete({ isError: false, coverage: undefined })).toBe(false) // an older Radar's bare array
    expect(loggingsReadComplete({ isError: true, coverage: { status: 'complete' } })).toBe(false)
  })
})

describe('LoggingFlowHostRenderer', () => {
  it('says when it could not read the outputs instead of looking checked', async () => {
    vi.resetModules()
    vi.doMock('../../../api/client', () => ({
      useResources: (kind: string) => kind === 'clusteroutputs'
        ? { data: undefined, error: new Error('boom'), isError: true }
        : { data: [], error: null, isError: false, coverage: { status: 'complete' } },
    }))
    const { LoggingFlowHostRenderer: Host } = await import('./LoggingFlowHostRenderer')
    const html = renderToStaticMarkup(<Host data={{ kind: 'ClusterFlow', metadata: { name: 'all', namespace: 'logging' }, spec: { globalOutputRefs: ['loki'] } }} />)
    expect(html).toContain('Could not check whether this flow&#x27;s outputs exist: boom')
    expect(html).not.toContain('Rejected by the Logging operator')
    vi.doUnmock('../../../api/client')
  })
})
