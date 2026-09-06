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

  it('does not switch vocabulary when every expression entry is malformed', () => {
    // The shared formatter skips entries it cannot read and has its own
    // empty-case wording; reaching it would describe one state in two ways.
    expect(getNetworkPolicySelector({ spec: { podSelector: { matchExpressions: [null] } } })).toBe('All pods')
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

  it('does not let a stale controller report outvote a live rejection, in either order', () => {
    // Status entries are keyed by parentRef AND controllerName, so one parent
    // can carry two controllers' reports mid-replacement. Reading only the
    // first made the badge depend on their order in the array.
    const old = { parentRef: { name: 'gw' }, controllerName: 'old/ctrl', conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] }
    const live = { parentRef: { name: 'gw' }, controllerName: 'new/ctrl', conditions: [cond('Accepted', 'False'), cond('ResolvedRefs', 'False')] }
    // Neither order may be green, and both must agree. Degraded rather than
    // unhealthy is deliberate: nothing here establishes which controller is
    // authoritative, so the verdict says "something is wrong" without claiming
    // the route is rejected outright.
    const forward = getRouteStatus(route({ parents: [old, live] }))
    const reversed = getRouteStatus(route({ parents: [live, old] }))
    expect(forward).toEqual(reversed)
    expect(forward.level).not.toBe('healthy')
    expect(forward).toMatchObject({ text: 'Degraded', level: 'degraded' })
  })

  it('counts a default-Gateway attachment the spec never names', () => {
    // With useDefaultGateways the attachment exists only in status, so a
    // failure there is the only evidence there is.
    const r = {
      metadata: { namespace: 'prod' },
      spec: { useDefaultGateways: 'All' },
      status: { parents: [{ parentRef: { name: 'default-gw' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'False')] }] },
    }
    expect(getRouteStatus(r)).toMatchObject({ text: 'Degraded', level: 'degraded' })
  })

  it('accepts a default-only route whose default parent is healthy', () => {
    const r = {
      metadata: { namespace: 'prod' },
      spec: { useDefaultGateways: 'All' },
      status: { parents: [{ parentRef: { name: 'default-gw' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] }] },
    }
    expect(getRouteStatus(r)).toMatchObject({ text: 'Accepted', level: 'healthy' })
  })

  it('does not let a healthy explicit parent mask a failing default one', () => {
    const r = {
      metadata: { namespace: 'prod' },
      spec: { parentRefs: [{ name: 'gw' }], useDefaultGateways: 'All' },
      status: {
        parents: [
          { parentRef: { name: 'gw' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] },
          { parentRef: { name: 'default-gw' }, conditions: [cond('Accepted', 'False'), cond('ResolvedRefs', 'True')] },
        ],
      },
    }
    expect(getRouteStatus(r)).toMatchObject({ text: 'Degraded', level: 'degraded' })
  })

  it('ignores a dropped mesh Service parent when weighing default Gateways', () => {
    // Services cannot be default Gateways, so an unmatched Service report is
    // obsolete by definition — and would otherwise outvote the live one
    // indefinitely once its controller is gone.
    const r = {
      metadata: { namespace: 'prod', generation: 3 },
      spec: { useDefaultGateways: 'All' },
      status: {
        parents: [
          { parentRef: { group: '', kind: 'Service', name: 'legacy-mesh' }, conditions: [cond('Accepted', 'True', 2)] },
          { parentRef: { name: 'default-gw' }, conditions: [cond('Accepted', 'True', 3), cond('ResolvedRefs', 'True', 3)] },
        ],
      },
    }
    expect(getRouteStatus(r)).toMatchObject({ text: 'Accepted', level: 'healthy' })
  })

  it('still reports a rejecting default Gateway as rejected despite a dropped Service parent', () => {
    const r = {
      metadata: { namespace: 'prod', generation: 3 },
      spec: { useDefaultGateways: 'All' },
      status: {
        parents: [
          { parentRef: { group: '', kind: 'Service', name: 'legacy-mesh' }, conditions: [cond('Accepted', 'True', 2)] },
          { parentRef: { name: 'default-gw' }, conditions: [cond('Accepted', 'False', 3)] },
        ],
      },
    }
    expect(getRouteStatus(r)).toMatchObject({ text: 'Not Accepted', level: 'unhealthy' })
  })

  it('takes a condition with no observedGeneration at face value', () => {
    // Missing is not stale: a controller that never sets it must not strand the
    // route as Pending forever.
    const r = route({
      generation: 7,
      parents: [{ parentRef: { name: 'gw' }, conditions: [cond('Accepted', 'True'), cond('ResolvedRefs', 'True')] }],
    })
    expect(getRouteStatus(r)).toMatchObject({ text: 'Accepted', level: 'healthy' })
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
