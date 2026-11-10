import { describe, expect, it } from 'vitest'

import { buildCauseByRef, getSubtitle, nodeCause } from './GitOpsTreeGraph'
import type { GitOpsIssue, GitOpsTreeNode } from '../../../types'

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
  it('shows confirmed absence even when controller health is unknown or stale', () => {
    expect(getSubtitle(node({ tool: 'argocd', missing: true, sync: 'OutOfSync', health: 'Healthy' }))).toBe('OutOfSync • Not found in cluster')
  })
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

describe('nodeCause', () => {
  // Shapes as the tree API sends them: the Go side maps Argo's Degraded and
  // Missing to topologyStatus 'unhealthy', Progressing and Suspended to
  // 'degraded' (pkg/gitops/tree healthToTopology).
  const FAILURE = { text: 'Back-off restarting failed container main', lead: false }
  const LEAD = { text: 'Elasticsearch cluster health degraded', lead: true }

  it('shows the cause on a node the controller calls Degraded', () => {
    expect(nodeCause(node({ health: 'Degraded', healthSource: 'controller', topologyStatus: 'unhealthy' }), FAILURE)).toBe(FAILURE.text)
  })

  it('hides a failure verdict on a Progressing node, even when an issue matched it', () => {
    // A rollout in flight is not broken; a cause tooltip on it says it is.
    expect(nodeCause(node({ health: 'Progressing', healthSource: 'controller', topologyStatus: 'degraded' }), FAILURE)).toBeUndefined()
    expect(nodeCause(node({ health: 'Progressing', healthSource: 'radar', topologyStatus: 'degraded' }), FAILURE)).toBeUndefined()
  })

  it('hides it on a Suspended node, even when an issue matched it', () => {
    expect(nodeCause(node({ health: 'Suspended', healthSource: 'controller', topologyStatus: 'degraded' }), FAILURE)).toBeUndefined()
    expect(nodeCause(node({ health: 'Suspended', healthSource: 'controller', topologyStatus: 'degraded' }), LEAD)).toBeUndefined()
  })

  it('never shows it on a healthy node', () => {
    expect(nodeCause(node({ health: 'Healthy', healthSource: 'controller', topologyStatus: 'healthy' }), FAILURE)).toBeUndefined()
    expect(nodeCause(node({ health: 'Healthy', healthSource: 'radar', topologyStatus: 'healthy' }), LEAD)).toBeUndefined()
  })

  it('shows a lead on a node that is not failing, framed as possible', () => {
    // Argo CD 3 does not persist per-resource health, so an Argo-Degraded app
    // can have every node read Progressing or unknown by Radar. The events
    // lead is then the only pointer, and it lands on one of those nodes.
    expect(nodeCause(node({ health: 'Progressing', healthSource: 'radar', topologyStatus: 'degraded' }), LEAD))
      .toBe('Possible cause: Elasticsearch cluster health degraded')
    expect(nodeCause(node({ ref: { group: 'elasticsearch.k8s.elastic.co', kind: 'Elasticsearch', name: 'logs', namespace: 'search' }, topologyStatus: 'unknown' }), LEAD))
      .toBe('Possible cause: Elasticsearch cluster health degraded')
  })

  it('shows a Flux source fetch error on the source while Flux retries it', () => {
    const source = node({
      ref: { group: 'source.toolkit.fluxcd.io', kind: 'GitRepository', name: 'podinfo', namespace: 'flux-system' },
      health: 'Progressing',
      topologyStatus: 'degraded',
    })
    // Keyed like the tree's own nodes, so the issue's ref finds this node.
    const cause = buildCauseByRef([{
      severity: 'warning',
      scope: 'tree',
      reason: 'SourceNotReady',
      message: 'GitRepository flux-system/podinfo can\'t fetch updates.',
      cause: 'authentication required',
      refs: [{ group: 'source.toolkit.fluxcd.io', kind: 'GitRepository', namespace: 'flux-system', name: 'podinfo' }],
    }]).get('source.toolkit.fluxcd.io/GitRepository/flux-system/podinfo')
    expect(nodeCause(source, cause)).toBe('Possible cause: authentication required')
  })

  it('shows a lead as is on a failing node', () => {
    expect(nodeCause(node({ health: 'Degraded', topologyStatus: 'unhealthy' }), LEAD)).toBe(LEAD.text)
  })
})

describe('buildCauseByRef', () => {
  const ref = { group: 'apps', kind: 'Deployment', namespace: 'demo', name: 'web' }
  function issue(extras: Partial<GitOpsIssue>): GitOpsIssue {
    return { severity: 'critical', scope: 'resource', reason: 'Degraded', message: 'Deployment web is Degraded', cause: 'crashloop', refs: [ref], ...extras }
  }

  it('marks the Warning-event lead and the Flux blockers as leads, and nothing else', () => {
    const lead = (reason: string) => [...buildCauseByRef([issue({ reason })]).values()][0]?.lead
    expect(lead('PossibleCause')).toBe(true)
    expect(lead('SourceNotReady')).toBe(true)
    expect(lead('ArtifactFailed')).toBe(true)
    expect(lead('DependencyNotReady')).toBe(true)
    expect(lead('Degraded')).toBe(false)
    expect(lead('Failed')).toBe(false)
  })

  it('keeps the first, most severe cause for a node', () => {
    const map = buildCauseByRef([issue({ cause: 'crashloop' }), issue({ severity: 'warning', reason: 'PossibleCause', cause: 'probe failed' })])
    expect([...map.values()]).toEqual([{ text: 'crashloop', lead: false }])
  })

  it('skips issues without a cause or a ref', () => {
    expect(buildCauseByRef([issue({ cause: undefined }), issue({ refs: [] })]).size).toBe(0)
  })
})
