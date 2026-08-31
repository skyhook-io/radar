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

  it('matches a pseudo topology kind through its real Kubernetes kind', () => {
    const knative = node('knative', 'KnativeService', 'api', 'serving.knative.dev/v1')
    knative.data.resourceKind = 'Service'

    expect(findSelectedTopologyNode(
      { nodes: [knative], edges: [] },
      { kind: 'services', namespace: 'default', name: 'api', group: 'serving.knative.dev' },
    )?.id).toBe('knative')
  })

  it('keeps an omitted or explicit core Service selection on the core node', () => {
    const core = node('core', 'Service', 'api')
    const knative = node('knative', 'KnativeService', 'api', 'serving.knative.dev/v1')
    knative.data.resourceKind = 'Service'
    const topology: Topology = { nodes: [knative, core], edges: [] }

    expect(findSelectedTopologyNode(topology, {
      kind: 'services', namespace: 'default', name: 'api',
    })?.id).toBe('core')
    expect(findSelectedTopologyNode(topology, {
      kind: 'services', namespace: 'default', name: 'api', group: '',
    })?.id).toBe('core')
  })

  it('infers a typed Job group when its topology node omits apiVersion', () => {
    const core = node('core', 'Job', 'train')
    const volcano = node('volcano', 'Job', 'train', 'batch.volcano.sh/v1alpha1')
    const topology: Topology = { nodes: [volcano, core], edges: [] }

    expect(findSelectedTopologyNode(topology, {
      kind: 'jobs', namespace: 'default', name: 'train', group: 'batch',
    })?.id).toBe('core')
    expect(findSelectedTopologyNode(topology, {
      kind: 'jobs', namespace: 'default', name: 'train', group: 'batch.volcano.sh',
    })?.id).toBe('volcano')
  })

  it('infers the policy group for a typed PodDisruptionBudget node', () => {
    const pdb = node('pdb', 'PodDisruptionBudget', 'budget')

    expect(findSelectedTopologyNode(
      { nodes: [pdb], edges: [] },
      { kind: 'poddisruptionbudgets', namespace: 'default', name: 'budget', group: 'policy' },
    )?.id).toBe('pdb')
  })

  it('fails closed for an omitted unknown group when custom resources collide', () => {
    const one = node('one', 'Widget', 'same', 'one.example/v1')
    const two = node('two', 'Widget', 'same', 'two.example/v1')

    expect(findSelectedTopologyNode(
      { nodes: [one, two], edges: [] },
      { kind: 'widgets', namespace: 'default', name: 'same' },
    )).toBeUndefined()
  })
})
