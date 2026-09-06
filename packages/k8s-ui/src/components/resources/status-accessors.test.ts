import { describe, expect, it } from 'vitest'
import { getNetworkPolicySelector, getRouteStatus } from './resource-utils'
import { getIstioGatewayStatus } from './resource-utils-istio'

// These three accessors reported healthy without checking the thing that makes
// it healthy. Each test names the real cluster state that used to render green.

describe('getNetworkPolicySelector', () => {
  it('reports a matchExpressions-only policy as targeting a subset, not everything', () => {
    // Used to render "All pods": a policy covering a fraction of the namespace
    // claiming to cover all of it, on a security surface.
    const np = {
      spec: { podSelector: { matchExpressions: [{ key: 'tier', operator: 'In', values: ['web', 'api'] }] } },
    }
    expect(getNetworkPolicySelector(np)).toBe('tier In (web,api)')
  })

  it('preserves every value rather than collapsing a multi-value NotIn', () => {
    const np = {
      spec: { podSelector: { matchExpressions: [{ key: 'env', operator: 'NotIn', values: ['prod', 'staging'] }] } },
    }
    expect(getNetworkPolicySelector(np)).toBe('env NotIn (prod,staging)')
  })

  it('renders valueless operators', () => {
    const np = { spec: { podSelector: { matchExpressions: [{ key: 'role', operator: 'Exists' }] } } }
    expect(getNetworkPolicySelector(np)).toBe('role Exists')
  })

  it('combines matchLabels with matchExpressions', () => {
    const np = {
      spec: {
        podSelector: {
          matchLabels: { app: 'api' },
          matchExpressions: [{ key: 'tier', operator: 'In', values: ['web'] }],
        },
      },
    }
    expect(getNetworkPolicySelector(np)).toBe('app=api, tier In (web)')
  })

  it('still says All pods when the selector is genuinely empty', () => {
    expect(getNetworkPolicySelector({ spec: { podSelector: {} } })).toBe('All pods')
    expect(getNetworkPolicySelector({ spec: {} })).toBe('All pods')
  })
})

describe('getRouteStatus', () => {
  const route = (opts: {
    refs?: any[]
    parents?: any[]
    generation?: number
    namespace?: string
  }) => ({
    metadata: { namespace: opts.namespace ?? 'prod', generation: opts.generation },
    spec: { parentRefs: opts.refs ?? [{ name: 'gw' }] },
    status: { parents: opts.parents ?? [] },
  })
  const cond = (type: string, status: string, observedGeneration?: number) =>
    ({ type, status, ...(observedGeneration === undefined ? {} : { observedGeneration }) })

  it('is not healthy when a backendRef names a Service that does not exist', () => {
    // Accepted=True + ResolvedRefs=False is the documented shape for a missing
    // backend — the gateway still routes, and answers 5xx. This used to be green.
    const r = route({
      parents: [{ parentRef: { name: 'gw' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'False')] }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Degraded', level: 'degraded' })
  })

  it('is healthy only when both conditions are true', () => {
    const r = route({
      parents: [{ parentRef: { name: 'gw' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Accepted', level: 'healthy' })
  })

  it('does not call an absent ResolvedRefs healthy', () => {
    const r = route({ parents: [{ parentRef: { name: 'gw' }, conditions: [cond('Accepted', 'True')] }] })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Pending', level: 'degraded' })
  })

  it('reports rejection when every requested parent rejects', () => {
    const r = route({
      parents: [{ parentRef: { name: 'gw' }, conditions: [cond('Accepted', 'False'), cond('ResolvedRefs', 'True')] }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Not Accepted', level: 'unhealthy' })
  })

  it('degrades rather than rejects when only one of several parents rejects', () => {
    const r = route({
      refs: [{ name: 'gw-a' }, { name: 'gw-b' }],
      parents: [
        { parentRef: { name: 'gw-a' }, conditions: [cond('Accepted', 'False'), cond('ResolvedRefs', 'True')] },
        { parentRef: { name: 'gw-b' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] },
      ],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Degraded', level: 'degraded' })
  })

  it('is Pending while a requested parent has not reported yet', () => {
    const r = route({
      refs: [{ name: 'gw-a' }, { name: 'gw-b' }],
      parents: [
        { parentRef: { name: 'gw-a' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] },
      ],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Pending', level: 'degraded' })
  })

  it('ignores a report left behind for a parentRef the spec no longer names', () => {
    const r = route({
      refs: [{ name: 'gw-new' }],
      parents: [{ parentRef: { name: 'gw-old' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Unknown', level: 'unknown' })
  })

  it('does not accept conditions observed against a superseded generation', () => {
    const r = route({
      generation: 3,
      parents: [{
        parentRef: { name: 'gw' },
        conditions: [cond('Accepted', 'True', 2), cond('ResolvedRefs', 'True', 2)],
      }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Pending', level: 'degraded' })
  })

  it('matches a parent whose namespace and kind are defaulted on one side only', () => {
    const r = route({
      namespace: 'prod',
      refs: [{ name: 'gw' }],
      parents: [{
        parentRef: { name: 'gw', kind: 'Gateway', namespace: 'prod', group: 'gateway.networking.k8s.io' },
        conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')],
      }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Accepted', level: 'healthy' })
  })

  it('does not match a report for a different sectionName', () => {
    const r = route({
      refs: [{ name: 'gw', sectionName: 'https' }],
      parents: [{ parentRef: { name: 'gw', sectionName: 'http' }, conditions: [cond('Accepted', 'True')] }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Unknown', level: 'unknown' })
  })
})

describe('getIstioGatewayStatus', () => {
  it('does not claim health it cannot observe from spec alone', () => {
    // A Gateway whose selector matches no running ingressgateway pod has
    // servers like any other; green asserted liveness nothing had checked.
    const gw = { spec: { servers: [{ port: { number: 80 } }] } }
    expect(getIstioGatewayStatus(gw)).toMatchObject({ text: 'Defined', level: 'neutral' })
  })

  it('does not treat TLS as a verdict', () => {
    const gw = { spec: { servers: [{ tls: { mode: 'SIMPLE' } }] } }
    expect(getIstioGatewayStatus(gw)).toMatchObject({ text: 'Defined', level: 'neutral' })
  })

  it('still flags a gateway with no servers', () => {
    expect(getIstioGatewayStatus({ spec: { servers: [] } })).toMatchObject({
      text: 'No Servers', level: 'unhealthy',
    })
  })
})
