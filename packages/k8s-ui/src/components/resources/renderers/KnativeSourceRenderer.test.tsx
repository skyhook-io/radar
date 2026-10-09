// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { PingSourceRenderer } from './KnativeSourceRenderer'
import { initNavigationMap, resetNavigationMap } from '../../../utils/navigation'

describe('Knative sink links', () => {
  it('keep the sink reference API group instead of resolving to the core Kind', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    initNavigationMap([
      { group: '', version: 'v1', kind: 'Service', name: 'services', namespaced: true, isCrd: false, verbs: ['get'] },
      { group: 'serving.knative.dev', version: 'v1', kind: 'Service', name: 'services', namespaced: true, isCrd: true, verbs: ['get'] },
      { group: 'relationships.radar.test', version: 'v1', kind: 'Service', name: 'virtualservices', namespaced: true, isCrd: true, verbs: ['get'] },
    ])
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    const clickSink = async (sinkRef: Record<string, string>) => {
      await act(async () => root.render(<PingSourceRenderer onNavigate={onNavigate} data={{
        metadata: { namespace: 'team', name: 'ping' },
        spec: { schedule: '* * * * *', sink: { ref: sinkRef } },
      }} />))
      const link = [...container.querySelectorAll('button')].find(button => button.textContent === sinkRef.name)
      expect(link).toBeDefined()
      await act(async () => link!.click())
    }
    try {
      await clickSink({ apiVersion: 'serving.knative.dev/v1', kind: 'Service', name: 'knative-backend' })
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'services', group: 'serving.knative.dev', namespace: 'team', name: 'knative-backend' }))
      await clickSink({ apiVersion: 'relationships.radar.test/v1', kind: 'Service', name: 'custom-backend' })
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'virtualservices', group: 'relationships.radar.test', name: 'custom-backend' }))
      await clickSink({ apiVersion: 'v1', kind: 'Service', name: 'core-backend' })
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'services', name: 'core-backend' }))
      expect(onNavigate.mock.lastCall?.[0].group ?? '').toBe('')
    } finally {
      await act(async () => root.unmount())
      resetNavigationMap()
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })
})
