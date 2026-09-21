import { describe, expect, it } from 'vitest'

import { getSubtitle } from './GitOpsTreeGraph'
import type { GitOpsTreeNode } from '../../../types'

function node(extras: Partial<GitOpsTreeNode> = {}): GitOpsTreeNode {
  return {
    id: 'node-1',
    ref: { kind: 'Service', name: 'podinfo', namespace: 'demo-flux' },
    role: 'declared',
    tool: 'fluxcd',
    ...extras,
  }
}

describe('getSubtitle', () => {
  it('shows what a healthy resource exposes, not that it is healthy', () => {
    // The status dot and the left border stripe already paint a healthy node
    // green, so the word adds nothing and the ports are the only thing here the
    // rest of the card can't say.
    expect(getSubtitle(node({
      sync: 'Synced',
      health: 'Healthy',
      info: [{ name: 'Service', value: 'ClusterIP :9898 +1 more' }],
    }))).toBe('Synced • ClusterIP :9898 +1 more')

    expect(getSubtitle(node({
      ref: { kind: 'Pod', name: 'podinfo-6b4f8c9d7-xk2mv', namespace: 'demo-flux' },
      role: 'generated',
      health: 'Healthy',
      info: [{ name: 'Phase', value: 'Running' }],
    }))).toBe('Running')

    expect(getSubtitle(node({
      ref: { kind: 'Ingress', name: 'podinfo', namespace: 'demo-flux' },
      sync: 'Synced',
      health: 'Healthy',
      info: [{ name: 'Host', value: 'podinfo.example.com' }],
    }))).toBe('Synced • podinfo.example.com')
  })

  it('keeps every health value the stripe colour cannot identify on its own', () => {
    // Progressing and Suspended are both yellow, Degraded and Missing both red.
    // Drop the word and the two become indistinguishable.
    expect(getSubtitle(node({
      sync: 'Synced',
      health: 'Progressing',
      info: [{ name: 'Service', value: 'LoadBalancer :80' }],
    }))).toBe('Synced • Progressing • LoadBalancer :80')

    expect(getSubtitle(node({
      ref: { kind: 'Deployment', name: 'podinfo', namespace: 'demo-flux' },
      sync: 'OutOfSync',
      health: 'Degraded',
      info: [{ name: 'Ready', value: '2/3' }],
    }))).toBe('OutOfSync • Degraded • 2/3')

    expect(getSubtitle(node({
      health: 'Unknown',
      info: [{ name: 'Service', value: 'ClusterIP :80' }],
    }))).toBe('Unknown • ClusterIP :80')
  })

  it('still reads as Healthy when there is nothing to say instead', () => {
    // Kinds infoFromTopology doesn't cover, and remote Argo destinations, reach
    // the tree with no info line at all. Those must not lose their status line.
    expect(getSubtitle(node({ sync: 'Synced', health: 'Healthy' }))).toBe('Synced • Healthy')
    expect(getSubtitle(node({ health: 'Healthy' }))).toBe('Healthy')
    expect(getSubtitle(node({ sync: 'OutOfSync', health: 'Missing' }))).toBe('OutOfSync • Missing')
  })

  it('lets lifecycle and grouping own the line outright', () => {
    expect(getSubtitle(node({
      role: 'group',
      sync: 'Synced',
      health: 'Healthy',
      info: [{ name: 'Phase', value: 'Running' }],
    }))).toBe('Click to expand')

    expect(getSubtitle(node({
      sync: 'Synced',
      health: 'Healthy',
      info: [{ name: 'Service', value: 'ClusterIP :9898 +1 more' }],
      data: { deletionTimestamp: '2026-09-11T13:09:43Z' },
    }))).toBe('Pending deletion')
  })

  it('falls back to the namespace only when the node states nothing else', () => {
    expect(getSubtitle(node())).toBe('demo-flux')
    expect(getSubtitle(node({ ref: { kind: 'ClusterRole', name: 'podinfo', namespace: '' } }))).toBe('')
  })
})
