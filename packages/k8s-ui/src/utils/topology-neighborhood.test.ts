import { describe, it, expect } from 'vitest'
import { neighborhoodFor, tagWorkloadOwnership } from './topology-neighborhood'
import type { Topology, NodeKind, EdgeType } from '../types/core'

function node(id: string, kind: string, ns: string, name: string): Topology['nodes'][number] {
  return { id, kind: kind as NodeKind, name, status: 'healthy' as Topology['nodes'][number]['status'], data: { namespace: ns } }
}
function edge(source: string, target: string, type: EdgeType): Topology['edges'][number] {
  return { id: `${source}->${target}`, source, target, type }
}

describe('neighborhoodFor', () => {
  // Deployment → ReplicaSet → Pod (manages), plus a Service exposing it and a
  // ConfigMap configuring it. All of it is the workload's neighborhood.
  it('includes the ownership chain + attached context', () => {
    const topo: Topology = {
      nodes: [
        node('dep', 'Deployment', 'app', 'web'),
        node('rs', 'ReplicaSet', 'app', 'web-abc'),
        node('pod', 'Pod', 'app', 'web-abc-1'),
        node('svc', 'Service', 'app', 'web'),
        node('cm', 'ConfigMap', 'app', 'web-config'),
      ],
      edges: [
        edge('dep', 'rs', 'manages'),
        edge('rs', 'pod', 'manages'),
        edge('svc', 'dep', 'exposes'),
        edge('cm', 'dep', 'configures'),
      ],
    }
    const out = neighborhoodFor(topo, [{ kind: 'Deployment', namespace: 'app', name: 'web' }])
    expect(new Set(out.nodes.map((n) => n.id))).toEqual(new Set(['dep', 'rs', 'pod', 'svc', 'cm']))
  })

  // The leaf rule: a ConfigMap shared by two unrelated Deployments must NOT
  // bridge the second Deployment into the first's neighborhood.
  it('does not bleed through a shared ConfigMap', () => {
    const topo: Topology = {
      nodes: [
        node('depA', 'Deployment', 'app', 'a'),
        node('depB', 'Deployment', 'app', 'b'),
        node('cm', 'ConfigMap', 'app', 'shared'),
      ],
      edges: [
        edge('cm', 'depA', 'configures'),
        edge('cm', 'depB', 'configures'),
      ],
    }
    const out = neighborhoodFor(topo, [{ kind: 'Deployment', namespace: 'app', name: 'a' }])
    const ids = new Set(out.nodes.map((n) => n.id))
    expect(ids.has('depA')).toBe(true)
    expect(ids.has('cm')).toBe(true) // the shared ConfigMap IS shown (context)
    expect(ids.has('depB')).toBe(false) // …but it doesn't drag in the other app
  })

  // A GitOps manager reached upward is a leaf: "managed by" is shown, but its
  // sibling workloads are not pulled in.
  it('does not expand through a GitOps manager to its siblings', () => {
    const topo: Topology = {
      nodes: [
        node('ks', 'Kustomization', 'flux-system', 'apps'),
        node('depA', 'Deployment', 'app', 'a'),
        node('depB', 'Deployment', 'app', 'b'),
      ],
      edges: [
        edge('ks', 'depA', 'manages'),
        edge('ks', 'depB', 'manages'),
      ],
    }
    const out = neighborhoodFor(topo, [{ kind: 'Deployment', namespace: 'app', name: 'a' }])
    const ids = new Set(out.nodes.map((n) => n.id))
    expect(ids.has('depA')).toBe(true)
    expect(ids.has('ks')).toBe(true) // the managing Kustomization is shown
    expect(ids.has('depB')).toBe(false) // …but not the Kustomization's other app
  })

  // Upward manages is context for ANY manager kind, not just GitOps: a seed
  // Job shows its CronJob ("managed by") without dragging in sibling Jobs.
  it('does not expand upward through a CronJob to sibling Jobs', () => {
    const topo: Topology = {
      nodes: [
        node('cj', 'CronJob', 'app', 'nightly'),
        node('job1', 'Job', 'app', 'nightly-001'),
        node('job2', 'Job', 'app', 'nightly-002'),
        node('pod2', 'Pod', 'app', 'nightly-002-x'),
      ],
      edges: [
        edge('cj', 'job1', 'manages'),
        edge('cj', 'job2', 'manages'),
        edge('job2', 'pod2', 'manages'),
      ],
    }
    const out = neighborhoodFor(topo, [{ kind: 'Job', namespace: 'app', name: 'nightly-001' }])
    const ids = new Set(out.nodes.map((n) => n.id))
    expect(ids.has('job1')).toBe(true)
    expect(ids.has('cj')).toBe(true) // the managing CronJob is shown…
    expect(ids.has('job2')).toBe(false) // …but its sibling Jobs are not
    expect(ids.has('pod2')).toBe(false)
  })

  // The degree guard targets shared infra (routing/context), not ownership: a
  // workload with more than K pods must still keep every pod — the ReplicaSet
  // in between must not be leafed for high manages-fan-out.
  it('keeps all pods of a large workload (degree guard exempts ownership)', () => {
    const pods = Array.from({ length: 10 }, (_, i) => node(`pod${i}`, 'Pod', 'app', `web-${i}`))
    const topo: Topology = {
      nodes: [node('dep', 'Deployment', 'app', 'web'), node('rs', 'ReplicaSet', 'app', 'web-abc'), ...pods],
      edges: [edge('dep', 'rs', 'manages'), ...pods.map((p) => edge('rs', p.id, 'manages'))],
    }
    const out = neighborhoodFor(topo, [{ kind: 'Deployment', namespace: 'app', name: 'web' }])
    const ids = new Set(out.nodes.map((n) => n.id))
    expect(ids.has('rs')).toBe(true)
    for (const p of pods) expect(ids.has(p.id)).toBe(true)
  })

  it('returns an empty graph with a warning when no seed matches', () => {
    const topo: Topology = { nodes: [node('dep', 'Deployment', 'app', 'web')], edges: [] }
    const out = neighborhoodFor(topo, [{ kind: 'Deployment', namespace: 'app', name: 'missing' }])
    expect(out.nodes).toHaveLength(0)
    expect(out.warnings?.some((w) => w.includes('No topology nodes matched'))).toBe(true)
  })
})

describe('tagWorkloadOwnership', () => {
  const dataOf = (t: Topology, id: string) => t.nodes.find((n) => n.id === id)!.data as Record<string, unknown>

  // Two workloads, each with its own Service + Pod, plus one shared ConfigMap.
  // Each workload owns its exclusive satellites; the shared ConfigMap is neutral.
  it('tags exclusive satellites + pods with their workload, shared as neutral', () => {
    const topo: Topology = {
      nodes: [
        node('depA', 'Deployment', 'app', 'a'),
        node('podA', 'Pod', 'app', 'a-1'),
        node('svcA', 'Service', 'app', 'a'),
        node('depB', 'Deployment', 'app', 'b'),
        node('podB', 'Pod', 'app', 'b-1'),
        node('shared', 'ConfigMap', 'app', 'shared'),
      ],
      edges: [
        edge('depA', 'podA', 'manages'),
        edge('svcA', 'depA', 'exposes'),
        edge('depB', 'podB', 'manages'),
        edge('shared', 'depA', 'configures'),
        edge('shared', 'depB', 'configures'),
      ],
    }
    const { topology, colorByWorkload } = tagWorkloadOwnership(topo, [
      { kind: 'Deployment', namespace: 'app', name: 'a' },
      { kind: 'Deployment', namespace: 'app', name: 'b' },
    ])
    const a = colorByWorkload.get('Deployment/app/a')
    const b = colorByWorkload.get('Deployment/app/b')
    expect(a).not.toBe(b)
    // a's core + its exclusive Service carry a's color; its pod inherits it.
    expect(dataOf(topology, 'depA').ownerWorkloadId).toBe('Deployment/app/a')
    expect(dataOf(topology, 'podA').ownerColorIndex).toBe(a)
    expect(dataOf(topology, 'svcA').ownerColorIndex).toBe(a)
    expect(dataOf(topology, 'podB').ownerColorIndex).toBe(b)
    // the ConfigMap touches both workloads → neutral color…
    expect(dataOf(topology, 'shared').ownerWorkloadId).toBeNull()
    expect(dataOf(topology, 'shared').ownerColorIndex).toBeNull()
    // …but its focus set includes BOTH, so focusing either lights it up.
    expect(new Set(dataOf(topology, 'shared').focusWorkloadIds as string[])).toEqual(
      new Set(['Deployment/app/a', 'Deployment/app/b']),
    )
    // an exclusive satellite's focus set is just its own workload.
    expect(dataOf(topology, 'svcA').focusWorkloadIds).toEqual(['Deployment/app/a'])
  })

  // A GitOps manager is context, not membership — it never claims a color even
  // when it manages a single workload in the neighborhood.
  it('leaves a GitOps manager neutral', () => {
    const topo: Topology = {
      nodes: [
        node('ks', 'Kustomization', 'flux-system', 'apps'),
        node('dep', 'Deployment', 'app', 'web'),
        node('pod', 'Pod', 'app', 'web-1'),
      ],
      edges: [edge('ks', 'dep', 'manages'), edge('dep', 'pod', 'manages')],
    }
    const { topology } = tagWorkloadOwnership(topo, [{ kind: 'Deployment', namespace: 'app', name: 'web' }])
    expect(dataOf(topology, 'ks').ownerWorkloadId).toBeNull()
    expect(dataOf(topology, 'pod').ownerWorkloadId).toBe('Deployment/app/web')
  })
})
