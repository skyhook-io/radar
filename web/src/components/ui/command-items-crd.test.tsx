// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { bestScore, useCommandItems } from './command-items'
vi.mock('../../api/client', () => ({ useNamespaces: () => ({ data: [] }), useContexts: () => ({ data: [] }) }))
vi.mock('../../api/apiResources', () => ({
  CORE_RESOURCES: [],
  useAPIResources: () => ({
    data: [
      { group: 'apiextensions.k8s.io', kind: 'CustomResourceDefinition', name: 'customresourcedefinitions', verbs: ['list'] },
      { group: '', kind: 'Pod', name: 'pods', verbs: ['list'] },
      { group: 'policy', kind: 'PodDisruptionBudget', name: 'poddisruptionbudgets', verbs: ['list'], shortNames: ['pdb'] },
    ],
  }),
}))
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
it('uses the shared CRD aliases and discovered short names in command items', () => {
  let crdScores: number[] = []
  let pdbScores: number[] = []
  function Probe() {
    const cb = { onNavigateView: vi.fn(), onNavigateKind: vi.fn(), onSwitchContext: vi.fn(), onSetNamespaces: vi.fn(), onToggleTheme: vi.fn() }
    const items = useCommandItems(cb).filter(i => i.category === 'Resource Kinds')
    crdScores = items.map(i => bestScore(i, 'crd'))
    pdbScores = items.map(i => bestScore(i, 'pdb'))
    return null
  }
  const host = document.createElement('div'); const root = createRoot(host)
  act(() => root.render(<Probe />))
  expect(crdScores).toHaveLength(3)
  expect(crdScores[0]).toBeGreaterThan(0)
  expect(crdScores[1]).toBe(0)
  expect(crdScores[2]).toBe(0)
  expect(pdbScores[0]).toBe(0)
  expect(pdbScores[1]).toBe(0)
  expect(pdbScores[2]).toBeGreaterThanOrEqual(100)
  act(() => root.unmount())
})
