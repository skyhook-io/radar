// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { EndpointSliceRenderer } from './EndpointSliceRenderer'
import { initNavigationMap, resetNavigationMap } from '../../../utils/navigation'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

it('navigates to the labeled Service as a core-group resource', async () => {
  const onNavigate = vi.fn()
  const element = document.createElement('div')
  const root = createRoot(element)
  const slice = {
    apiVersion: 'discovery.k8s.io/v1',
    kind: 'EndpointSlice',
    metadata: { name: 'web-abc', namespace: 'team', labels: { 'kubernetes.io/service-name': 'web' } },
  }
  await act(async () => { root.render(<EndpointSliceRenderer data={slice} onNavigate={onNavigate} />) })
  const button = [...element.querySelectorAll('button')].find(b => b.textContent === 'web')
  expect(button).toBeDefined()
  await act(async () => { button!.click() })
  expect(onNavigate).toHaveBeenCalledWith({ kind: 'Service', group: '', namespace: 'team', name: 'web' })
  await act(async () => root.unmount())
})

it('navigates exact target API identity and cluster-scoped placement', async () => {
  initNavigationMap([{ group: 'custom.example.io', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: true, verbs: ['get'] }])
  const element = document.createElement('div'); document.body.append(element)
  const root = createRoot(element); const onNavigate = vi.fn()
  try {
    await act(async () => root.render(<EndpointSliceRenderer data={{ metadata: { namespace: 'slice-team' }, endpoints: [
      { addresses: ['10.0.0.1'], nodeName: 'worker', targetRef: { apiVersion: 'custom.example.io/v1', kind: 'Node', name: 'custom-target' } },
      { addresses: ['10.0.0.2'], targetRef: { apiVersion: 'v1', kind: 'Pod', namespace: 'target-team', name: 'backend' } },
      { addresses: ['10.0.0.3'], targetRef: { kind: 'Pod', namespace: 'target-team', name: 'ambiguous' } },
    ] }} onNavigate={onNavigate} />))
    const click = async (label: string) => {
      const button = Array.from(element.querySelectorAll('button')).find(b => b.textContent === label)
      expect(button).toBeDefined()
      await act(async () => button!.click())
    }
    await click('Node custom-target')
    expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Node', group: 'custom.example.io', namespace: '', name: 'custom-target' })
    await click('Pod target-team/backend')
    expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Pod', group: '', namespace: 'target-team', name: 'backend' })
    await click('worker')
    expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'Node', group: '', namespace: '', name: 'worker' })
    expect(Array.from(element.querySelectorAll('button')).some(b => b.textContent?.includes('ambiguous'))).toBe(false)
    expect(element.textContent).toContain('Pod target-team/ambiguous')
  } finally {
    await act(async () => root.unmount()); element.remove(); resetNavigationMap()
  }

})
