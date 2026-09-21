import { describe, expect, it } from 'vitest'
import { effectivePolicyTypeNames, effectivePolicyTypes, formatNetworkPolicyPort } from './network-policy'

describe('effectivePolicyTypes', () => {
  it('reads an explicit list as authoritative', () => {
    expect(effectivePolicyTypes({ policyTypes: ['Egress'] })).toEqual({ ingress: false, egress: true, explicit: true })
    expect(effectivePolicyTypes({ policyTypes: ['Ingress', 'Egress'], egress: [] })).toEqual({ ingress: true, egress: true, explicit: true })
  })

  it('defaults an omitted list to Ingress — the classic default-deny', () => {
    expect(effectivePolicyTypes({ podSelector: {} })).toEqual({ ingress: true, egress: false, explicit: false })
    expect(effectivePolicyTypes({ policyTypes: [] })).toEqual({ ingress: true, egress: false, explicit: false })
    expect(effectivePolicyTypes(undefined)).toEqual({ ingress: true, egress: false, explicit: false })
  })

  it('adds Egress to the default only when egress rules are declared', () => {
    expect(effectivePolicyTypes({ egress: [{}] })).toEqual({ ingress: true, egress: true, explicit: false })
    expect(effectivePolicyTypes({ egress: [] })).toEqual({ ingress: true, egress: false, explicit: false })
  })

  it('names the effective types as Ingress then Egress', () => {
    expect(effectivePolicyTypeNames({})).toEqual(['Ingress'])
    expect(effectivePolicyTypeNames({ egress: [{}] })).toEqual(['Ingress', 'Egress'])
    expect(effectivePolicyTypeNames({ policyTypes: ['Egress'] })).toEqual(['Egress'])
    expect(effectivePolicyTypeNames({ policyTypes: ['Egress', 'Ingress'] })).toEqual(['Ingress', 'Egress'])
  })
})

describe('formatNetworkPolicyPort', () => {
  it('renders single ports, ranges, named ports and protocol-only entries', () => {
    expect(formatNetworkPolicyPort({ port: 80 })).toBe('TCP/80')
    expect(formatNetworkPolicyPort({ protocol: 'UDP', port: 53 })).toBe('UDP/53')
    expect(formatNetworkPolicyPort({ port: 9000, endPort: 9100 })).toBe('TCP/9000-9100')
    expect(formatNetworkPolicyPort({ port: 'metrics' })).toBe('TCP/metrics')
    expect(formatNetworkPolicyPort({ protocol: 'SCTP' })).toBe('SCTP/*')
  })

  it('never shows a range off a named port or off no port', () => {
    expect(formatNetworkPolicyPort({ port: 'metrics', endPort: 9100 })).toBe('TCP/metrics')
    expect(formatNetworkPolicyPort({ endPort: 9100 })).toBe('TCP/*')
  })
})
