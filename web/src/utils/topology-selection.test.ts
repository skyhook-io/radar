import { describe, expect, it } from 'vitest'
import type { SelectedResource, Topology, TopologyNode } from '@skyhook-io/k8s-ui/types/core'
import { findSelectedTopologyNode } from './topology-selection'

function node(id: string, kind: string, name: string, apiVersion?: string): TopologyNode {
  return {
    id,
    kind,
    name,
    status: 'healthy',
    data: { namespace: 'default', ...(apiVersion ? { apiVersion } : {}) },
  } as TopologyNode
}

function selected(group?: string, kind = 'networkpolicies'): SelectedResource {
  return { kind, namespace: 'default', name: 'policy', group }
}

describe('findSelectedTopologyNode', () => {
  it('prefers the node from the selected API group when names collide', () => {
    const native = node('native', 'NetworkPolicy', 'policy')
    const calico = node('calico', 'CalicoNetworkPolicy', 'policy', 'projectcalico.org/v3')

    expect(findSelectedTopologyNode({ nodes: [native, calico], edges: [] }, selected('projectcalico.org'))?.id).toBe('calico')
  })

  it('keeps native NetworkPolicy selection working without an apiVersion on the node', () => {
    const native = node('native', 'NetworkPolicy', 'policy')

    expect(findSelectedTopologyNode({ nodes: [native], edges: [] }, selected('networking.k8s.io'))?.id).toBe('native')
  })

  it('matches arbitrary same-name nodes by API group when both groups are present', () => {
    const first = node('first', 'Widget', 'policy', 'one.example/v1')
    const second = node('second', 'Widget', 'policy', 'two.example/v1')
    const topology: Topology = { nodes: [first, second], edges: [] }

    expect(findSelectedTopologyNode(topology, selected('two.example', 'widgets'))?.id).toBe('second')
  })
})
