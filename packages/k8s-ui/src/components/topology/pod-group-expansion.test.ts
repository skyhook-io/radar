import { describe, expect, it } from 'vitest'
import { expandPodGroup } from './TopologyGraph'
import type { TopologyEdge, TopologyNode } from '../../types'

function group(pods: Record<string, unknown>[]): TopologyNode {
  return { id: 'podgroup/ml/train-workers-0', kind: 'PodGroup', name: 'train-workers-0', status: 'healthy', data: { namespace: 'ml', pods } }
}

const jobEdge: TopologyEdge = { id: 'job-to-group', source: 'job/ml/train-workers-0', target: 'podgroup/ml/train-workers-0', type: 'manages' }
const pod = (name: string, extra: Record<string, unknown> = {}) => ({ name, namespace: 'ml', phase: 'Running', restarts: 0, containers: 1, ...extra })

describe('expandPodGroup', () => {
  it('preserves earlier-run context and each Pod termination state when expanded', () => {
    const node = group([pod('old', { status: 'neutral', terminating: true }), pod('failed', { status: 'unhealthy', terminating: false })])
    node.data.earlierJobRun = 'train-workers-0'
    node.data.terminating = 1
    const edge: TopologyEdge = { ...jobEdge, source: 'jobset/ml/train/jobset.x-k8s.io', type: 'uses', label: 'Earlier Job run (same name)' }
    const out = expandPodGroup([node], [edge], node.id)
    expect(out.nodes.map(n => [n.status, n.data.earlierJobRun, n.data.terminating])).toEqual([
      ['neutral', 'train-workers-0', 1], ['unhealthy', 'train-workers-0', 0],
    ])
    expect(out.edges.every(e => e.type === 'uses' && e.label === edge.label)).toBe(true)
  })
  it('keeps each Pod termination state when an ordinary group is expanded', () => {
    const node = group([pod('draining', { status: 'healthy', terminating: true }), pod('serving', { status: 'healthy' })])
    const out = expandPodGroup([node], [jobEdge], node.id)
    expect(out.nodes.map(n => [n.name, n.data.terminating])).toEqual([['draining', 1], ['serving', 0]])
  })

  it('connects a pod without ownerIds to every owner of the group', () => {
    const out = expandPodGroup([group([pod('a')])], [jobEdge], 'podgroup/ml/train-workers-0')
    expect(out.edges.map((e) => [e.source, e.target])).toEqual([['job/ml/train-workers-0', 'pod/ml/a']])
  })

  it('connects a pod with empty ownerIds to none of the group\'s owners', () => {
    const out = expandPodGroup(
      [group([pod('current', { ownerIds: ['job/ml/train-workers-0'] }), pod('earlier', { ownerIds: [] })])],
      [jobEdge],
      'podgroup/ml/train-workers-0',
    )
    expect(out.nodes.map((n) => n.id).sort()).toEqual(['pod/ml/current', 'pod/ml/earlier'])
    expect(out.edges.map((e) => [e.source, e.target])).toEqual([['job/ml/train-workers-0', 'pod/ml/current']])
  })
})
