/**
 * Core networking.k8s.io/v1 NetworkPolicy presentation helpers, shared by the
 * list column, the detail renderer and the flow diagram so the three cannot
 * disagree about what a policy does.
 */

export interface EffectivePolicyTypes {
  ingress: boolean
  egress: boolean
  /** True when spec.policyTypes was set; false when Kubernetes' defaulting decided. */
  explicit: boolean
}

/**
 * Kubernetes' policyTypes defaulting, applied exactly: when spec.policyTypes
 * is set it is authoritative; when omitted, a policy always isolates Ingress
 * and additionally isolates Egress iff it declares egress rules. The common
 * default-deny (`podSelector: {}` and nothing else) has no policyTypes and
 * isolates ingress — reading the field raw shows it as isolating nothing.
 */
export function effectivePolicyTypes(spec: any): EffectivePolicyTypes {
  const declared: unknown = spec?.policyTypes
  if (Array.isArray(declared) && declared.length > 0) {
    return {
      ingress: declared.includes('Ingress'),
      egress: declared.includes('Egress'),
      explicit: true,
    }
  }
  const egressRules: unknown = spec?.egress
  return {
    ingress: true,
    egress: Array.isArray(egressRules) && egressRules.length > 0,
    explicit: false,
  }
}

/** The effective types as the list they would have been declared as. */
export function effectivePolicyTypeNames(spec: any): string[] {
  const t = effectivePolicyTypes(spec)
  const out: string[] = []
  if (t.ingress) out.push('Ingress')
  if (t.egress) out.push('Egress')
  return out
}

/**
 * One NetworkPolicyPort as `PROTO/port`, `PROTO/start-end` for an endPort
 * range, or `PROTO/*` when the entry names a protocol but no port.
 */
export function formatNetworkPolicyPort(port: any): string {
  const proto = port?.protocol || 'TCP'
  const start = port?.port ?? '*'
  const end = port?.endPort
  // endPort only ever pairs with a numeric port; a range off a named port or
  // off no port is not a range Kubernetes would accept, so it isn't shown as one.
  const isRange = end != null && typeof start === 'number'
  return isRange ? `${proto}/${start}-${end}` : `${proto}/${start}`
}
