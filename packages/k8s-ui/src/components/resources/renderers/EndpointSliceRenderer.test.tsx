// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { EndpointSliceRenderer } from './EndpointSliceRenderer'

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
