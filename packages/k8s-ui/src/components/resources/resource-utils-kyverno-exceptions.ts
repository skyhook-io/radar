// PolicyException + CleanupPolicy accessors.
//
// PolicyException is served by BOTH Kyverno API families under the same Kind
// and the same plural, with genuinely different spec shapes:
//
//   kyverno.io/v2            spec.exceptions[].{policyName, ruleNames[]}, spec.match, spec.conditions, spec.background
//   policies.kyverno.io/v1   spec.policyRefs[].{kind, name}, spec.matchConditions[], spec.reportResult, spec.images
//
// So every accessor here is group-qualified. Reading `spec.exceptions` off a
// modern object (or `spec.policyRefs` off a legacy one) silently yields
// nothing, which on THIS surface means "no policies are excepted" — the exact
// opposite of the truth for an object that exempts a workload from
// enforcement.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

export const KYVERNO_LEGACY_GROUP = 'kyverno.io'
export const KYVERNO_MODERN_GROUP_PREFIX = 'policies.kyverno.io/'

export function isLegacyKyvernoPolicyException(resource: any): boolean {
  return typeof resource?.apiVersion === 'string' && resource.apiVersion.startsWith(`${KYVERNO_LEGACY_GROUP}/`)
}

export function isModernKyvernoPolicyException(resource: any): boolean {
  return typeof resource?.apiVersion === 'string' && resource.apiVersion.startsWith(KYVERNO_MODERN_GROUP_PREFIX)
}

/** Either family — the guard dispatch uses before rendering a PolicyException. */
export function isAnyKyvernoPolicyException(resource: any): boolean {
  return isLegacyKyvernoPolicyException(resource) || isModernKyvernoPolicyException(resource)
}

/** One excepted policy, normalized across both spec shapes. */
export interface KyvernoExceptedPolicy {
  /** Policy object name. */
  name: string
  /**
   * Rule names excepted within that policy (legacy only — the modern family
   * exempts whole policies, since CEL policies have no named rules).
   */
  ruleNames: string[]
  /**
   * Policy Kind being referenced (modern only). Load-bearing because
   * policyRefs can point at ValidatingPolicy or ImageValidatingPolicy and the
   * name alone doesn't say which.
   */
  kind: string
}

export function getKyvernoExceptedPolicies(resource: any): KyvernoExceptedPolicy[] {
  if (isModernKyvernoPolicyException(resource)) {
    const refs = resource?.spec?.policyRefs
    if (!Array.isArray(refs)) return []
    return refs.map((r: any) => ({
      name: r?.name || '',
      kind: r?.kind || '',
      ruleNames: [],
    }))
  }

  const exceptions = resource?.spec?.exceptions
  if (!Array.isArray(exceptions)) return []
  return exceptions.map((e: any) => ({
    name: e?.policyName || '',
    kind: '',
    ruleNames: Array.isArray(e?.ruleNames) ? e.ruleNames.filter((r: unknown): r is string => typeof r === 'string') : [],
  }))
}

/**
 * The legacy `spec.match` block (any/all → resources). Absent on the modern
 * family, which scopes via matchConditions instead.
 */
export function getKyvernoExceptionMatch(resource: any): any {
  return isLegacyKyvernoPolicyException(resource) ? resource?.spec?.match : undefined
}

export interface KyvernoNamedCEL {
  name: string
  expression: string
}

/**
 * CEL conditions narrowing the exception. Both families have them under
 * different keys: legacy `spec.conditions` (which is Kyverno's own
 * any/all condition DSL, not CEL) versus modern `spec.matchConditions` (CEL).
 * Only the modern one is a named-expression list; the legacy block is returned
 * raw by getKyvernoExceptionLegacyConditions.
 */
export function getKyvernoExceptionMatchConditions(resource: any): KyvernoNamedCEL[] {
  if (!isModernKyvernoPolicyException(resource)) return []
  const conditions = resource?.spec?.matchConditions
  if (!Array.isArray(conditions)) return []
  return conditions.map((c: any) => ({ name: c?.name || '', expression: c?.expression || '' }))
}

export function getKyvernoExceptionLegacyConditions(resource: any): any {
  return isLegacyKyvernoPolicyException(resource) ? resource?.spec?.conditions : undefined
}

/** Modern only: what result the excepted resource is reported as. */
export function getKyvernoExceptionReportResult(resource: any): string {
  return isModernKyvernoPolicyException(resource) ? resource?.spec?.reportResult || '' : ''
}

/**
 * True when the exception names no policy at all. Both shapes can express it,
 * and it means the exception applies to every policy in scope — the widest
 * possible exemption, and worth flagging.
 */
export function isBlanketKyvernoException(resource: any): boolean {
  return getKyvernoExceptedPolicies(resource).length === 0
}

export function getKyvernoPolicyExceptionStatus(resource: any): StatusBadge {
  const count = getKyvernoExceptedPolicies(resource).length
  if (count === 0) {
    return { text: 'All policies', color: healthColors.alert, level: 'alert' }
  }
  return {
    text: count === 1 ? '1 policy' : `${count} policies`,
    color: healthColors.neutral,
    level: 'neutral',
  }
}

// ============================================================================
// CLEANUP POLICY (kyverno.io/v2 — deprecated in 1.18 in favour of DeletingPolicy)
// ============================================================================

export function getKyvernoCleanupSchedule(resource: any): string {
  return resource?.spec?.schedule || ''
}

export function getKyvernoCleanupPropagationPolicy(resource: any): string {
  return resource?.spec?.deletionPropagationPolicy || ''
}

export function getKyvernoCleanupLastExecution(resource: any): string {
  return resource?.status?.lastExecutionTime || ''
}

export function getKyvernoCleanupConditions(resource: any): any {
  return resource?.spec?.conditions
}

export function getKyvernoCleanupMatch(resource: any): any {
  return resource?.spec?.match
}

export function getKyvernoCleanupExclude(resource: any): any {
  return resource?.spec?.exclude
}

/**
 * Kinds a cleanup policy matches, flattened out of the any/all match block for
 * a one-line summary. This is a delete-on-schedule object, so "what does it
 * delete" is the only question that matters at a glance.
 */
export function getKyvernoCleanupMatchedKinds(resource: any): string[] {
  const kinds = new Set<string>()
  const collect = (block: any) => {
    if (!Array.isArray(block)) return
    for (const entry of block) {
      const entryKinds = entry?.resources?.kinds
      if (Array.isArray(entryKinds)) {
        for (const k of entryKinds) if (typeof k === 'string') kinds.add(k)
      }
    }
  }
  collect(resource?.spec?.match?.any)
  collect(resource?.spec?.match?.all)
  return Array.from(kinds)
}

export function getKyvernoCleanupPolicyStatus(resource: any): StatusBadge {
  if (!getKyvernoCleanupSchedule(resource)) {
    return { text: 'No schedule', color: healthColors.unknown, level: 'unknown' }
  }
  return { text: 'Scheduled', color: healthColors.alert, level: 'alert' }
}
