// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { EndpointSliceRenderer } from './EndpointSliceRenderer'
import { initNavigationMap, resetNavigationMap } from '../../../utils/navigation'

describe('EndpointSliceRenderer target navigation', () => {
  it('navigates exact target identity, controller-written Pod targets and Node placement', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    initNavigationMap([{ group: 'custom.example.io', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: true, verbs: ['get'] }])
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    const click = async (label: string) => {
      const button = [...container.querySelectorAll('button')].find(button => button.textContent === label)
      expect(button).toBeDefined()
      await act(async () => button!.click())
    }
    try {
      await act(async () => root.render(<EndpointSliceRenderer onNavigate={onNavigate} data={{ metadata: { namespace: 'slice-team' }, endpoints: [
        { addresses: ['10.0.0.1'], nodeName: 'worker', targetRef: { apiVersion: 'custom.example.io/v1', kind: 'Node', name: 'custom-target' } },
        { addresses: ['10.0.0.2'], targetRef: { apiVersion: 'v1', kind: 'Pod', namespace: 'target-team', name: 'backend' } },
        { addresses: ['10.0.0.3'], targetRef: { kind: 'Pod', namespace: 'slice-team', name: 'controller-written', uid: 'pod-uid' } },
        { addresses: ['10.0.0.4'], targetRef: { kind: 'Widget', namespace: 'slice-team', name: 'unknown-api' } },
      ] }} />))
      await click('Node custom-target')
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Node', group: 'custom.example.io', namespace: '', name: 'custom-target' })
      await click('Pod target-team/backend')
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Pod', group: '', namespace: 'target-team', name: 'backend' })
      await click('Pod slice-team/controller-written')
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Pod', group: '', namespace: 'slice-team', name: 'controller-written' })
      await click('worker')
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Node', group: '', namespace: '', name: 'worker' })
      expect([...container.querySelectorAll('button')].some(button => button.textContent?.includes('unknown-api'))).toBe(false)
      expect(container.textContent).toContain('unknown-api')
    } finally {
      await act(async () => root.unmount())
      resetNavigationMap()
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })
})
