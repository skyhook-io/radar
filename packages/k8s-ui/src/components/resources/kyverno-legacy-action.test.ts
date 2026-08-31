import { describe, it, expect } from 'vitest'
import { getKyvernoPolicyAction } from './resource-utils-kyverno'

function cpol(spec: any) {
  return { apiVersion: 'kyverno.io/v1', kind: 'ClusterPolicy', metadata: { name: 'p' }, spec }
}

/**
 * The Enforce cases here were confirmed against a live Kyverno 1.18 admission
 * controller: each one creates a pod the apiserver actually rejects, while
 * `spec.validationFailureAction` on the same policy reads Audit.
 */
describe('getKyvernoPolicyAction', () => {
  it('lets a rule-level failureAction override the spec-level one', () => {
    // Live result: pod REJECTED by validate.kyverno.svc-fail.
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Audit',
          rules: [{ name: 'r', validate: { failureAction: 'Enforce' } }],
        }),
      ),
    ).toBe('Enforce')
  })

  it('reads the failureAction on an image-verification rule', () => {
    // Live result: pod REJECTED by mutate.kyverno.svc-fail. Kyverno had
    // defaulted spec.validationFailureAction to Audit on its own.
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Audit',
          rules: [{ name: 'r', verifyImages: [{ failureAction: 'Enforce' }] }],
        }),
      ),
    ).toBe('Enforce')
  })

  it('does not claim Enforce when every rule overrides it away', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Enforce',
          rules: [{ name: 'r', validate: { failureAction: 'Audit' } }],
        }),
      ),
    ).toBe('Audit')
  })

  it('applies the spec-level action to rules that declare none', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Enforce',
          rules: [{ name: 'r', validate: { pattern: {} } }],
        }),
      ),
    ).toBe('Enforce')
  })

  // Only validate and verifyImages rules can reject, so only they inherit the
  // spec-level action. A mutate rule alongside them would otherwise report a
  // policy as blocking when its one validating rule audits.
  it('does not let a mutate rule carry the spec-level Enforce', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Enforce',
          rules: [{ name: 'v', validate: { failureAction: 'Audit' } }, { name: 'm', mutate: {} }],
        }),
      ),
    ).toBe('Audit')
  })

  it('reports Audit for a policy whose rules cannot reject at all', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({ validationFailureAction: 'Enforce', rules: [{ name: 'g', generate: {} }] }),
      ),
    ).toBe('Audit')
  })

  it('still inherits the spec action for a validating rule that declares none', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Enforce',
          rules: [{ name: 'm', mutate: {} }, { name: 'v', validate: { pattern: {} } }],
        }),
      ),
    ).toBe('Enforce')
  })

  it('falls back to Audit when nothing declares an action', () => {
    expect(getKyvernoPolicyAction(cpol({ rules: [{ name: 'r', validate: {} }] }))).toBe('Audit')
    expect(getKyvernoPolicyAction(cpol({}))).toBe('Audit')
  })

  // Kyverno's ValidationFailureAction enum accepts the deprecated lowercase
  // `enforce`/`audit` alongside the capitalized spelling, and the admission
  // controller blocks on lowercase `enforce` exactly as on `Enforce`. A legacy
  // policy carrying the lowercase value must not read as non-blocking.
  it('treats a lowercase spec-level enforce as Enforce (no rules)', () => {
    expect(getKyvernoPolicyAction(cpol({ validationFailureAction: 'enforce' }))).toBe('Enforce')
  })

  it('treats a lowercase spec-level enforce inherited by a validating rule as Enforce', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'enforce',
          rules: [{ name: 'v', validate: { pattern: {} } }],
        }),
      ),
    ).toBe('Enforce')
  })

  it('canonicalizes a lowercase spec-level audit to Audit', () => {
    expect(getKyvernoPolicyAction(cpol({ validationFailureAction: 'audit' }))).toBe('Audit')
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'audit',
          rules: [{ name: 'v', validate: { pattern: {} } }],
        }),
      ),
    ).toBe('Audit')
  })

  // A per-rule failureAction is capitalized-only per its CRD enum, but matching
  // case-insensitively there too keeps a single rule for reading the field.
  it('treats a lowercase per-rule failureAction override as Enforce', () => {
    expect(
      getKyvernoPolicyAction(
        cpol({
          validationFailureAction: 'Audit',
          rules: [{ name: 'r', validate: { failureAction: 'enforce' } }],
        }),
      ),
    ).toBe('Enforce')
  })
})
