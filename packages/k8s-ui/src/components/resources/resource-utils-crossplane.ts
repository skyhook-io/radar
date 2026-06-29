// Crossplane CRD utility functions
//
// Crossplane v1 vs v2 (v2 GA in late 2025) moved several spec fields under
// `spec.crossplane.*`. Accessors here check v2 path first, fall back to v1 —
// never gate on a Crossplane-version check, since both can be present in the
// same cluster during migrations.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

interface CrossplaneCondition {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export interface CrossplaneResourceRef {
  apiVersion: string
  kind: string
  name: string
  namespace?: string
}

// ============================================================================
// DETECTION HEURISTICS
// ============================================================================

/**
 * A Managed Resource has a providerConfigRef (v1: spec.providerConfigRef,
 * v2: spec.crossplane.providerConfigRef). The crossplane.io/external-name
 * annotation is the secondary signal, but providerConfigRef is more reliable —
 * MRs always have a ProviderConfig (default or explicit), the external-name
 * may be empty until reconciliation succeeds.
 */
export function isManagedResource(data: any): boolean {
  if (!data?.spec) return false
  if (data.spec.providerConfigRef) return true
  if (data.spec.crossplane?.providerConfigRef) return true
  return false
}

/**
 * A Composite Resource (XR) or v1 Claim. XRs expose spec.resourceRefs
 * (v1) or spec.crossplane.resourceRefs (v2); v1 Claims also expose
 * singular spec.resourceRef + spec.compositionRef pointing at their
 * bound XR. The singular-ref arm matters during the period before
 * reconciliation populates resourceRefs — without it, freshly-applied
 * Claims fall through to GenericRenderer/GenericCell despite being
 * fully-formed Crossplane resources.
 *
 * Mirrors the Go isCrossplaneComposite in internal/audit/runner.go so
 * the audit walker and UI dispatch agree on what counts as a composite.
 * Distinct from an MR: an XR/Claim never has providerConfigRef.
 */
export function isComposite(data: any): boolean {
  if (!data?.spec) return false
  if (isManagedResource(data)) return false
  if (Array.isArray(data.spec.resourceRefs)) return true
  if (Array.isArray(data.spec.crossplane?.resourceRefs)) return true
  // v1 Claim: singular resourceRef + compositionRef. Both required —
  // singular resourceRef alone shows up in unrelated CRDs.
  if (data.spec.resourceRef && data.spec.compositionRef) return true
  return false
}

/**
 * A Claim is a v1-only namespaced alias for an XR. Detection: has resourceRef
 * (singular — points at the bound XR), plus compositionRef. v2 removed Claims.
 */
export function isClaim(data: any): boolean {
  if (!data?.spec) return false
  if (isManagedResource(data)) return false
  return Boolean(data.spec.resourceRef && data.spec.compositionRef)
}

// ============================================================================
// FIELD ACCESSORS (v1+v2 path-aware)
// ============================================================================

export function getProviderConfigRef(data: any): { name: string; kind?: string; apiVersion?: string } | null {
  const ref = data?.spec?.crossplane?.providerConfigRef ?? data?.spec?.providerConfigRef
  if (!ref?.name) return null
  // apiVersion is sometimes present on the ref (Crossplane v1 schema) and
  // sometimes omitted (legacy specs). Pass it through when present so
  // callers can route navigation by GVR; if absent, callers must omit
  // the group rather than guess — ProviderConfig groups can differ from
  // the MR's own group (e.g. an MR in s3.aws.upbound.io references a
  // ProviderConfig in aws.upbound.io).
  return { name: ref.name, kind: ref.kind, apiVersion: ref.apiVersion }
}

export function getCompositionRef(data: any): { name: string } | null {
  const ref = data?.spec?.crossplane?.compositionRef ?? data?.spec?.compositionRef
  if (!ref?.name) return null
  return { name: ref.name }
}

export function getCompositionRevisionRef(data: any): { name: string } | null {
  const ref = data?.spec?.crossplane?.compositionRevisionRef ?? data?.spec?.compositionRevisionRef
  if (!ref?.name) return null
  return { name: ref.name }
}

export function getCompositionUpdatePolicy(data: any): string | null {
  return data?.spec?.crossplane?.compositionUpdatePolicy ?? data?.spec?.compositionUpdatePolicy ?? null
}

export function getCrossplaneResourceRefs(data: any): CrossplaneResourceRef[] {
  const refs = data?.spec?.crossplane?.resourceRefs ?? data?.spec?.resourceRefs
  if (!Array.isArray(refs)) return []
  return refs.filter((r: any) => r?.kind && r?.name)
}

/**
 * Singular resourceRef points at the bound XR for a v1 Claim.
 */
export function getBoundXRRef(data: any): CrossplaneResourceRef | null {
  const ref = data?.spec?.resourceRef
  if (!ref?.kind || !ref?.name) return null
  return ref
}

export function getExternalName(data: any): string | null {
  return data?.metadata?.annotations?.['crossplane.io/external-name'] ?? null
}

export function getManagementPolicies(data: any): string[] | null {
  const v2 = data?.spec?.crossplane?.managementPolicies
  if (Array.isArray(v2)) return v2
  const v1 = data?.spec?.managementPolicies
  if (Array.isArray(v1)) return v1
  return null
}

export function getDeletionPolicy(data: any): string | null {
  return data?.spec?.crossplane?.deletionPolicy ?? data?.spec?.deletionPolicy ?? null
}

/**
 * Crossplane convention: setting `crossplane.io/paused: "true"` on any MR/XR/Claim
 * stops the provider from reconciling it. Universal across every provider, so
 * surfacing it as a chip on every Crossplane resource is high-leverage.
 */
export function isCrossplanePaused(data: any): boolean {
  return data?.metadata?.annotations?.['crossplane.io/paused'] === 'true'
}

/**
 * Walk ownerReferences to find the parent Composite for a Managed Resource.
 * Crossplane sets exactly one ownerRef pointing at the composing XR. Returns
 * null for standalone MRs (no composite parent).
 */
export function getComposingXRRef(data: any): { apiVersion: string; kind: string; name: string } | null {
  const refs = data?.metadata?.ownerReferences
  if (!Array.isArray(refs) || refs.length === 0) return null
  // Crossplane composing-resource ownerRefs are typed with apiextensions.crossplane.io
  // -generated CR groups. The Composite is always the controller; pick that one.
  const owner = refs.find((r: any) => r?.controller === true) ?? refs[0]
  if (!owner?.kind || !owner?.name) return null
  return { apiVersion: owner.apiVersion ?? '', kind: owner.kind, name: owner.name }
}

// ============================================================================
// STATUS — Managed Resource / Composite / Claim
// ============================================================================

function findCondition(data: any, type: string): CrossplaneCondition | undefined {
  const conds: CrossplaneCondition[] = data?.status?.conditions || []
  return conds.find(c => c.type === type)
}

/**
 * Crossplane resources expose two top-level conditions:
 *  - Synced: provider has accepted the spec (no rejected fields, no auth errors)
 *  - Ready: external resource exists in the desired state
 *
 * Both must be True for healthy; Synced=False usually means a configuration
 * problem (bad ProviderConfig, malformed spec); Ready=False usually means the
 * external API is still working or the resource hasn't converged yet.
 */
export function getCrossplaneStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) {
    return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
  }
  const ready = findCondition(data, 'Ready')
  const synced = findCondition(data, 'Synced')

  if (!ready && !synced) {
    return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }
  if (synced?.status === 'False') {
    return { text: 'Out of sync', color: healthColors.alert, level: 'alert' }
  }
  if (ready?.status === 'False') {
    return { text: ready.reason || 'Not ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (ready?.status === 'True' && synced?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (ready?.status === 'Unknown' || synced?.status === 'Unknown') {
    return { text: 'Provisioning', color: healthColors.degraded, level: 'degraded' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

/**
 * First non-empty message from a False/Unknown Ready or Synced condition —
 * used as the tooltip below status badges and as the alert banner body in
 * detail views.
 */
export function getCrossplaneStatusReason(data: any): string | null {
  const ready = findCondition(data, 'Ready')
  const synced = findCondition(data, 'Synced')
  if (synced && synced.status !== 'True' && synced.message) return synced.message
  if (ready && ready.status !== 'True' && ready.message) return ready.message
  if (synced && synced.status !== 'True' && synced.reason) return synced.reason
  if (ready && ready.status !== 'True' && ready.reason) return ready.reason
  return null
}

// ============================================================================
// STATUS — Provider
// ============================================================================

/**
 * Providers expose Installed + Healthy conditions. A Provider in
 * Installed=True but Healthy=False is mid-rollout (image pulled but pod
 * crashlooping). Installed=False means the provider package failed
 * verification or the image isn't pullable.
 */
export function getProviderStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) {
    return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
  }
  const installed = findCondition(data, 'Installed')
  const healthy = findCondition(data, 'Healthy')

  if (installed?.status === 'False') {
    return { text: installed.reason || 'Install failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (healthy?.status === 'False') {
    return { text: healthy.reason || 'Unhealthy', color: healthColors.alert, level: 'alert' }
  }
  if (installed?.status === 'True' && healthy?.status === 'True') {
    return { text: 'Healthy', color: healthColors.healthy, level: 'healthy' }
  }
  if (installed?.status === 'True') {
    return { text: 'Installed', color: healthColors.degraded, level: 'degraded' }
  }
  return { text: 'Pending', color: healthColors.unknown, level: 'unknown' }
}

// ============================================================================
// STATUS — ProviderConfig
// ============================================================================

/**
 * ProviderConfigs don't have rich status. Most expose `status.users` (the
 * number of MRs referencing them) and may carry a Ready condition in some
 * providers. We surface "Active" if anything references it, otherwise "Idle".
 */
export function getProviderConfigStatus(data: any): StatusBadge {
  if (data?.metadata?.deletionTimestamp) {
    return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
  }
  const users = typeof data?.status?.users === 'number' ? data.status.users : null
  const ready = findCondition(data, 'Ready')
  if (ready?.status === 'False') {
    return { text: ready.reason || 'Not ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (users && users > 0) {
    return { text: `${users} in use`, color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Idle', color: healthColors.neutral, level: 'neutral' }
}

export function getProviderConfigCredentialsSource(data: any): string {
  return data?.spec?.credentials?.source ?? '-'
}

// ============================================================================
// PROVIDER FIELD ACCESSORS
// ============================================================================

export function getProviderPackage(data: any): string {
  return data?.spec?.package ?? '-'
}

export function getProviderPackagePullPolicy(data: any): string | null {
  return data?.spec?.packagePullPolicy ?? null
}

export function getProviderCurrentRevision(data: any): string | null {
  return data?.status?.currentRevision ?? null
}

// ============================================================================
// COMPOSITION / XRD FIELD ACCESSORS (for cell rendering only)
// ============================================================================

export function getXRDClaimNames(data: any): { kind?: string; plural?: string } {
  const cn = data?.spec?.claimNames
  return { kind: cn?.kind, plural: cn?.plural }
}

export function getXRDKind(data: any): string {
  return data?.spec?.names?.kind ?? '-'
}

export function getCompositionMode(data: any): string {
  return data?.spec?.mode ?? 'Resources'
}

export function getCompositionFunctionCount(data: any): number {
  const pipeline = data?.spec?.pipeline
  return Array.isArray(pipeline) ? pipeline.length : 0
}

export function getCompositionCompositeTypeRef(data: any): string {
  const ref = data?.spec?.compositeTypeRef
  if (!ref) return '-'
  return ref.kind ? `${ref.kind}` : '-'
}
