import { describe, it, expect } from 'vitest'
import {
  KYVERNO_MODERN_PLURALS,
  getModernKyvernoPolicyStatus,
  isModernKyvernoPolicy,
} from './resource-utils-kyverno-modern'
import { isAnyKyvernoPolicyException } from './resource-utils-kyverno-exceptions'

// Bugbot #4 (MEDIUM): the ResourcesView cell dispatch matched Kyverno plurals
// without checking the API group, so a foreign CRD sharing a plural picked up
// Kyverno columns and badges while its drawer correctly stayed generic.
//
// The stakes are higher in the table than in the drawer: an absent
// `validationActions` legitimately means Deny for a real Kyverno policy, so an
// ungated foreign CR renders a red "Deny" purely for lacking a field it never
// had. These pin the predicates the cell dispatch now gates on.
describe('kyverno cell group gating', () => {
  const foreignValidatingPolicy = {
    apiVersion: 'policy.example.com/v1',
    kind: 'ValidatingPolicy',
    metadata: { name: 'not-kyverno' },
    spec: {},
  }

  it('rejects a foreign CR that merely shares the plural', () => {
    expect(KYVERNO_MODERN_PLURALS.has('validatingpolicies')).toBe(true)
    expect(isModernKyvernoPolicy(foreignValidatingPolicy)).toBe(false)
  })

  // Proof the gate is load-bearing rather than defensive: ungated, this exact
  // foreign object renders as Deny.
  it('would read as Deny without the gate', () => {
    expect(getModernKyvernoPolicyStatus(foreignValidatingPolicy).text).toBe('Deny')
    expect(getModernKyvernoPolicyStatus(foreignValidatingPolicy).level).toBe('unhealthy')
  })

  it('accepts a genuine modern Kyverno policy', () => {
    const real = { apiVersion: 'policies.kyverno.io/v1', kind: 'ValidatingPolicy', spec: { validationActions: ['Audit'] } }
    expect(isModernKyvernoPolicy(real)).toBe(true)
    expect(getModernKyvernoPolicyStatus(real).text).toBe('Audit')
  })

  // PolicyException is served by BOTH Kyverno families, so the gate accepts
  // either group while still rejecting a foreign one.
  it('accepts PolicyException from both Kyverno groups, rejects foreign', () => {
    expect(isAnyKyvernoPolicyException({ apiVersion: 'kyverno.io/v2' })).toBe(true)
    expect(isAnyKyvernoPolicyException({ apiVersion: 'policies.kyverno.io/v1' })).toBe(true)
    expect(isAnyKyvernoPolicyException({ apiVersion: 'exceptions.example.com/v1' })).toBe(false)
    expect(isAnyKyvernoPolicyException({})).toBe(false)
  })

  // The legacy cleanup pair gates on the legacy group specifically.
  it('gates CleanupPolicy on the legacy kyverno.io group', () => {
    expect('kyverno.io/v2'.startsWith('kyverno.io/')).toBe(true)
    expect('cleanup.example.com/v1'.startsWith('kyverno.io/')).toBe(false)
  })
})
