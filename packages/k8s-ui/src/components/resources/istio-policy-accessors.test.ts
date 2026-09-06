import { describe, expect, it } from 'vitest'
import { AUTHORIZATION_POLICY_ACTION_SEVERITY } from './renderers/istio-cells'
import {
  getAuthorizationPolicySelectorString,
  getAuthorizationPolicyStatus,
  getDestinationRuleStatus,
  getAuthorizationPolicyRuleNotice,
  getDestinationRuleTlsMode,
} from './resource-utils-istio'

// A request decision is made across every policy selecting a workload, so no
// single object can establish it. These pin the line between what one policy
// declares and what the mesh actually does.

describe('getAuthorizationPolicyStatus', () => {
  it('marks the deny-all idiom rather than presenting it as permitted', () => {
    // `spec: {}` is Istio's documented deny-all: action defaults to ALLOW, and
    // rules are alternatives, so zero of them match nothing.
    expect(getAuthorizationPolicyStatus({ spec: {} })).toMatchObject({
      text: 'No allow rules', level: 'degraded',
    })
  })

  it('treats an explicit rule-less ALLOW the same as the defaulted one', () => {
    expect(getAuthorizationPolicyStatus({ spec: { action: 'ALLOW', rules: [] } })).toMatchObject({
      text: 'No allow rules', level: 'degraded',
    })
  })

  it('does not call a DENY doing its job unhealthy', () => {
    expect(getAuthorizationPolicyStatus({ spec: { action: 'DENY', rules: [{}] } })).toMatchObject({
      text: 'Deny (1 rule)', level: 'neutral',
    })
  })

  it('does not present an ALLOW with rules as proof anything is permitted', () => {
    expect(getAuthorizationPolicyStatus({ spec: { action: 'ALLOW', rules: [{}, {}] } })).toMatchObject({
      text: 'Allow (2 rules)', level: 'neutral',
    })
  })

  it('keeps AUDIT and CUSTOM neutral — neither blocks nor permits on its own', () => {
    expect(getAuthorizationPolicyStatus({ spec: { action: 'AUDIT', rules: [{}] } }).level).toBe('neutral')
    expect(getAuthorizationPolicyStatus({ spec: { action: 'CUSTOM', rules: [{}] } }).level).toBe('neutral')
  })

  it('does not claim to recognise an unknown action', () => {
    expect(getAuthorizationPolicyStatus({ spec: { action: 'FUTURE' } })).toMatchObject({
      text: 'FUTURE', level: 'unknown',
    })
  })
})

describe('getAuthorizationPolicySelectorString', () => {
  it('reports a targetRef attachment instead of claiming namespace scope', () => {
    // Waypoint and Gateway attachment in ambient mode uses targetRefs, not
    // labels; reading only the selector called these namespace-wide.
    const p = {
      metadata: { namespace: 'prod' },
      spec: { targetRefs: [{ kind: 'Gateway', name: 'waypoint' }] },
    }
    expect(getAuthorizationPolicySelectorString(p)).toBe('Gateway/waypoint')
  })

  it('qualifies a target naming another namespace', () => {
    // Rendering only — supported attachments are same-namespace, so this
    // records what the cell shows, not that the attachment would be enforced.
    const p = {
      metadata: { namespace: 'prod' },
      spec: { targetRef: { kind: 'Gateway', name: 'shared', namespace: 'infra' } },
    }
    expect(getAuthorizationPolicySelectorString(p)).toBe('infra/Gateway/shared')
  })

  it('lists every target', () => {
    const p = {
      metadata: { namespace: 'prod' },
      spec: { targetRefs: [{ kind: 'Gateway', name: 'a' }, { kind: 'Service', name: 'b' }] },
    }
    expect(getAuthorizationPolicySelectorString(p)).toBe('Gateway/a, Service/b')
  })

  it('falls back to selector labels', () => {
    const p = { metadata: { namespace: 'prod' }, spec: { selector: { matchLabels: { app: 'api' } } } }
    expect(getAuthorizationPolicySelectorString(p)).toBe('app=api')
  })

  it('names both possible scopes when neither is declared', () => {
    // Which one applies depends on MeshConfig.rootNamespace, which this object
    // cannot see — and the root namespace is configurable, so the conventional
    // name is not evidence.
    expect(getAuthorizationPolicySelectorString({ metadata: { namespace: 'istio-system' }, spec: {} }))
      .toBe('Namespace / mesh scope')
  })
})

describe('getDestinationRuleStatus', () => {
  it('flags a rule that applies to nothing', () => {
    expect(getDestinationRuleStatus({ spec: {} })).toMatchObject({ text: 'No Host', level: 'unhealthy' })
  })

  it('does not restate the subset count as a health verdict', () => {
    expect(getDestinationRuleStatus({ spec: { host: 'api', subsets: [{ name: 'v1' }, { name: 'v2' }] } }))
      .toMatchObject({ text: 'Not assessed', level: 'unknown' })
  })

  it('does not read a declared traffic policy as working', () => {
    expect(getDestinationRuleStatus({ spec: { host: 'api', trafficPolicy: { tls: { mode: 'ISTIO_MUTUAL' } } } }))
      .toMatchObject({ text: 'Not assessed', level: 'unknown' })
  })
})

describe('getDestinationRuleTlsMode', () => {
  it('surfaces the mode that can defeat a STRICT PeerAuthentication', () => {
    expect(getDestinationRuleTlsMode({ spec: { host: 'api', trafficPolicy: { tls: { mode: 'DISABLE' } } } }))
      .toBe('DISABLE')
  })

  it('shows the declared mode when one is set', () => {
    expect(getDestinationRuleTlsMode({ spec: { trafficPolicy: { tls: { mode: 'ISTIO_MUTUAL' } } } }))
      .toBe('ISTIO_MUTUAL')
  })

  it('reports nothing declared rather than inventing a default', () => {
    expect(getDestinationRuleTlsMode({ spec: { host: 'api' } })).toBe('-')
    expect(getDestinationRuleTlsMode({ spec: { host: 'api', trafficPolicy: {} } })).toBe('-')
  })
})

describe('AuthorizationPolicy action badge', () => {
  it('gives every action the same tone', () => {
    // The action is a declaration, not a verdict. Colouring ALLOW green made a
    // deny-all policy the greenest row on the page; colouring DENY red made a
    // control doing its job look like a failure. This is also the badge that
    // stays visible when the Status column is off by default.
    const tones = new Set(Object.values(AUTHORIZATION_POLICY_ACTION_SEVERITY))
    expect(tones).toEqual(new Set(['neutral']))
    expect(Object.keys(AUTHORIZATION_POLICY_ACTION_SEVERITY).sort())
      .toEqual(['ALLOW', 'AUDIT', 'CUSTOM', 'DENY'])
  })
})

describe('getAuthorizationPolicyRuleNotice', () => {
  it('does not claim an empty DENY blocks anything', () => {
    // "If not set, the match will never occur" — a DENY with no rules matches
    // nothing and denies nothing. Calling it Deny All is the dangerous
    // direction: it tells an operator an ineffective policy protects them.
    for (const spec of [{ action: 'DENY' }, { action: 'DENY', rules: [] }]) {
      const notice = getAuthorizationPolicyRuleNotice({ spec })
      expect(notice).toMatchObject({ level: 'info', title: 'No deny rules' })
      expect(notice!.message).not.toMatch(/denies all/i)
    }
  })

  it('explains an unconditional DENY, which no ALLOW can carve an exception from', () => {
    // rules: [{}] matches every request. DENY is evaluated before ALLOW, so
    // this is not a baseline with exceptions — it blocks everything it selects.
    const notice = getAuthorizationPolicyRuleNotice({ spec: { action: 'DENY', rules: [{}] } })
    expect(notice).toMatchObject({ level: 'info', title: 'Matches all requests' })
  })

  it('stays quiet on a DENY whose rules have conditions', () => {
    expect(getAuthorizationPolicyRuleNotice({
      spec: { action: 'DENY', rules: [{ from: [{ source: { namespaces: ['dev'] } }] }] },
    })).toBeNull()
  })

  it('does not flag an unconditional ALLOW the same way', () => {
    // An ALLOW matching everything is permissive, not a blanket block.
    expect(getAuthorizationPolicyRuleNotice({ spec: { action: 'ALLOW', rules: [{}] } })).toBeNull()
  })

  it('warns on a rule-less ALLOW without asserting an outage', () => {
    for (const spec of [{}, { action: 'ALLOW', rules: [] }]) {
      const notice = getAuthorizationPolicyRuleNotice({ spec })
      expect(notice).toMatchObject({ level: 'warning', title: 'No allow rules' })
      expect(notice!.message).toMatch(/may still permit/i)
      expect(notice!.message).not.toMatch(/no traffic is allowed/i)
    }
  })

  it('stays quiet when the policy has rules', () => {
    expect(getAuthorizationPolicyRuleNotice({ spec: { action: 'ALLOW', rules: [{}] } })).toBeNull()
  })

  it('does not editorialise about AUDIT or CUSTOM', () => {
    expect(getAuthorizationPolicyRuleNotice({ spec: { action: 'AUDIT' } })).toBeNull()
    expect(getAuthorizationPolicyRuleNotice({ spec: { action: 'CUSTOM' } })).toBeNull()
  })
})
