// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { IngressRenderer } from './IngressRenderer'
import { IngressClassRenderer } from './IngressClassRenderer'
import { ingressClassParametersResourceRef } from '../../../utils/ingress-class-references'

describe('ingressClassParametersResourceRef', () => {
  it('defaults to the core group and cluster scope, and requires a namespace for namespaced parameters', () => {
    expect(ingressClassParametersResourceRef({ kind: 'IngressClassParams', apiGroup: 'elbv2.k8s.aws', name: 'shared' }))
      .toEqual({ kind: 'IngressClassParams', group: 'elbv2.k8s.aws', namespace: '', name: 'shared' })
    expect(ingressClassParametersResourceRef({ kind: 'ConfigMap', scope: 'Namespace', namespace: 'controller', name: 'settings' }))
      .toEqual({ kind: 'ConfigMap', group: '', namespace: 'controller', name: 'settings' })
    expect(ingressClassParametersResourceRef({ kind: 'ConfigMap', scope: 'Namespace', name: 'settings' })).toBeNull()
  })
})

describe('Ingress class navigation', () => {
  it('links the Ingress class and both parameter scopes, but leaves the legacy annotation as text', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    const click = async (name: string) => {
      const button = [...container.querySelectorAll('button')].find(button => button.textContent === name)
      expect(button).toBeDefined()
      await act(async () => button!.click())
    }
    try {
      await act(async () => root.render(<IngressRenderer onNavigate={onNavigate} data={{ metadata: { namespace: 'app', annotations: { 'kubernetes.io/ingress.class': 'old-controller' } }, spec: { ingressClassName: 'public' } }} />))
      await click('public')
      expect(onNavigate).toHaveBeenLastCalledWith({ kind: 'ingressclasses', group: 'networking.k8s.io', namespace: '', name: 'public' })

      await act(async () => root.render(<IngressRenderer onNavigate={onNavigate} data={{ metadata: { annotations: { 'kubernetes.io/ingress.class': 'old-controller' } }, spec: {} }} />))
      expect(container.textContent).toContain('old-controller')
      expect([...container.querySelectorAll('button')].some(button => button.textContent === 'old-controller')).toBe(false)

      for (const parameters of [
        { name: 'shared', kind: 'IngressClassParams', apiGroup: 'elbv2.k8s.aws' },
        { name: 'settings', kind: 'ConfigMap', scope: 'Namespace' as const, namespace: 'controller' },
      ]) {
        await act(async () => root.render(<IngressClassRenderer onNavigate={onNavigate} data={{ spec: { controller: 'example.test/ingress', parameters } }} />))
        await click(parameters.name)
        expect(onNavigate).toHaveBeenLastCalledWith(ingressClassParametersResourceRef(parameters))
      }
    } finally {
      await act(async () => root.unmount())
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })
})
