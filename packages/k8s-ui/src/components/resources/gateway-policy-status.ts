import type { GenericStatus } from './generic-status'

/**
 * Gateway API policy attachment (GEP-713) reports status per *ancestor*, at
 * `status.ancestors[].conditions`, not at `status.conditions` — so the generic
 * ladder finds nothing and these kinds render "-".
 *
 * The shape is shared by every policy implementation: Gateway API's own
 * BackendTLSPolicy and BackendLBPolicy, Envoy Gateway's SecurityPolicy /
 * ClientTrafficPolicy / BackendTrafficPolicy / EnvoyPatchPolicy /
 * EnvoyExtensionPolicy, and GKE's GCPBackendPolicy family. Dispatching on the
 * shape rather than on a list of kinds means a policy from an implementation
 * Radar has never heard of reads correctly too.
 *
 * Kept separate from the generic ladder rather than folded into it, because the
 * ladder resolves positive conditions before negative ones and that order is
 * wrong here: a policy is routinely `Accepted=True` *and* `Programmed=False`,
 * which means the controller took ownership and then failed to apply it. Read
 * positives-first, that object is healthy; it is not.
 */

/**
 * Conditions that describe whether the policy took effect. `Accepted` is
 * GEP-713's own verdict; `Programmed` and `ResolvedRefs` are how Gateway API
 * and its implementations report that a policy the controller took on could
 * not actually be applied, and `Attached` is GKE's equivalent.
 */
const EFFECT_CONDITIONS = ['Accepted', 'Attached', 'Programmed', 'ResolvedRefs'] as const

/**
 * The subset that is a verdict in its own right, so True means the policy took
 * effect. `ResolvedRefs` is deliberately absent: refs resolving is a
 * precondition, not a statement that the policy applies. Kept separate from
 * EFFECT_CONDITIONS because a condition can be conclusive when False and say
 * nothing when True — GKE reports a healthy policy as `Attached=True` with no
 * `Accepted` at all, so checking only `Accepted` read it as pending.
 */
const VERDICT_CONDITIONS = ['Accepted', 'Attached', 'Programmed'] as const

/**
 * Reasons that mean "not settled yet" rather than "failed". GEP-713 lists
 * Reconciling as a legitimate state for a policy on its way to being applied,
 * and rendering ordinary convergence as a red failure would train operators to
 * ignore the colour.
 */
const TRANSIENT_REASONS = new Set(['Reconciling', 'Pending'])

/** A True condition can still be qualified: partial application is not success. */
const QUALIFIED_SUCCESS_REASONS = new Set(['PartiallyProgrammed'])

/** Conditions that mean trouble when True, mirroring the generic ladder's set. */
const NEGATIVE_CONDITIONS = new Set(['Degraded', 'Warning'])

/**
 * GEP-713 keys an ancestor's status on the full ancestorRef plus the
 * controllerName — the same Gateway can appear once per controller, section,
 * or kind. The label carries the short parts outright; the controllerName is
 * long, so it is appended only when two labels would otherwise collide.
 */
function ancestorLabel(ancestor: any): string {
  const ref = ancestor?.ancestorRef
  const name = typeof ref?.name === 'string' ? ref.name : ''
  if (!name) return ''
  const ns = typeof ref?.namespace === 'string' ? ref.namespace : ''
  const kind = typeof ref?.kind === 'string' ? ref.kind : ''
  const section = typeof ref?.sectionName === 'string' ? ref.sectionName : ''
  let label = ns ? `${ns}/${name}` : name
  if (kind && kind !== 'Gateway') label = `${kind} ${label}`
  if (section) label += `:${section}`
  return label
}

/**
 * Reasons are usually short CamelCase tokens, but the API allows 1024 chars
 * and this reader accepts any CRD with an ancestors array, so the list is
 * capped rather than trusted to stay small.
 */
const MAX_FAILURE_DETAILS = 4

function renderFailureDetails(failures: { label: string; controller: string; problem: string }[]): string {
  const labelCounts = new Map<string, number>()
  for (const f of failures) labelCounts.set(f.label, (labelCounts.get(f.label) ?? 0) + 1)
  const entries = failures.map(f => {
    let label = f.label
    if (label && (labelCounts.get(label) ?? 0) > 1 && f.controller) label += ` (${f.controller})`
    return label ? `${label}: ${f.problem}` : f.problem
  })
  const shown = entries.slice(0, MAX_FAILURE_DETAILS)
  const hidden = entries.length - shown.length
  return hidden > 0 ? `${shown.join('; ')}; +${hidden} more` : shown.join('; ')
}

function conditionsOf(ancestor: any): any[] {
  return Array.isArray(ancestor?.conditions) ? ancestor.conditions : []
}

/**
 * The controller's reason where it gave one — it names the actual failure
 * (ResourceNotFound, Conflicted, NotAllowed) where the type does not.
 *
 * The fallback has to follow the condition's polarity. `Accepted=False` reads
 * as "Not Accepted", but `Warning=True` means there *is* a warning, so the same
 * construction would render "Not Warning" and inverts the meaning.
 *
 * Spelled with a space where the Go reader behind MCP writes "NotAccepted",
 * matching each side's existing convention. Whenever a controller supplies a
 * reason the two sides are identical; they part ways only here and on mixed
 * failures, where the per-ancestor detail goes to the tooltip while MCP,
 * having no tooltip, carries it in the text.
 */
function problemText(cond: any, trueMeansTrouble = false): string {
  const reason = typeof cond?.reason === 'string' ? cond.reason.trim() : ''
  if (reason) return reason
  const type = typeof cond?.type === 'string' ? cond.type.trim() : ''
  if (!type) return 'Failed'
  return trueMeansTrouble ? type : `Not ${type}`
}

/**
 * Status for one policy, aggregated across the Gateways it attaches to.
 *
 * A policy may attach to several ancestors with different outcomes, so a
 * failure anywhere wins and the count says how widespread it is — collapsing to
 * a bare "Accepted" would hide the Gateway that rejected it.
 */
export function getGatewayPolicyStatus(resource: any): GenericStatus | null {
  const ancestors = resource?.status?.ancestors
  if (!Array.isArray(ancestors)) return null

  // Null rather than a verdict, so the caller can fall back to any top-level
  // conditions the object also carries. A policy that attaches to nothing and
  // says nothing else is left to render as absent, because "not attached" and
  // "not reconciled yet" are indistinguishable from here.
  if (ancestors.length === 0) return null

  let failed = 0
  let degraded = 0
  let accepted = 0
  // Tracked apart, not in one slot: a warning on the first ancestor and a
  // failure on the second would otherwise show the warning's text alongside
  // the failure's count and tone, reading as though the warning was the
  // failure.
  let failure: { text: string; reason?: string } | null = null
  let warning: { text: string; reason?: string } | null = null
  // Ancestors can fail for different reasons. Reporting the first one with a
  // count claims they all failed that way.
  const failureReasons = new Set<string>()
  // Which Gateway failed with what, for the tooltip: "2/3 failed" without it
  // sends the operator digging through raw status for the names.
  const failureDetails: { label: string; controller: string; problem: string }[] = []
  let acceptedAs: string | null = null

  for (const ancestor of ancestors) {
    const conditions = conditionsOf(ancestor)

    const broken = conditions.find(
      (c: any) => EFFECT_CONDITIONS.includes(c?.type) && c?.status === 'False',
    )
    if (broken) {
      const reason = typeof broken?.reason === 'string' ? broken.reason.trim() : ''
      // A controller still working towards the desired state is not a failure.
      if (TRANSIENT_REASONS.has(reason)) {
        degraded++
        warning ??= { text: reason, reason: broken?.message }
        continue
      }
      failed++
      failure ??= { text: problemText(broken), reason: broken?.message }
      failureReasons.add(problemText(broken))
      failureDetails.push({
        label: ancestorLabel(ancestor),
        controller: typeof ancestor?.controllerName === 'string' ? ancestor.controllerName : '',
        problem: problemText(broken),
      })
      continue
    }

    const warned = conditions.find((c: any) => NEGATIVE_CONDITIONS.has(c?.type) && c?.status === 'True')
    if (warned) {
      degraded++
      warning ??= { text: problemText(warned, true), reason: warned?.message }
      continue
    }

    // A qualified success is not success: PartiallyProgrammed means some of the
    // policy landed and some did not.
    const qualified = conditions.find(
      (c: any) => EFFECT_CONDITIONS.includes(c?.type) && c?.status === 'True' &&
        QUALIFIED_SUCCESS_REASONS.has(typeof c?.reason === 'string' ? c.reason.trim() : ''),
    )
    if (qualified) {
      degraded++
      warning ??= { text: qualified.reason.trim(), reason: qualified?.message }
      continue
    }

    const verdict = conditions.find(
      (c: any) => VERDICT_CONDITIONS.includes(c?.type) && c?.status === 'True',
    )
    if (verdict) {
      accepted++
      // Named after the condition that granted it, so a GKE policy reads
      // "Attached" where an Envoy one reads "Accepted".
      acceptedAs ??= verdict.type
    }
  }

  const total = ancestors.length
  const scope = (n: number) => (total > 1 ? ` (${n}/${total})` : '')

  if (failed > 0 && failure) {
    if (failureReasons.size > 1) {
      return { text: `${failed}/${total} failed`, tone: 'unhealthy', reason: renderFailureDetails(failureDetails) }
    }
    return { text: `${failure.text}${scope(failed)}`, tone: 'unhealthy', reason: failure.reason }
  }
  if (degraded > 0 && warning) {
    return { text: `${warning.text}${scope(degraded)}`, tone: 'degraded', reason: warning.reason }
  }
  if (accepted === total) return { text: acceptedAs ?? 'Accepted', tone: 'healthy' }

  // Some ancestor published neither an outcome nor a failure — the controller
  // has seen the policy but not finished with it.
  return { text: `Pending${scope(total - accepted)}`, tone: 'degraded' }
}
