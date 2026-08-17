import { describe, expect, it } from 'vitest'
import type { Topology, TopologyNode } from '@skyhook-io/k8s-ui/types/core'
import { getNetworkPolicyResourceTarget } from './navigation'

function node(kind: string, apiVersion?: string): TopologyNode {
  return {
    id: `${kind}/default/policy`,
    kind,
    name: 'policy',
    status: 'healthy',
    data: { namespace: 'default', ...(apiVersion ? { apiVersion } : {}) },
  } as TopologyNode
}

function topology(...nodes: TopologyNode[]): Topology {
  return { nodes, edges: [] }
}

describe('getNetworkPolicyResourceTarget', () => {
  it('routes a Calico policy to its API group instead of the native policy list', () => {
    expect(getNetworkPolicyResourceTarget(topology(node('CalicoNetworkPolicy', 'projectcalico.org/v3')))).toEqual({
      kind: 'networkpolicies',
      group: 'projectcalico.org',
    })
  })

  it('uses the Calico policy kind when the aggregate only contains global policies', () => {
    expect(getNetworkPolicyResourceTarget(topology(node('CalicoGlobalNetworkPolicy', 'crd.projectcalico.org/v1')))).toEqual({
      kind: 'globalnetworkpolicies',
      group: 'crd.projectcalico.org',
    })
  })

  it('routes staged Kubernetes Calico policies to their exact API group', () => {
    expect(getNetworkPolicyResourceTarget(topology(node('CalicoStagedKubernetesNetworkPolicy', 'crd.projectcalico.org/v1')))).toEqual({
      kind: 'stagedkubernetesnetworkpolicies',
      group: 'crd.projectcalico.org',
    })
  })

  it('uses the generic resources view when the aggregate contains multiple policy targets', () => {
    expect(getNetworkPolicyResourceTarget(topology(
      node('NetworkPolicy', 'networking.k8s.io/v1'),
      node('CalicoNetworkPolicy', 'crd.projectcalico.org/v1'),
    ))).toBeUndefined()
  })

  it('keeps a stable route when multiple policies share one kind and group', () => {
    expect(getNetworkPolicyResourceTarget(topology(
      node('CalicoNetworkPolicy', 'crd.projectcalico.org/v1'),
      node('CalicoNetworkPolicy', 'crd.projectcalico.org/v1'),
    ))).toEqual({
      kind: 'networkpolicies',
      group: 'crd.projectcalico.org',
    })
  })

  it('preserves the native NetworkPolicy API group when apiVersion is absent', () => {
    expect(getNetworkPolicyResourceTarget(topology(node('NetworkPolicy')))).toEqual({
      kind: 'networkpolicies',
      group: 'networking.k8s.io',
    })
  })

  it('returns no targeted route when topology has not identified a policy', () => {
    expect(getNetworkPolicyResourceTarget(topology(node('Deployment')))).toBeUndefined()
  })
})
