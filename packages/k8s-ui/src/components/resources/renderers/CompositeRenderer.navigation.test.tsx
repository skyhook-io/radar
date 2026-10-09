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
})
