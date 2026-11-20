// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { bestScore, useCommandItems } from './command-items'
vi.mock('../../api/client', () => ({ useNamespaces: () => ({ data: [] }), useContexts: () => ({ data: [] }) }))
vi.mock('../../api/apiResources', () => ({ CORE_RESOURCES: [], useAPIResources: () => ({ data: [{ group: 'apiextensions.k8s.io', kind: 'CustomResourceDefinition', name: 'customresourcedefinitions', verbs: ['list'] }, { group: '', kind: 'Pod', name: 'pods', verbs: ['list'] }] }) }))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
it('uses the shared CRD aliases in command items', () => {
  let scores: number[] = []
  function Probe() {
    const cb = { onNavigateView: vi.fn(), onNavigateKind: vi.fn(), onSwitchContext: vi.fn(), onSetNamespaces: vi.fn(), onToggleTheme: vi.fn() }
    const items = useCommandItems(cb)
    scores = items.filter(i => i.category === 'Resource Kinds').map(i => bestScore(i, 'crd'))
    return null
  }
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<Probe />))
  expect(scores).toHaveLength(2)
  expect(scores[0]).toBeGreaterThan(0)
  expect(scores[1]).toBe(0)
  act(() => root.unmount())
})
