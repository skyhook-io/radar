import { describe, expect, it } from 'vitest'
import type { TopologyNode } from '@skyhook-io/k8s-ui/types/core'
import { isInNamespaceScope, scopeNodesToNamespaces } from './topology-namespace'

function node(id: string, kind: string, namespace?: string): TopologyNode {
  return {
    id,
    kind,
    name: id,
    status: 'healthy',
    data: namespace === undefined ? {} : { namespace },
  } as TopologyNode
}

const nodes = [
  node('web', 'Deployment', 'prod'),
  node('ing', 'Ingress', 'staging'),
  node('db', 'StatefulSet', 'prod'),
  node('node-1', 'Node'),          // cluster-scoped
  node('pv-1', 'PersistentVolume'), // cluster-scoped
]

describe('scopeNodesToNamespaces', () => {
  it('returns every node when no namespace is selected', () => {
    expect(scopeNodesToNamespaces(nodes, [])).toHaveLength(5)
  })

  it('keeps only the selected namespace, plus cluster-scoped nodes', () => {
    const scoped = scopeNodesToNamespaces(nodes, ['prod'])
    expect(scoped.map(n => n.id)).toEqual(['web', 'db', 'node-1', 'pv-1'])
  })

  it('drops a kind that exists only outside the scope', () => {
    const scoped = scopeNodesToNamespaces(nodes, ['prod'])
    expect(scoped.some(n => n.kind === 'Ingress')).toBe(false)
  })

  it('accepts several namespaces at once', () => {
    const scoped = scopeNodesToNamespaces(nodes, ['prod', 'staging'])
    expect(scoped).toHaveLength(5)
  })

  it('returns the same array identity when the scope is empty', () => {
    expect(scopeNodesToNamespaces(nodes, [])).toBe(nodes)
  })
})

describe('isInNamespaceScope', () => {
  it('passes everything for a null scope', () => {
    expect(isInNamespaceScope(node('x', 'Pod', 'any'), null)).toBe(true)
  })

  it('always passes a node with no namespace', () => {
    expect(isInNamespaceScope(node('node-1', 'Node'), new Set(['prod']))).toBe(true)
  })

  it('treats an empty-string namespace as cluster-scoped', () => {
    expect(isInNamespaceScope(node('n', 'Node', ''), new Set(['prod']))).toBe(true)
  })

  it('rejects a namespaced node outside the scope', () => {
    expect(isInNamespaceScope(node('ing', 'Ingress', 'staging'), new Set(['prod']))).toBe(false)
  })
})
