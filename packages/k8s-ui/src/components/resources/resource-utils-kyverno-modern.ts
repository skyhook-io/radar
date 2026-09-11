// Kyverno modern CEL policy family (policies.kyverno.io) utility functions.
//
// Distinct from resource-utils-kyverno.ts, which serves the legacy kyverno.io
// Policy/ClusterPolicy family (deprecated in Kyverno 1.18, removal planned for
// 1.20). The two families share a vendor and nothing else: the legacy spec is
// `rules[]` with `validationFailureAction`, the modern one is VAP-shaped
// `matchConstraints` + CEL `validations[]` with `validationActions`. Keep the
// accessors apart — a shared one would have to guess which shape it holds.

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

export const KYVERNO_MODERN_GROUP = 'policies.kyverno.io'

/**
 * True when the resource belongs to the modern CEL family. Dispatch MUST gate
 * on this rather than on the plural alone: `PolicyException` exists in both
 * kyverno.io and policies.kyverno.io with the same Kind, the same plural and
 * different spec shapes, and names like `validatingpolicies` are generic
 * enough that another vendor could shadow them.
 */
export function isModernKyvernoPolicy(resource: any): boolean {
  return typeof resource?.apiVersion === 'string' && resource.apiVersion.startsWith(`${KYVERNO_MODERN_GROUP}/`)
}

/**
 * Plurals served by policies.kyverno.io. Dispatch uses this together with
 * isModernKyvernoPolicy so a foreign CRD sharing a generic plural like
 * `validatingpolicies` falls through to generic rendering instead of being
 * described in Kyverno's vocabulary.
 */
export const KYVERNO_MODERN_PLURALS = new Set([
  'validatingpolicies',
  'namespacedvalidatingpolicies',
  'imagevalidatingpolicies',
  'namespacedimagevalidatingpolicies',
  'mutatingpolicies',
  'namespacedmutatingpolicies',
  'generatingpolicies',
  'namespacedgeneratingpolicies',
  'deletingpolicies',
  'namespaceddeletingpolicies',
])

export type KyvernoPolicyFamily = 'validating' | 'imageValidating' | 'mutating' | 'generating' | 'deleting'

/**
 * Maps a Kind to its behavioural family, collapsing each Namespaced* twin onto
 * its cluster-scoped counterpart. The twins are the same spec with a narrower
 * scope, so they render identically; scope is already conveyed by the
 * namespace column and the resource header.
 */
export function getKyvernoPolicyFamily(kind: string | undefined): KyvernoPolicyFamily | undefined {
  switch ((kind || '').replace(/^Namespaced/, '')) {
    case 'ValidatingPolicy':
      return 'validating'
    case 'ImageValidatingPolicy':
      return 'imageValidating'
    case 'MutatingPolicy':
      return 'mutating'
    case 'GeneratingPolicy':
      return 'generating'
    case 'DeletingPolicy':
      return 'deleting'
    default:
      return undefined
  }
}

// ============================================================================
// ENFORCEMENT POSTURE
// ============================================================================

export interface KyvernoEnforcementPosture {
  /** Short label for the badge, e.g. "Deny", "Audit + Warn", "Deletes on schedule". */
  label: string
  level: 'healthy' | 'degraded' | 'alert' | 'unhealthy' | 'neutral' | 'unknown'
  /** True only when a violating admission request is actually rejected. */
  blocks: boolean
  /**
   * One sentence describing what this policy does to matched resources.
   * Per-family, because "violations are recorded" is meaningless for a
   * policy that mutates, generates, or deletes on a schedule.
   */
  summary: string
  /**
   * Set when the effective posture differs from what the declared fields
   * suggest at a glance — the operator needs the reason, not just the verdict.
   */
  note?: string
}

/**
 * Reads `spec.validationActions`, applying Kyverno's real default.
 *
 * An ABSENT validationActions means Deny. This is verified behaviour, not an
 * inference: on Kyverno 1.18.2 a ValidatingPolicy with no validationActions
 * rejected a violating request ("admission webhook vpol.validate.kyverno.svc-fail
 * denied the request"). The legacy family defaults the analogous field to
 * Audit, so carrying that assumption over would under-report the modern
 * family's strictest state — the single worst error this surface could make.
 */
export function getKyvernoValidationActions(resource: any): string[] {
  const actions = resource?.spec?.validationActions
  if (Array.isArray(actions) && actions.length > 0) {
    return actions.filter((a: unknown): a is string => typeof a === 'string')
  }
  return ['Deny']
}

/** `spec.evaluation.admission.enabled`, defaulting to true per the CRD schema. */
export function isKyvernoAdmissionEnabled(resource: any): boolean {
  return resource?.spec?.evaluation?.admission?.enabled !== false
}

/**
 * `spec.evaluation.mutateExisting.enabled` (MutatingPolicy only), defaulting
 * to FALSE per the CRD schema — the opposite default from admission/background.
 *
 * Load-bearing for the posture verdict: a MutatingPolicy with admission
 * disabled still rewrites already-existing resources when this is on, so it is
 * emphatically not inactive.
 */
export function isKyvernoMutateExistingEnabled(resource: any): boolean {
  return resource?.spec?.evaluation?.mutateExisting?.enabled === true
}

/** `spec.evaluation.background.enabled`, defaulting to true per the CRD schema. */
export function isKyvernoBackgroundEnabled(resource: any): boolean {
  return resource?.spec?.evaluation?.background?.enabled !== false
}

/**
 * `spec.evaluation.mode`, defaulting to "Kubernetes". A policy in "JSON" mode
 * is evaluated against JSON payloads out-of-cluster and never participates in
 * admission, so it cannot block regardless of its declared actions.
 */
export function getKyvernoEvaluationMode(resource: any): string {
  return resource?.spec?.evaluation?.mode || 'Kubernetes'
}

export function getKyvernoFailurePolicy(resource: any): string {
  return resource?.spec?.failurePolicy || ''
}

/**
 * `spec.failurePolicy` with the CRD's own default applied.
 *
 * An ABSENT failurePolicy means Fail — "Allowed values are Ignore or Fail.
 * Defaults to Fail", per the published schema, and most policies never set it.
 * Reading absent as anything else inverts the consequence of an engine error:
 * the screen would say those admissions are "allowed through unchecked" when the
 * cluster is in fact rejecting them. Same defaulting trap as validationActions
 * above, in the other direction.
 *
 * Kept separate from the raw reader so the Evaluation section can still show
 * only what the policy actually declares.
 */
export function getKyvernoEffectiveFailurePolicy(resource: any): string {
  return (
    resource?.spec?.failurePolicy ||
    // Where the legacy family keeps it since 1.13. Reading only the modern
    // location would default a policy that explicitly set Ignore to Fail, and
    // announce a rejection the cluster is not making.
    resource?.spec?.webhookConfiguration?.failurePolicy ||
    'Fail'
  )
}

/**
 * Computes what the policy actually does, rather than echoing a single field.
 *
 * Three fields decide whether a validating policy blocks, and reading any one
 * alone gets it wrong: `validationActions` (verified above to mean Deny when
 * absent), `evaluation.admission.enabled`, and `evaluation.mode`. Verified on
 * Kyverno 1.18.2: a policy with `validationActions: [Deny]` and
 * `evaluation.admission.enabled: false` did NOT block a violating pod, while
 * the same policy with admission enabled did. Rendering that first policy as a
 * red "Deny" badge would tell an operator their cluster is protected when it
 * is not.
 *
 * `failurePolicy` is deliberately NOT part of the blocks calculation — it
 * governs what happens when the ENGINE errors, not when the policy fails, so
 * it changes the failure mode rather than the enforcement posture.
 */
export function getKyvernoEnforcementPosture(
  resource: any,
  family: KyvernoPolicyFamily | undefined
): KyvernoEnforcementPosture {
  const admissionEnabled = isKyvernoAdmissionEnabled(resource)
  const mode = getKyvernoEvaluationMode(resource)
  const backgroundEnabled = isKyvernoBackgroundEnabled(resource)
  // Only the validating families carry validationActions; asking for them on a
  // mutating/generating policy would read the Deny default off a field that
  // kind never has.
  const validating = family === 'validating' || family === 'imageValidating'
  const declaresBlocking = validating && getKyvernoValidationActions(resource).includes('Deny')

  if (family === 'deleting') {
    const schedule = getKyvernoSchedule(resource)
    return {
      label: schedule ? `Deletes on schedule (${schedule})` : 'Deletes on schedule',
      level: 'alert',
      blocks: false,
      summary: 'Matched resources are deleted when the schedule fires.',
      note: schedule ? undefined : 'No schedule set — this policy will not run.',
    }
  }

  if (mode === 'JSON') {
    return {
      label: 'JSON mode',
      level: declaresBlocking ? 'alert' : 'neutral',
      blocks: false,
      summary: 'Evaluated outside the cluster; it does not take part in admission.',
      note: 'Evaluated against JSON payloads outside the cluster; it does not take part in admission.',
    }
  }

  if (family === 'generating') {
    return {
      label: admissionEnabled ? 'Generates resources' : 'Generates resources (background only)',
      level: 'neutral',
      blocks: false,
      summary: 'Matched resources trigger generation of the resources below.',
    }
  }

  if (family === 'mutating') {
    const mutatesExisting = isKyvernoMutateExistingEnabled(resource)
    if (admissionEnabled) {
      return {
        label: 'Mutates on admission',
        level: 'degraded',
        blocks: false,
        summary: mutatesExisting
          ? 'Matched resources are modified as they are admitted, and existing ones are rewritten too.'
          : 'Matched resources are modified as they are admitted.',
      }
    }
    // Admission off is NOT the same as inactive: mutateExisting rewrites
    // resources that already exist, independently of the admission path.
    // Calling this policy inactive would tell an operator nothing is being
    // changed while it is actively rewriting their cluster.
    if (mutatesExisting) {
      return {
        label: 'Mutates existing',
        level: 'degraded',
        blocks: false,
        summary: 'Existing matched resources are rewritten by background scans.',
        note: 'Admission evaluation is disabled, so nothing is modified as it is admitted — but mutateExisting is on.',
      }
    }
    return {
      label: 'Inactive',
      level: 'alert',
      blocks: false,
      summary: 'This policy modifies nothing.',
      note: 'Admission evaluation is disabled and mutateExisting is off, so this policy mutates nothing.',
    }
  }

  // validating + imageValidating
  const actions = getKyvernoValidationActions(resource)
  const declaresDeny = actions.includes('Deny')

  if (!admissionEnabled) {
    return {
      label: backgroundEnabled ? 'Background only' : 'Inactive',
      // Tone marks the DISCREPANCY, not the severity of the action. A policy
      // declaring Deny that evaluates nothing at admission is a gap between
      // what an operator believes protects the cluster and what does — it must
      // not read as calm. `alert` rather than `degraded` because `degraded` is
      // already the deliberate Warn posture, and "I meant to audit" must not
      // share a tone with "I meant to block and don't". A policy that never
      // evaluates at all is a discrepancy whatever it declared.
      level: !backgroundEnabled ? 'alert' : declaresBlocking ? 'alert' : 'neutral',
      blocks: false,
      summary: backgroundEnabled
        ? 'Violations are recorded by background scans, not blocked.'
        : 'This policy evaluates nothing.',
      note: declaresDeny
        ? backgroundEnabled
          ? 'Declares Deny, but admission evaluation is disabled — violations are reported, never blocked.'
          : 'Declares Deny, but both admission and background evaluation are disabled — this policy does nothing.'
        : backgroundEnabled
          ? 'Admission evaluation is disabled — violations are reported by background scans only.'
          : 'Both admission and background evaluation are disabled — this policy does nothing.',
    }
  }

  if (declaresDeny) {
    return {
      label: actions.join(' + '),
      level: 'unhealthy',
      blocks: true,
      summary: 'Violating requests are rejected at admission.',
    }
  }
  if (actions.includes('Warn')) {
    return {
      label: actions.join(' + '),
      level: 'degraded',
      blocks: false,
      summary: 'Violations are surfaced as admission warnings, not blocked.',
    }
  }
  return {
    label: actions.join(' + ') || 'Audit',
    level: 'neutral',
    blocks: false,
    summary: 'Violations are recorded in policy reports, not blocked.',
  }
}

// ============================================================================
// MATCH SCOPE
// ============================================================================

export interface KyvernoResourceRule {
  apiGroups: string[]
  apiVersions: string[]
  resources: string[]
  operations: string[]
  resourceNames: string[]
  scope: string
}

function normalizeRule(raw: any): KyvernoResourceRule {
  const list = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
  return {
    apiGroups: list(raw?.apiGroups),
    apiVersions: list(raw?.apiVersions),
    resources: list(raw?.resources),
    operations: list(raw?.operations),
    resourceNames: list(raw?.resourceNames),
    scope: typeof raw?.scope === 'string' ? raw.scope : '',
  }
}

export function getKyvernoResourceRules(resource: any): KyvernoResourceRule[] {
  const rules = resource?.spec?.matchConstraints?.resourceRules
  return Array.isArray(rules) ? rules.map(normalizeRule) : []
}

export function getKyvernoExcludeResourceRules(resource: any): KyvernoResourceRule[] {
  const rules = resource?.spec?.matchConstraints?.excludeResourceRules
  return Array.isArray(rules) ? rules.map(normalizeRule) : []
}

export function getKyvernoNamespaceSelector(resource: any): any {
  return resource?.spec?.matchConstraints?.namespaceSelector
}

export function getKyvernoObjectSelector(resource: any): any {
  return resource?.spec?.matchConstraints?.objectSelector
}

/**
 * Renders one resourceRule as a kubectl-ish string, e.g.
 * "CREATE, UPDATE apps/v1 deployments". An empty apiGroup is the core group,
 * which reads as "v1" rather than "/v1".
 */
export function formatKyvernoResourceRule(rule: KyvernoResourceRule): string {
  const groups = rule.apiGroups.length > 0 ? rule.apiGroups : ['']
  const versions = rule.apiVersions.length > 0 ? rule.apiVersions.join(',') : '*'
  const target = groups
    .map((g) => (g === '' || g === '*' ? versions : `${g}/${versions}`))
    .join(', ')
  const resources = rule.resources.length > 0 ? rule.resources.join(', ') : '*'
  const ops = rule.operations.length > 0 ? rule.operations.join(', ') : '*'
  return `${ops}  ${target}  ${resources}`
}

/** Short "what does this apply to" summary for table columns. */
export function getKyvernoMatchSummary(resource: any): string {
  const rules = getKyvernoResourceRules(resource)
  if (rules.length === 0) return '-'
  const resources = new Set<string>()
  for (const rule of rules) {
    for (const r of rule.resources) resources.add(r)
  }
  if (resources.size === 0) return '-'
  const list = Array.from(resources)
  return list.length <= 3 ? list.join(', ') : `${list.slice(0, 3).join(', ')} +${list.length - 3}`
}

// ============================================================================
// CEL EXPRESSIONS + PER-FAMILY SPEC ACCESSORS
// ============================================================================

export interface KyvernoCELValidation {
  expression: string
  message: string
  messageExpression: string
  reason: string
}

export function getKyvernoValidations(resource: any): KyvernoCELValidation[] {
  const validations = resource?.spec?.validations
  if (!Array.isArray(validations)) return []
  return validations.map((v: any) => ({
    expression: v?.expression || '',
    message: v?.message || '',
    messageExpression: v?.messageExpression || '',
    reason: v?.reason || '',
  }))
}

export interface KyvernoNamedExpression {
  name: string
  expression: string
}

function namedExpressions(raw: any): KyvernoNamedExpression[] {
  if (!Array.isArray(raw)) return []
  return raw.map((v: any) => ({ name: v?.name || '', expression: v?.expression || '' }))
}

export function getKyvernoVariables(resource: any): KyvernoNamedExpression[] {
  return namedExpressions(resource?.spec?.variables)
}

export function getKyvernoMatchConditions(resource: any): KyvernoNamedExpression[] {
  return namedExpressions(resource?.spec?.matchConditions)
}

/** DeletingPolicy: `spec.conditions[]` decides which matched objects are deleted. */
export function getKyvernoDeletionConditions(resource: any): KyvernoNamedExpression[] {
  return namedExpressions(resource?.spec?.conditions)
}

export function getKyvernoSchedule(resource: any): string {
  return resource?.spec?.schedule || ''
}

/**
 * `status.lastExecutionTime` — when the schedule last fired.
 *
 * This, not readiness, is the health signal for a scheduled policy. Kyverno
 * rejects an uncompilable DeletingPolicy at admission, so a broken one never
 * exists to be flagged; the real failure is a policy that exists and never
 * runs. Empty means it has not run since creation.
 */
export function getKyvernoLastExecutionTime(resource: any): string {
  return resource?.status?.lastExecutionTime || ''
}

export function getKyvernoDeletionPropagationPolicy(resource: any): string {
  return resource?.spec?.deletionPropagationPolicy || ''
}

export function getKyvernoReinvocationPolicy(resource: any): string {
  return resource?.spec?.reinvocationPolicy || ''
}

export interface KyvernoMutation {
  patchType: string
  expression: string
}

export function getKyvernoMutations(resource: any): KyvernoMutation[] {
  const mutations = resource?.spec?.mutations
  if (!Array.isArray(mutations)) return []
  return mutations.map((m: any) => ({
    patchType: m?.patchType || '',
    // ApplyConfiguration and JSONPatch carry their CEL under different keys.
    expression: m?.applyConfiguration?.expression || m?.jsonPatch?.expression || '',
  }))
}

export function getKyvernoGenerations(resource: any): string[] {
  const generate = resource?.spec?.generate
  if (!Array.isArray(generate)) return []
  // Each entry declares its target as exactly one of a CEL `expression` or a
  // YAML `template.value`; both are first-class ways to say what gets generated.
  return generate.map((g: any) => g?.expression || g?.template?.value || '').filter(Boolean)
}

/**
 * GeneratingPolicy's evaluation block is shaped differently from the
 * validating one: each key is an object with `.enabled` rather than a bare
 * boolean, and the keys are generation-lifecycle concerns.
 */
export function getKyvernoGenerationSettings(resource: any): {
  generateExisting: boolean
  synchronize: boolean
  orphanDownstreamOnPolicyDelete: boolean
} {
  const evaluation = resource?.spec?.evaluation || {}
  return {
    generateExisting: evaluation.generateExisting?.enabled === true,
    synchronize: evaluation.synchronize?.enabled === true,
    orphanDownstreamOnPolicyDelete: evaluation.orphanDownstreamOnPolicyDelete?.enabled === true,
  }
}

// ============================================================================
// IMAGE VALIDATING POLICY (supply chain)
// ============================================================================

export interface KyvernoImageAttestor {
  name: string
  /** "cosign" | "notary" — which verification mechanism this attestor uses. */
  kind: string
  /** Human-readable identity summary: keyless issuer/subject, key ref, or cert. */
  identity: string
}

export function getKyvernoAttestors(resource: any): KyvernoImageAttestor[] {
  const attestors = resource?.spec?.attestors
  if (!Array.isArray(attestors)) return []
  return attestors.map((a: any) => {
    if (a?.cosign) {
      const identities = a.cosign.keyless?.identities
      if (Array.isArray(identities) && identities.length > 0) {
        const summary = identities
          .map((id: any) => {
            const issuer = id?.issuer || id?.issuerRegExp || '*'
            const subject = id?.subject || id?.subjectRegExp || '*'
            return `${subject} via ${issuer}`
          })
          .join('; ')
        return { name: a?.name || '', kind: 'cosign', identity: `keyless: ${summary}` }
      }
      if (a.cosign.key) return { name: a?.name || '', kind: 'cosign', identity: 'public key' }
      if (a.cosign.certificate) return { name: a?.name || '', kind: 'cosign', identity: 'certificate' }
      return { name: a?.name || '', kind: 'cosign', identity: '' }
    }
    if (a?.notary) {
      return { name: a?.name || '', kind: 'notary', identity: a.notary.certs ? 'certificate chain' : '' }
    }
    return { name: a?.name || '', kind: '', identity: '' }
  })
}

export interface KyvernoImageAttestation {
  name: string
  /** "intoto" | "referrer" */
  kind: string
  type: string
}

export function getKyvernoAttestations(resource: any): KyvernoImageAttestation[] {
  const attestations = resource?.spec?.attestations
  if (!Array.isArray(attestations)) return []
  return attestations.map((a: any) => {
    if (a?.intoto) return { name: a?.name || '', kind: 'intoto', type: a.intoto.type || '' }
    if (a?.referrer) return { name: a?.name || '', kind: 'referrer', type: a.referrer.type || '' }
    return { name: a?.name || '', kind: '', type: '' }
  })
}

/** `spec.matchImageReferences[]` — which image refs the policy scopes itself to. */
export function getKyvernoImageReferences(resource: any): string[] {
  const refs = resource?.spec?.matchImageReferences
  if (!Array.isArray(refs)) return []
  return refs.map((r: any) => r?.glob || r?.expression || '').filter(Boolean)
}

/** `spec.images[]` — CEL expressions extracting image refs from the resource. */
export function getKyvernoImageExtractors(resource: any): KyvernoNamedExpression[] {
  return namedExpressions(resource?.spec?.images)
}

// ============================================================================
// STATUS
// ============================================================================

/**
 * Modern policies report readiness via `status.conditionStatus.ready` — the
 * controller sets it false when the CEL fails to compile, which is the single
 * most common "why isn't my policy working" cause.
 */
export function getKyvernoPolicyReady(resource: any): boolean | undefined {
  const ready = resource?.status?.conditionStatus?.ready
  return typeof ready === 'boolean' ? ready : undefined
}

export function getKyvernoPolicyStatusMessage(resource: any): string {
  return resource?.status?.conditionStatus?.message || ''
}

export function getKyvernoPolicyConditions(resource: any): any[] {
  const conditions = resource?.status?.conditionStatus?.conditions
  return Array.isArray(conditions) ? conditions : []
}

/**
 * Table/drawer status badge. Readiness outranks posture: a policy that failed
 * to compile enforces nothing, so surfacing its declared "Deny" would be
 * actively misleading.
 *
 * This is why the eight admission-shaped kinds have no separate Ready column:
 * readiness is not missing from their tables, it REPLACES the posture in this
 * one cell. Upstream's own printer columns list READY separately, so the
 * absence looks like an omission at a glance — it isn't. Adding a second Ready
 * column beside Enforcement would show one irrelevant value on every row,
 * because the two states are mutually exclusive by construction here.
 *
 * The Deleting kinds are the exception and DO carry their own `ready` column
 * (see KNOWN_COLUMNS): their headline column is the cron schedule rather than
 * a posture, so nothing else in the row would ever surface a failed compile.
 */
export function getModernKyvernoPolicyStatus(resource: any): StatusBadge {
  const ready = getKyvernoPolicyReady(resource)
  if (ready === false) {
    return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const family = getKyvernoPolicyFamily(resource?.kind)
  const posture = getKyvernoEnforcementPosture(resource, family)
  return { text: posture.label, color: healthColors[posture.level], level: posture.level }
}
