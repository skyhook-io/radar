// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { CompositeRenderer } from './CompositeRenderer'
import { initNavigationMap, resetNavigationMap } from '../../../utils/navigation'

describe('CompositeRenderer composed-resource links', () => {
  it('resolves a composed Kind within its own API group, not the core Kind it reuses', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    initNavigationMap([
      { group: '', version: 'v1', kind: 'Node', name: 'nodes', namespaced: false, isCrd: false, verbs: ['get'] },
      { group: 'relationships.radar.test', version: 'v1', kind: 'Node', name: 'workerhosts', namespaced: false, isCrd: true, verbs: ['get'] },
    ])
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    try {
      await act(async () => root.render(<CompositeRenderer onNavigate={onNavigate} data={{
        apiVersion: 'platform.example.org/v1alpha1',
        kind: 'Database',
        metadata: { name: 'example' },
        spec: { resourceRefs: [{ apiVersion: 'relationships.radar.test/v1', kind: 'Node', name: 'worker' }] },
      }} />))
      const link = [...container.querySelectorAll('button')].find(button => button.textContent === 'worker')
      expect(link).toBeDefined()
      await act(async () => link!.click())
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'workerhosts', group: 'relationships.radar.test', name: 'worker' }))
    } finally {
      await act(async () => root.unmount())
      resetNavigationMap()
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })
  it('navigates a namespaced XR composed ref into the XR namespace, not cluster scope', async () => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
    initNavigationMap([
      { group: 'apps', version: 'v1', kind: 'StatefulSet', name: 'statefulsets', namespaced: true, isCrd: false, verbs: ['get'] },
    ])
    const container = document.createElement('div')
    const root = createRoot(container)
    const onNavigate = vi.fn()
    try {
      await act(async () => root.render(<CompositeRenderer onNavigate={onNavigate} data={{
        apiVersion: 'platform.example.org/v1alpha1',
        kind: 'Database',
        metadata: { name: 'kivo', namespace: 'kivo' },
        spec: { crossplane: { resourceRefs: [{ apiVersion: 'apps/v1', kind: 'StatefulSet', name: 'kivo-db' }] } },
      }} />))
      const link = [...container.querySelectorAll('button')].find(button => button.textContent === 'kivo-db')
      expect(link).toBeDefined()
      await act(async () => link!.click())
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'statefulsets', namespace: 'kivo', name: 'kivo-db' }))
      // The row does not repeat the namespace the whole panel already lives in.
      expect(container.textContent).not.toMatch(/kivo-dbkivo/)
    } finally {
      await act(async () => root.unmount())
      resetNavigationMap()
      Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: false })
    }
  })
})
