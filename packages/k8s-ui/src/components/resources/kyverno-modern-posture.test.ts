import { describe, it, expect } from 'vitest'
import {
  getKyvernoEnforcementPosture,
  getKyvernoGenerations,
  getKyvernoPolicyFamily,
  getKyvernoValidationActions,
  getKyvernoMatchSummary,
  getModernKyvernoPolicyStatus,
  isKyvernoAdmissionEnabled,
  isModernKyvernoPolicy,
} from './resource-utils-kyverno-modern'

function vpol(spec: any = {}, extra: any = {}) {
  return {
    apiVersion: 'policies.kyverno.io/v1',
    kind: 'ValidatingPolicy',
    metadata: { name: 'p' },
    spec,
    ...extra,
  }
}

describe('validationActions defaulting', () => {
  // Verified against Kyverno 1.18.2: a ValidatingPolicy with no
  // validationActions rejected a violating request. The legacy family
  // defaults the analogous field to Audit, so this is the assumption most
  // likely to be carried over wrongly — and it would under-report the
  // strictest possible state.
  it('treats an absent validationActions as Deny', () => {
    expect(getKyvernoValidationActions(vpol({}))).toEqual(['Deny'])
    expect(getKyvernoEnforcementPosture(vpol({}), 'validating').blocks).toBe(true)
  })

  it('treats an empty validationActions array as Deny', () => {
    expect(getKyvernoValidationActions(vpol({ validationActions: [] }))).toEqual(['Deny'])
  })

  it('reads explicit actions verbatim', () => {
    expect(getKyvernoValidationActions(vpol({ validationActions: ['Audit', 'Warn'] }))).toEqual(['Audit', 'Warn'])
  })
})

describe('effective enforcement posture', () => {
  it('reports Deny as blocking when admission evaluation is on', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({ validationActions: ['Deny'], evaluation: { admission: { enabled: true } } }),
      'validating'
    )
    expect(posture.blocks).toBe(true)
    expect(posture.label).toBe('Deny')
    expect(posture.level).toBe('unhealthy')
  })

  // The case the whole posture computation exists for. Verified on Kyverno
  // 1.18.2: this exact spec did NOT block a violating pod, while the same
  // policy with admission enabled did. Rendering it as a red "Deny" would
  // tell an operator the cluster is protected when it is not.
  it('does not report blocking when admission evaluation is disabled, even for Deny', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({ validationActions: ['Deny'], evaluation: { admission: { enabled: false } } }),
      'validating'
    )
    expect(posture.blocks).toBe(false)
    expect(posture.label).toBe('Background only')
    expect(posture.note).toMatch(/never blocked/i)
  })

  // Tone marks the discrepancy between declared and effective, not the
  // severity of the declared action. `alert` rather than `degraded` because
  // `degraded` is already the deliberate Warn posture — "I meant to audit" and
  // "I meant to block and don't" must not share a tone.
  it('flags a Deny-declaring background-only policy as a discrepancy', () => {
    const p = getKyvernoEnforcementPosture(
      vpol({ validationActions: ['Deny'], evaluation: { admission: { enabled: false } } }),
      'validating'
    )
    expect(p.label).toBe('Background only')
    expect(p.level).toBe('alert')
  })

  // No Deny declared means no protection gap — reporting-only either way.
  it('leaves a non-blocking background-only policy calm', () => {
    const p = getKyvernoEnforcementPosture(
      vpol({ validationActions: ['Audit'], evaluation: { admission: { enabled: false } } }),
      'validating'
    )
    expect(p.label).toBe('Background only')
    expect(p.level).toBe('neutral')
  })

  // Deliberate postures keep their own tones and must stay distinct from the
  // discrepancy tier above.
  it('keeps deliberate Audit and Audit + Warn visually distinct from a discrepancy', () => {
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Audit'] }), 'validating').level).toBe('neutral')
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Audit', 'Warn'] }), 'validating').level).toBe('degraded')
  })

  it('flags JSON mode as a discrepancy only when the policy declares Deny', () => {
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Deny'], evaluation: { mode: 'JSON' } }), 'validating').level).toBe('alert')
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Audit'], evaluation: { mode: 'JSON' } }), 'validating').level).toBe('neutral')
  })

  // A policy that evaluates nothing at all declared *something* and delivers
  // none of it, whatever that something was.
  it('flags a fully inactive policy as a discrepancy regardless of declared action', () => {
    const ev = { admission: { enabled: false }, background: { enabled: false } }
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Deny'], evaluation: ev }), 'validating').level).toBe('alert')
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Audit'], evaluation: ev }), 'validating').level).toBe('alert')
  })

  it('flags an inactive mutating policy as a discrepancy', () => {
    const p = getKyvernoEnforcementPosture(vpol({ evaluation: { admission: { enabled: false } } }), 'mutating')
    expect(p.label).toBe('Inactive')
    expect(p.level).toBe('alert')
  })

  it('says the policy does nothing when both admission and background are off', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({
        validationActions: ['Deny'],
        evaluation: { admission: { enabled: false }, background: { enabled: false } },
      }),
      'validating'
    )
    expect(posture.blocks).toBe(false)
    expect(posture.label).toBe('Inactive')
    expect(posture.note).toMatch(/does nothing/i)
  })

  // JSON mode evaluates against payloads outside the cluster, so it never
  // participates in admission regardless of declared actions.
  it('does not report blocking in JSON evaluation mode', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({ validationActions: ['Deny'], evaluation: { mode: 'JSON' } }),
      'validating'
    )
    expect(posture.blocks).toBe(false)
    expect(posture.label).toBe('JSON mode')
  })

  it('treats Audit and Warn as non-blocking', () => {
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Audit'] }), 'validating').blocks).toBe(false)
    expect(getKyvernoEnforcementPosture(vpol({ validationActions: ['Warn'] }), 'validating').blocks).toBe(false)
  })

  it('reports Deny as blocking when combined with other actions', () => {
    const posture = getKyvernoEnforcementPosture(vpol({ validationActions: ['Deny', 'Audit'] }), 'validating')
    expect(posture.blocks).toBe(true)
    expect(posture.label).toBe('Deny + Audit')
  })

  // failurePolicy governs what happens when the ENGINE errors, not when the
  // policy fails, so it must not move the blocking verdict either way.
  it('ignores failurePolicy when deciding whether the policy blocks', () => {
    const ignore = getKyvernoEnforcementPosture(
      vpol({ validationActions: ['Deny'], failurePolicy: 'Ignore' }),
      'validating'
    )
    const fail = getKyvernoEnforcementPosture(vpol({ validationActions: ['Deny'], failurePolicy: 'Fail' }), 'validating')
    expect(ignore.blocks).toBe(true)
    expect(fail.blocks).toBe(true)
  })
})

describe('non-validating families', () => {
  it('describes mutating policies as mutating, never blocking', () => {
    const posture = getKyvernoEnforcementPosture(vpol({ mutations: [] }), 'mutating')
    expect(posture.blocks).toBe(false)
    expect(posture.label).toBe('Mutates on admission')
  })

  it('marks a mutating policy inactive when admission is disabled and it mutates nothing existing', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({ evaluation: { admission: { enabled: false } } }),
      'mutating'
    )
    expect(posture.label).toBe('Inactive')
  })

  // Codex #7: mutateExisting rewrites resources that already exist,
  // independently of the admission path. Calling such a policy "Inactive"
  // tells an operator nothing is changing while it actively rewrites their
  // cluster. Verified against the upstream CRD: MutatingPolicy's
  // spec.evaluation.mutateExisting.enabled defaults to false, the opposite of
  // admission/background.
  it('does not call a mutating policy inactive when mutateExisting is on', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({ evaluation: { admission: { enabled: false }, mutateExisting: { enabled: true } } }),
      'mutating'
    )
    expect(posture.label).toBe('Mutates existing')
    expect(posture.label).not.toBe('Inactive')
    expect(posture.summary).toMatch(/rewritten/i)
    expect(posture.note).toMatch(/mutateExisting is on/i)
  })

  it('mentions existing-resource rewriting even when admission is enabled', () => {
    const posture = getKyvernoEnforcementPosture(
      vpol({ evaluation: { admission: { enabled: true }, mutateExisting: { enabled: true } } }),
      'mutating'
    )
    expect(posture.label).toBe('Mutates on admission')
    expect(posture.summary).toMatch(/existing/i)
  })

  // A deleting policy has no "violations" — the generic non-blocking sentence
  // must not leak into a family it does not describe.
  it('describes deletion in its own terms, not violation terms', () => {
    const posture = getKyvernoEnforcementPosture(vpol({ schedule: '0 2 * * *' }), 'deleting')
    expect(posture.summary).toMatch(/deleted/i)
    expect(posture.summary).not.toMatch(/violation/i)
  })

  it('describes generation in its own terms', () => {
    const posture = getKyvernoEnforcementPosture(vpol({ generate: [] }), 'generating')
    expect(posture.summary).not.toMatch(/violation/i)
  })

  it('surfaces the cron in a deleting policy posture', () => {
    const posture = getKyvernoEnforcementPosture(vpol({ schedule: '0 */6 * * *' }), 'deleting')
    expect(posture.blocks).toBe(false)
    expect(posture.label).toContain('0 */6 * * *')
  })

  it('flags a deleting policy with no schedule as never running', () => {
    const posture = getKyvernoEnforcementPosture(vpol({}), 'deleting')
    expect(posture.note).toMatch(/will not run/i)
  })
})

describe('status badge', () => {
  // A policy that failed to compile enforces nothing whatever its spec says,
  // so readiness has to outrank the declared posture.
  it('reports Not Ready ahead of the enforcement posture', () => {
    const badge = getModernKyvernoPolicyStatus(
      vpol({ validationActions: ['Deny'] }, { status: { conditionStatus: { ready: false } } })
    )
    expect(badge.text).toBe('Not Ready')
    expect(badge.level).toBe('unhealthy')
  })

  it('falls back to the posture when ready', () => {
    const badge = getModernKyvernoPolicyStatus(
      vpol({ validationActions: ['Audit'] }, { status: { conditionStatus: { ready: true } } })
    )
    expect(badge.text).toBe('Audit')
  })
})

describe('family + group detection', () => {
  it('collapses namespaced twins onto their cluster-scoped family', () => {
    expect(getKyvernoPolicyFamily('NamespacedValidatingPolicy')).toBe('validating')
    expect(getKyvernoPolicyFamily('ValidatingPolicy')).toBe('validating')
    expect(getKyvernoPolicyFamily('NamespacedImageValidatingPolicy')).toBe('imageValidating')
    expect(getKyvernoPolicyFamily('DeletingPolicy')).toBe('deleting')
    expect(getKyvernoPolicyFamily('SomethingElse')).toBeUndefined()
  })

  // The guard that stops a foreign CRD sharing a generic plural like
  // `validatingpolicies` from being described in Kyverno's vocabulary.
  it('only matches the policies.kyverno.io group', () => {
    expect(isModernKyvernoPolicy({ apiVersion: 'policies.kyverno.io/v1' })).toBe(true)
    expect(isModernKyvernoPolicy({ apiVersion: 'kyverno.io/v1' })).toBe(false)
    expect(isModernKyvernoPolicy({ apiVersion: 'example.com/v1' })).toBe(false)
    expect(isModernKyvernoPolicy({})).toBe(false)
  })

  it('defaults admission evaluation to enabled when unset', () => {
    expect(isKyvernoAdmissionEnabled(vpol({}))).toBe(true)
    expect(isKyvernoAdmissionEnabled(vpol({ evaluation: {} }))).toBe(true)
    expect(isKyvernoAdmissionEnabled(vpol({ evaluation: { admission: { enabled: false } } }))).toBe(false)
  })
})

describe('match summary', () => {
  it('summarizes matched resources and truncates long lists', () => {
    const spec = {
      matchConstraints: {
        resourceRules: [
          { apiGroups: [''], apiVersions: ['v1'], resources: ['pods', 'configmaps'], operations: ['CREATE'] },
          { apiGroups: ['apps'], apiVersions: ['v1'], resources: ['deployments', 'statefulsets'], operations: ['CREATE'] },
        ],
      },
    }
    expect(getKyvernoMatchSummary(vpol(spec))).toBe('pods, configmaps, deployments +1')
  })

  it('returns a dash when nothing is matched', () => {
    expect(getKyvernoMatchSummary(vpol({}))).toBe('-')
  })
})

// A GeneratingPolicy declares each generated target as exactly one of an
// `expression` (CEL returning the resources) or a `template` (a YAML document,
// optionally CEL-interpolated). Both are first-class; the "Generates" count and
// list must see either form.
describe('generation targets', () => {
  it('counts expression-form generations', () => {
    const gen = getKyvernoGenerations(vpol({ generate: [{ expression: 'generator.Apply(ns, [cm])' }] }))
    expect(gen).toEqual(['generator.Apply(ns, [cm])'])
  })

  it('counts template-form generations', () => {
    const value = 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n'
    const gen = getKyvernoGenerations(vpol({ generate: [{ template: { interpolate: 'none', value } }] }))
    expect(gen).toHaveLength(1)
    expect(gen[0]).toContain('ConfigMap')
  })

  it('counts a mix of expression and template forms', () => {
    const gen = getKyvernoGenerations(
      vpol({
        generate: [
          { expression: 'generator.Apply(ns, [cm])' },
          { template: { value: 'kind: NetworkPolicy\n' } },
        ],
      }),
    )
    expect(gen).toHaveLength(2)
  })

  it('returns nothing for an empty or absent generate block', () => {
    expect(getKyvernoGenerations(vpol({ generate: [] }))).toEqual([])
    expect(getKyvernoGenerations(vpol({}))).toEqual([])
  })
})
