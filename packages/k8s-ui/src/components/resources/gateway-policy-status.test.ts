import { describe, expect, it } from 'vitest'
import { getGatewayPolicyStatus } from './gateway-policy-status'
import { getGenericResourceStatus } from './generic-status'

const cond = (type: string, status: string, reason?: string, message?: string) => ({ type, status, reason, message })
const policy = (...ancestors: any[]) => ({ status: { ancestors } })
const anc = (...conditions: any[]) => ({ ancestorRef: { kind: 'Gateway', name: 'gw' }, conditions })

describe('Gateway API PolicyStatus', () => {
  // Taken from a live EnvoyPatchPolicy. The controller accepted the policy and
  // then could not apply it. Read positives-first — the way the generic ladder
  // resolves conditions — this object is healthy, which is the whole reason it
  // gets its own reader.
  it('reports the failure on a policy that was accepted and then not applied', () => {
    const p = policy(anc(cond('Accepted', 'True', 'Accepted'), cond('Programmed', 'False', 'ResourceNotFound')))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'ResourceNotFound', tone: 'unhealthy' })
  })

  it('reports a plain accepted policy as healthy', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('Accepted', 'True', 'Accepted')))))
      .toMatchObject({ text: 'Accepted', tone: 'healthy' })
  })

  it('reports a rejected policy with the controller reason', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('Accepted', 'False', 'Conflicted')))))
      .toMatchObject({ text: 'Conflicted', tone: 'unhealthy' })
  })

  it('falls back to the condition type when the controller gave no reason', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('Accepted', 'False')))))
      .toMatchObject({ text: 'Not Accepted', tone: 'unhealthy' })
  })

  // Envoy Gateway publishes this combination. A positives-first read returns
  // healthy and the warning never surfaces.
  it('does not let an accepted policy hide a warning', () => {
    const p = policy(anc(cond('Accepted', 'True'), cond('Warning', 'True', 'ShadowedRules')))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'ShadowedRules', tone: 'degraded' })
  })

  // Accepted=False reads as "Not Accepted"; Warning=True means there IS a
  // warning, so the same construction would invert it.
  it('names a reason-less warning after the condition, not its negation', () => {
    const p = policy(anc(cond('Accepted', 'True'), cond('Warning', 'True')))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'Warning', tone: 'degraded' })
  })

  it('carries the controller message as the tooltip reason', () => {
    const p = policy(anc(cond('Accepted', 'False', 'Invalid', 'spec.targetRef.kind is not supported')))
    expect(getGatewayPolicyStatus(p)?.reason).toBe('spec.targetRef.kind is not supported')
  })
})

describe('aggregating across ancestors', () => {
  // A policy may attach to several Gateways with different outcomes. Collapsing
  // to a bare "Accepted" would hide the one that rejected it.
  it('reports a failure anywhere, and how widespread it is', () => {
    const p = policy(
      anc(cond('Accepted', 'True')),
      anc(cond('Accepted', 'False', 'NotAllowed')),
      anc(cond('Accepted', 'True')),
    )
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'NotAllowed (1/3)', tone: 'unhealthy' })
  })

  it('does not add a count when the policy attaches to one ancestor', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('Accepted', 'False', 'NotAllowed'))))?.text)
      .toBe('NotAllowed')
  })

  // Absence is not acceptance: an ancestor the controller has not answered for
  // must not be counted as healthy just because its sibling was.
  it('does not read an ancestor with no verdict as accepted', () => {
    const p = policy(anc(cond('Accepted', 'True')), anc())
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'Pending (1/2)', tone: 'degraded' })
  })

  it('treats Unknown as undecided rather than as failure', () => {
    const p = policy(anc(cond('Accepted', 'Unknown', 'Pending')))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ tone: 'degraded' })
  })

  // One slot for both kinds of problem showed the warning's reason next to the
  // failure's count and tone — the operator reads the warning as the failure.
  it('reports the failure, not an earlier warning, when both are present', () => {
    const p = policy(
      anc(cond('Accepted', 'True'), cond('Warning', 'True', 'ShadowedRules')),
      anc(cond('Accepted', 'False', 'NotAllowed')),
    )
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'NotAllowed (1/2)', tone: 'unhealthy' })
  })

  it('still reports the warning when nothing failed', () => {
    const p = policy(
      anc(cond('Accepted', 'True'), cond('Warning', 'True', 'ShadowedRules')),
      anc(cond('Accepted', 'True')),
    )
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'ShadowedRules (1/2)', tone: 'degraded' })
  })

  it('is healthy only when every ancestor accepted', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('Accepted', 'True')), anc(cond('Accepted', 'True')))))
      .toMatchObject({ text: 'Accepted', tone: 'healthy' })
  })
})

describe('shapes that are not a verdict', () => {
  // Yields nothing rather than a verdict, so a policy that also publishes
  // top-level conditions still gets read from those.
  it('declines to judge a policy that attaches to nothing', () => {
    expect(getGatewayPolicyStatus(policy())).toBeNull()
  })

  it('ignores malformed ancestors rather than throwing', () => {
    expect(() => getGatewayPolicyStatus({ status: { ancestors: [null, 'nope', 42] } })).not.toThrow()
    expect(() => getGatewayPolicyStatus({ status: { ancestors: [{ conditions: 'nope' }] } })).not.toThrow()
  })

  it('yields nothing when ancestors is absent or not an array', () => {
    expect(getGatewayPolicyStatus({ status: { conditions: [] } })).toBeNull()
    expect(getGatewayPolicyStatus({ status: { ancestors: {} } })).toBeNull()
    expect(getGatewayPolicyStatus({ status: {} })).toBeNull()
  })
})

describe('wiring into the generic ladder', () => {
  it('resolves a policy through its own reader', () => {
    const p = policy(anc(cond('Accepted', 'True', 'Accepted'), cond('Programmed', 'False', 'ResourceNotFound')))
    expect(getGenericResourceStatus(p)).toMatchObject({ text: 'ResourceNotFound', tone: 'unhealthy' })
  })

  // The dispatch is on shape, so it must not intercept an ordinary resource.
  it('leaves a resource with top-level conditions alone', () => {
    const r = { status: { conditions: [{ type: 'Ready', status: 'True' }] } }
    expect(getGenericResourceStatus(r)).toMatchObject({ text: 'Ready', tone: 'healthy' })
  })

  it('leaves a phase-based resource alone', () => {
    expect(getGenericResourceStatus({ status: { phase: 'Running' } })).toMatchObject({ text: 'Running', tone: 'healthy' })
  })
})

describe('condition vocabulary beyond Accepted', () => {
  // GKE reports attachment with its own condition; Gateway API's
  // BackendTLSPolicy requires ResolvedRefs as well as Accepted.
  it.each([
    ['Attached', 'InvalidTarget'],
    ['ResolvedRefs', 'InvalidCACertificateRef'],
    ['Programmed', 'ResourceNotFound'],
  ])('treats %s=False as a failure', (type, reason) => {
    const p = policy(anc(cond('Accepted', 'True'), cond(type, 'False', reason)))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: reason, tone: 'unhealthy' })
  })

  // GEP-713 lists Reconciling as a legitimate state on the way to being
  // applied. Painting ordinary convergence red teaches operators to ignore red.
  it.each(['Reconciling', 'Pending'])('treats %s as in-flight, not failed', reason => {
    const p = policy(anc(cond('Accepted', 'True'), cond('Programmed', 'False', reason)))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: reason, tone: 'degraded' })
  })

  // GKE's healthy shape carries no Accepted condition at all. Checking only
  // Accepted read it as pending, which is a policy that works reported amber.
  it('accepts a verdict that is not spelled Accepted', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('Attached', 'True')))))
      .toMatchObject({ text: 'Attached', tone: 'healthy' })
    expect(getGatewayPolicyStatus(policy(anc(cond('Programmed', 'True')))))
      .toMatchObject({ text: 'Programmed', tone: 'healthy' })
  })

  // Refs resolving is a precondition, not a statement that the policy applies.
  it('does not treat ResolvedRefs alone as a verdict', () => {
    expect(getGatewayPolicyStatus(policy(anc(cond('ResolvedRefs', 'True')))))
      .toMatchObject({ tone: 'degraded' })
  })

  it('does not read a partially applied policy as success', () => {
    const p = policy(anc(cond('Accepted', 'True'), cond('Programmed', 'True', 'PartiallyProgrammed')))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'PartiallyProgrammed', tone: 'degraded' })
  })

  // Reporting the first reason with a count claims every ancestor failed that
  // way. When they differ, say how many failed instead of guessing why.
  it('does not attribute one ancestor\'s reason to another', () => {
    const p = policy(
      anc(cond('Accepted', 'False', 'NotAllowed')),
      anc(cond('Accepted', 'False', 'ResourceNotFound')),
      anc(cond('Accepted', 'True')),
    )
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: '2/3 failed', tone: 'unhealthy' })
  })

  it('keeps the reason when every failure agrees', () => {
    const p = policy(anc(cond('Accepted', 'False', 'NotAllowed')), anc(cond('Accepted', 'False', 'NotAllowed')))
    expect(getGatewayPolicyStatus(p)).toMatchObject({ text: 'NotAllowed (2/2)', tone: 'unhealthy' })
  })
})

describe('policies that also publish top-level conditions', () => {
  // GCPBackendPolicy declares both status.ancestors and status.conditions.
  // Dispatching on the shape alone suppressed the conditions entirely.
  it('falls back to top-level conditions when the ancestors say nothing', () => {
    const dual = { status: { ancestors: [], conditions: [{ type: 'Ready', status: 'False', reason: 'Invalid' }] } }
    expect(getGenericResourceStatus(dual)).toMatchObject({ text: 'Invalid', tone: 'unhealthy' })
  })

  it('still prefers a verdict the ancestors did give', () => {
    const dual = {
      status: {
        ancestors: [{ conditions: [cond('Accepted', 'False', 'NotAllowed')] }],
        conditions: [{ type: 'Ready', status: 'True' }],
      },
    }
    expect(getGenericResourceStatus(dual)).toMatchObject({ text: 'NotAllowed', tone: 'unhealthy' })
  })
})

