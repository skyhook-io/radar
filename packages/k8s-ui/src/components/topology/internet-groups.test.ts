import { describe, expect, it } from 'vitest'
import type { TopologyEdge, TopologyNode } from '../../types'
import { splitInternetByGroup } from './internet-groups'

const node = (id: string, kind: string, namespace?: string): TopologyNode =>
  ({ id, kind, name: id, status: 'healthy', data: namespace ? { namespace } : {} }) as TopologyNode
const fromInternet = (target: string): TopologyEdge => ({ id: `internet-to-${target}`, source: 'internet', target, type: 'routes-to' })

describe('splitInternetByGroup', () => {
  it('keeps every entrypoint connected, not only Ingresses and Gateways', () => {
    const nodes = [
      node('internet', 'Internet'),
      node('ingress/shop/web', 'Ingress', 'shop'),
      node('knativeservice/shop/api', 'KnativeService', 'shop'),
      node('ingressroute/edge/dash', 'IngressRoute', 'edge'),
    ]
    const edges = nodes.slice(1).map(n => fromInternet(n.id))
    const out = splitInternetByGroup(nodes, edges, 'namespace')

    const reached = out.edges.filter(e => out.nodes.find(n => n.id === e.source)?.kind === 'Internet').map(e => `${e.source} -> ${e.target}`)
    expect(reached.sort()).toEqual([
      'internet-namespace-edge -> ingressroute/edge/dash',
      'internet-namespace-shop -> ingress/shop/web',
      'internet-namespace-shop -> knativeservice/shop/api',
    ])
    expect(out.nodes.some(n => n.id === 'internet')).toBe(false)
  })

  it('leaves an ungrouped entrypoint on the original Internet node', () => {
    const nodes = [node('internet', 'Internet'), node('ingress/shop/web', 'Ingress', 'shop'), node('gateway//shared', 'Gateway')]
    const out = splitInternetByGroup(nodes, nodes.slice(1).map(n => fromInternet(n.id)), 'namespace')
    expect(out.edges.some(e => e.source === 'internet' && e.target === 'gateway//shared')).toBe(true)
    expect(out.nodes.some(n => n.id === 'internet')).toBe(true)
  })
})
