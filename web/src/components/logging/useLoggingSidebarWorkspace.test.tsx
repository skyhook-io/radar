import { describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'

const pipeline = vi.hoisted(() => ({ enabled: false as boolean, data: undefined as unknown }))
vi.mock('../../api/loggingPipeline', () => ({
  useLoggingPipeline: (_ns: string[], enabled: boolean) => { pipeline.enabled = enabled; return { data: enabled ? pipeline.data : undefined, support: 'supported' } },
}))
vi.mock('../cnpg/useCNPGNavigate', () => ({ useCNPGNavigate: () => () => {} }))

import { useLoggingSidebarWorkspace } from './useLoggingSidebarWorkspace'

const logging = [{ group: 'logging.banzaicloud.io', version: 'v1beta1', kind: 'Flow', name: 'flows', namespaced: true, verbs: ['list'] }]

function probe(apiResources: unknown[], group?: string) {
  let out: Record<string, SidebarCategoryWorkspace> | undefined
  function Probe() {
    out = useLoggingSidebarWorkspace({ apiResources: apiResources as never, namespaces: [], selectedKind: group ? { group } : null })
    return null
  }
  renderToStaticMarkup(<Probe />)
  return out
}

describe('useLoggingSidebarWorkspace', () => {
  it('is absent without the Logging operator', () => {
    expect(probe([])).toBeUndefined()
  })
  it('adds a Pipeline destination under the Logging category, counted only there', () => {
    pipeline.data = { unrouted: { count: 7, lowerBound: true } }
    const away = probe(logging)
    expect(pipeline.enabled).toBe(false)
    expect(away?.Logging.destinations.map(d => d.label)).toEqual(['Pipeline'])
    expect(away?.Logging.destinations[0].count).toBeUndefined()
    const there = probe(logging, 'logging.banzaicloud.io')
    expect(pipeline.enabled).toBe(true)
    expect(there?.Logging.destinations[0]).toMatchObject({ count: 7, countLowerBound: true })
  })
})
