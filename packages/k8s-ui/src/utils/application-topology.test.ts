import { describe, expect, it } from 'vitest'
import type { GitOpsResourceTree, Topology } from '../types'
import { layerDeploymentInventory } from './application-topology'

const topology: Topology = {
  nodes: [
    { id: 'deployment/default/api', kind: 'Deployment', name: 'api', status: 'healthy', data: { namespace: 'default', apiVersion: 'apps/v1' } },
    { id: 'service/default/external', kind: 'Service', name: 'external', status: 'healthy', data: { namespace: 'default', apiVersion: 'v1' } },
  ],
  edges: [],
}

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
    const result = layerDeploymentInventory(topology, inventory(), 'Argo CD')

    expect(result.managedRuntimeCount).toBe(1)
    expect(result.runtimeOnlyCount).toBe(1)
    expect(result.managedOnly.map((node) => node.ref.name)).toEqual(['api-config'])
    expect(result.topology.nodes[0].data?.deploymentMembership).toBeUndefined()
    expect(result.topology.nodes[1].data?.deploymentMembership).toBe('runtime-only')
  })

  it('does not claim runtime-only membership for a partial inventory', () => {
    const result = layerDeploymentInventory(topology, inventory(['Some resources are hidden']), 'Argo CD')

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

    const result = layerDeploymentInventory(matchingTopology, matchingInventory, 'Argo CD')

    expect(result.runtimeOnlyCount).toBe(0)
    expect(result.managedOnly).toEqual([])
    expect(result.topology.nodes[0].data?.deploymentMembership).toBeUndefined()
  })

  it('does not merge same-named resources from different CRD groups', () => {
    const collisionTopology: Topology = {
      nodes: [{ id: 'service/default/api', kind: 'Service', name: 'api', status: 'healthy', data: { namespace: 'default', apiVersion: 'serving.knative.dev/v1' } }],
      edges: [],
    }
    const result = layerDeploymentInventory(collisionTopology, {
      root: inventory().root,
      edges: [],
      nodes: [
        inventory().root,
        { id: 'core-service', ref: { kind: 'Service', namespace: 'default', name: 'api' }, role: 'declared', tool: 'argocd' },
      ],
    }, 'Argo CD')

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
    const result = layerDeploymentInventory(syntheticTopology, inventory(), 'Argo CD')

    expect(result.runtimeOnlyCount).toBe(0)
    expect(result.topology.nodes.every((node) => node.data?.deploymentMembership === undefined)).toBe(true)
  })
})
