import { describe, expect, it } from 'vitest'
import type { GitOpsResourceTree, Topology } from '../types'
import { collapseStableReplicaSets, deploymentInventoryFromGitOps, deploymentInventoryFromHelm, layerDeploymentInventory, topologyGroup } from './application-topology'

const topology: Topology = {
  nodes: [
    { id: 'deployment/default/api', kind: 'Deployment', name: 'api', status: 'healthy', data: { namespace: 'default', apiVersion: 'apps/v1' } },
    { id: 'service/default/external', kind: 'Service', name: 'external', status: 'healthy', data: { namespace: 'default', apiVersion: 'v1' } },
  ],
  edges: [],
}

it('resolves the stored API group for source-only CRD nodes', () => {
  expect(topologyGroup({
    id: 'source/prometheusrule',
    kind: 'PrometheusRule',
    name: 'alerts',
    status: 'healthy',
    data: { namespace: 'monitoring', sourceGroup: 'monitoring.coreos.com' },
  })).toBe('monitoring.coreos.com')
})

function inventory(warnings?: string[]): GitOpsResourceTree {
  const root = { id: 'root', ref: { group: 'argoproj.io', kind: 'Application', namespace: 'argocd', name: 'api' }, role: 'root' as const, tool: 'argocd' as const }
  return {
    root,
    warnings,
    edges: [],
    nodes: [
      root,
      { id: 'deployment', ref: { group: 'apps', kind: 'Deployment', namespace: 'default', name: 'api' }, role: 'declared', tool: 'argocd' },
      { id: 'config', ref: { kind: 'ConfigMap', namespace: 'default', name: 'api-config' }, role: 'declared', tool: 'argocd' },
    ],
  }
}

describe('layerDeploymentInventory', () => {
  it('separates overlapping, runtime-only, and managed-only resources', () => {
    const result = layerDeploymentInventory(topology, deploymentInventoryFromGitOps(inventory())!, 'Argo CD')

    expect(result.managedRuntimeCount).toBe(1)
    expect(result.runtimeOnlyCount).toBe(1)
    expect(result.managedOnly.map((node) => node.name)).toEqual(['api-config'])
    expect(result.topology.nodes[0].data?.deploymentMembership).toBeUndefined()
    expect(result.topology.nodes[1].data?.deploymentMembership).toBe('runtime-only')
    expect(result.topology.nodes[2]).toMatchObject({ kind: 'ConfigMap', name: 'api-config', data: { deploymentMembership: 'source-only' } })
  })

  it('does not claim runtime-only membership for a partial inventory', () => {
    const result = layerDeploymentInventory(topology, deploymentInventoryFromGitOps(inventory(['Some resources are hidden']))!, 'Argo CD')

    expect(result.inventoryComplete).toBe(false)
    expect(result.runtimeOnlyCount).toBe(0)
    expect(result.topology.nodes[1].data?.deploymentMembership).toBeUndefined()
  })

  it('does not claim runtime-only membership for Helm without a completeness signal', () => {
    const helmInventory = deploymentInventoryFromHelm([{
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      namespace: 'default',
      name: 'api',
      status: 'ready',
    }])!
    const result = layerDeploymentInventory(topology, helmInventory, 'Helm')

    expect(result.inventoryComplete).toBe(false)
    expect(result.runtimeOnlyCount).toBe(0)
    expect(result.topology.nodes[1].data?.deploymentMembership).toBeUndefined()
  })

  it('adds no membership markers when source and runtime agree', () => {
    const matchingTopology: Topology = {
      nodes: [topology.nodes[0]],
      edges: [],
    }
    const matchingInventory = inventory()
    matchingInventory.nodes = matchingInventory.nodes.filter((node) => node.id !== 'config')

    const result = layerDeploymentInventory(matchingTopology, deploymentInventoryFromGitOps(matchingInventory)!, 'Argo CD')

    expect(result.runtimeOnlyCount).toBe(0)
    expect(result.managedOnly).toEqual([])
    expect(result.topology.nodes[0].data?.deploymentMembership).toBeUndefined()
  })

  it('does not merge same-named resources from different CRD groups', () => {
    const collisionTopology: Topology = {
      nodes: [{ id: 'service/default/api', kind: 'Service', name: 'api', status: 'healthy', data: { namespace: 'default', apiVersion: 'serving.knative.dev/v1' } }],
      edges: [],
    }
    const result = layerDeploymentInventory(collisionTopology, deploymentInventoryFromGitOps({
      root: inventory().root,
      edges: [],
      nodes: [
        inventory().root,
        { id: 'core-service', ref: { kind: 'Service', namespace: 'default', name: 'api' }, role: 'declared', tool: 'argocd' },
      ],
    })!, 'Argo CD')

    expect(result.managedRuntimeCount).toBe(0)
    expect(result.runtimeOnlyCount).toBe(1)
    expect(result.managedOnly.map((node) => node.id)).toEqual(['core-service'])
  })

  it('does not classify synthetic graph nodes as runtime-only resources', () => {
    const syntheticTopology: Topology = {
      nodes: [
        { id: 'podgroup/default/api', kind: 'PodGroup', name: 'api', status: 'healthy', data: { namespace: 'default' } },
        { id: 'internet', kind: 'Internet', name: 'Internet', status: 'healthy', data: {} },
      ],
      edges: [],
    }
    const result = layerDeploymentInventory(syntheticTopology, deploymentInventoryFromGitOps(inventory())!, 'Argo CD')

    expect(result.runtimeOnlyCount).toBe(0)
    expect(result.topology.nodes.slice(0, 2).every((node) => node.data?.deploymentMembership === undefined)).toBe(true)
  })

  it('groups repeated source-only resources so large bundles remain readable', () => {
    const largeInventory = inventory()
    largeInventory.nodes = [
      largeInventory.root,
      ...Array.from({ length: 5 }, (_, index) => ({
        id: `rule-${index}`,
        ref: { group: 'monitoring.coreos.com', kind: 'PrometheusRule', namespace: 'monitoring', name: `rule-${index}` },
        role: 'declared' as const,
        tool: 'argocd' as const,
      })),
    ]

    const result = layerDeploymentInventory({ nodes: [], edges: [] }, deploymentInventoryFromGitOps(largeInventory)!, 'Argo CD')

    expect(result.managedOnly).toHaveLength(5)
    expect(result.topology.nodes).toHaveLength(1)
    expect(result.topology.nodes[0]).toMatchObject({ kind: 'PrometheusRule', data: { sourceInventoryGroup: true, sourceInventoryCount: 5 } })
  })

  it('summarizes a large source-only inventory outside the graph', () => {
    const largeInventory = inventory()
    largeInventory.nodes = [
      largeInventory.root,
      ...Array.from({ length: 10 }, (_, index) => ({
        id: `rule-${index}`,
        ref: { group: 'monitoring.coreos.com', kind: 'PrometheusRule', namespace: 'monitoring', name: `rule-${index}` },
        role: 'declared' as const,
        tool: 'argocd' as const,
      })),
      ...Array.from({ length: 5 }, (_, index) => ({
        id: `config-${index}`,
        ref: { kind: 'ConfigMap', namespace: 'monitoring', name: `config-${index}` },
        role: 'declared' as const,
        tool: 'argocd' as const,
      })),
    ]

    const result = layerDeploymentInventory({ nodes: [], edges: [] }, deploymentInventoryFromGitOps(largeInventory)!, 'Argo CD')

    expect(result.managedOnly).toHaveLength(15)
    expect(result.topology.nodes).toHaveLength(0)
    expect(result.managedOnlySummary).toBe('10 PrometheusRules · 5 ConfigMaps')
  })
})

describe('collapseStableReplicaSets', () => {
  const replicaSetTopology: Topology = {
    nodes: [
      { id: 'deployment/default/api', kind: 'Deployment', name: 'api', status: 'healthy', data: { namespace: 'default' } },
      { id: 'replicaset/default/api-abc', kind: 'ReplicaSet', name: 'api-abc', status: 'healthy', data: { namespace: 'default', readyReplicas: 2, totalReplicas: 2 } },
      { id: 'pod/default/api-1', kind: 'Pod', name: 'api-1', status: 'healthy', data: { namespace: 'default' } },
    ],
    edges: [
      { id: 'deployment-rs', source: 'deployment/default/api', target: 'replicaset/default/api-abc', type: 'manages' },
      { id: 'rs-pod', source: 'replicaset/default/api-abc', target: 'pod/default/api-1', type: 'manages' },
    ],
  }

  it('collapses a stable single ReplicaSet and rewires its pods', () => {
    const result = collapseStableReplicaSets(replicaSetTopology, new Set())
    expect(result.nodes.map((node) => node.kind)).toEqual(['Deployment', 'Pod'])
    expect(result.edges).toContainEqual(expect.objectContaining({ source: 'deployment/default/api', target: 'pod/default/api-1' }))
    expect(result.nodes[0].data).toMatchObject({ replicaSetCount: 1, replicaSetsCollapsed: true })
  })

  it('shows the ReplicaSet when the developer expands it', () => {
    const result = collapseStableReplicaSets(replicaSetTopology, new Set(['deployment/default/api']))
    expect(result.nodes.map((node) => node.kind)).toEqual(['Deployment', 'ReplicaSet', 'Pod'])
    expect(result.nodes[0].data).toMatchObject({ replicaSetCount: 1, replicaSetsCollapsed: false })
  })

  it('keeps an unhealthy ReplicaSet visible automatically', () => {
    const unhealthy = {
      ...replicaSetTopology,
      nodes: replicaSetTopology.nodes.map((node) => node.kind === 'ReplicaSet' ? { ...node, status: 'degraded' as const } : node),
    }
    expect(collapseStableReplicaSets(unhealthy, new Set()).nodes.map((node) => node.kind)).toContain('ReplicaSet')
  })
})
