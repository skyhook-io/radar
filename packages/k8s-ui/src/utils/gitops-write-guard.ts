// Classifies a direct write to a live object against the GitOps (or Helm)
// source that manages it: will the owner put the old value back, might it, or
// does a policy exclude the field?
//
// Evidence comes from the server (POST /api/gitops/write-evidence), because
// the facts that matter — the last client-side apply payload and
// managedFields — are stripped from every cached object Radar serves. Neither
// is proof of what Git declares today: last-applied is a PREVIOUS apply, and a
// field missing from both can still be in the source. So absence of evidence
// is "unknown" (may be overwritten), never "safe".

import { activeManagerClaims, gitOpsOwnerFromRef, managerCandidates, managerRefLabel, type GitOpsOwnerRef, type GitOpsOwnerTool } from './gitops-owner'
import type { ManagerAnswer, ResourceRef } from '../types/core'
import { dedupeResourceRefs } from './resource-refs'

export type GitOpsWriteScope = 'status' | 'metadata' | 'spec' | 'create-child' | 'delete'

export interface GitOpsWrite {
  scope: GitOpsWriteScope
  /** Field paths, e.g. `spec.suspend`, `metadata.annotations["cnpg.io/hibernation"]`,
   *  `spec.template.spec.containers[name=app].image`, `spec.template.spec.containers[*].image`.
   *  Omitted = the fields aren't known. */
  paths?: string[]
  description?: string
}

export interface GitOpsWriteTarget {
  /** Singular Kind, e.g. `Deployment`. */
  kind: string
  /** API group; `''` for core. */
  group: string
  namespace: string
  name: string
}

export type GitOpsWriteLevel = 'none' | 'info' | 'may-revert' | 'will-revert'

export interface GitOpsSyncPolicy {
  tool: GitOpsOwnerTool
  auto: boolean | null
  selfHeal: boolean | null
  prune: boolean | null
  suspended: boolean | null
  interval?: string
}

export interface GitOpsWriteHelmRelease {
  namespace: string
  name: string
}

export interface GitOpsWriteOperatorOwner {
  kind: string
  name: string
}

// --- Wire types of POST /api/gitops/write-evidence -------------------------

export interface GitOpsFieldOwnerEvidence {
  manager: string
  operation: string
  subresource?: string
  tool?: 'argocd' | 'fluxcd' | 'helm'
  approximate?: boolean
}

export interface GitOpsPathEvidence {
  path: string
  error?: string
  lastApplied: 'present' | 'absent' | 'no-annotation'
  ownedBy: GitOpsFieldOwnerEvidence[]
  /** A manager of the owner's controller owns the field. */
  ownedByGitOps: boolean
  approximate?: boolean
  /** The path has a wildcard, and the source declares the field on some of
   *  the entries it matches (one container's image but not another's). */
  partial?: boolean
  /** The wildcard matches nothing in the live object (no init containers),
   *  so the write can't touch this path. */
  empty?: boolean
  /** `effective`: the owner is not expected to overwrite it. `comparison-only`: Argo
   *  ignoreDifferences without RespectIgnoreDifferences. `unevaluated`: a
   *  matching rule uses jqPathExpressions, which Radar doesn't evaluate, or
   *  covers only some of the entries a wildcard path matches. */
  ignored?: 'effective' | 'comparison-only' | 'unevaluated'
  ignoredBy?: string
}

export interface GitOpsWritePolicyEvidence {
  tool: GitOpsOwnerTool
  auto: boolean | null
  selfHeal: boolean | null
  prune: boolean | null
  suspended: boolean | null
  interval?: string
  respectIgnoreDifferences?: boolean
  serverSideApply?: boolean
  replace?: boolean
  /** HelmRelease `spec.driftDetection.mode`. */
  driftDetection?: string
  objectReconcile?: 'ignore' | 'if-not-present'
}

export interface GitOpsWriteEvidence {
  manager?: ManagerAnswer
  /** Why the server couldn't answer what manages the live object; the owner hint wasn't used either. */
  managerUnavailable?: string
  uid: string
  resourceVersion: string
  owner: { kind: string; group?: string; namespace: string; name: string } | null
  policy: GitOpsWritePolicyEvidence | null
  policyError?: string
  controllerOwner?: { apiVersion: string; kind: string; name: string }
  /** The Argo CD Application's status lists the object among its resources. */
  ownerTracksTarget?: boolean
  paths: GitOpsPathEvidence[]
}

// ---------------------------------------------------------------------------

export interface GitOpsWriteGuardInput {
  manager?: ManagerAnswer
  target: GitOpsWriteTarget
  owner: GitOpsOwnerRef | null
  /** Native Helm release that manages the object when no GitOps tool does. */
  helmRelease?: GitOpsWriteHelmRelease | null
  /** A non-GitOps controller (e.g. a CNPG Cluster owning a Pod). Defaults to
   *  the evidence's controllerOwner. Only affects `delete`. */
  operatorOwner?: GitOpsWriteOperatorOwner | null
  /** Ownership or evidence is still loading. */
  ownerPending?: boolean
  /** The host could not determine ownership: one sentence saying why, shown as the reason. */
  ownershipError?: string | null
  /** The owner was matched by name only (an Argo CD tracking label without a
   *  namespace), so its policy can't waive an acknowledgment. */
  ownerMatchedByName?: boolean
  evidence?: GitOpsWriteEvidence | null
  /** The evidence request failed. */
  evidenceError?: string | null
  writes: GitOpsWrite[]
}

export interface GitOpsWriteAssessment {
  write: GitOpsWrite
  level: GitOpsWriteLevel
  reason: string
}

export interface GitOpsWriteGuard {
  manager?: ManagerAnswer
  owner: GitOpsOwnerRef | null
  helmRelease: GitOpsWriteHelmRelease | null
  pending: boolean
  level: GitOpsWriteLevel
  perWrite: GitOpsWriteAssessment[]
  summary: string
  syncPolicy: GitOpsSyncPolicy | null
  requiresAck: boolean
  ownershipError: string | null
}

const LEVEL_RANK: Record<GitOpsWriteLevel, number> = {
  none: 0,
  info: 1,
  'may-revert': 2,
  'will-revert': 3,
}

function maxLevel(a: GitOpsWriteLevel, b: GitOpsWriteLevel): GitOpsWriteLevel {
  return LEVEL_RANK[b] > LEVEL_RANK[a] ? b : a
}

export function gitOpsToolLabel(tool: GitOpsOwnerTool | 'helm'): string {
  if (tool === 'argocd') return 'Argo CD'
  if (tool === 'fluxcd') return 'Flux'
  return 'Helm'
}

const OWNER_KIND_LABEL: Record<GitOpsOwnerRef['kind'], string> = {
  applications: 'Application',
  kustomizations: 'Kustomization',
  helmreleases: 'HelmRelease',
}

/** `Argo CD Application argocd/api` */
export function describeGitOpsOwner(owner: GitOpsOwnerRef): string {
  const ref = owner.namespace ? `${owner.namespace}/${owner.name}` : owner.name
  return `${gitOpsToolLabel(owner.tool)} ${OWNER_KIND_LABEL[owner.kind]} ${ref}`
}

/** `the Application`: the owner as reasons name it, after a headline that already named it in full. */
function ownerKindRef(owner: GitOpsOwnerRef): string {
  return `the ${OWNER_KIND_LABEL[owner.kind]}`
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** The tool named in acknowledgments: `Argo CD`, `Flux`, `Helm`. */
export function guardToolLabel(guard: Pick<GitOpsWriteGuard, 'owner' | 'helmRelease'>): string {
  if (guard.owner) return gitOpsToolLabel(guard.owner.tool)
  if (guard.helmRelease) return 'Helm'
  return 'a GitOps tool'
}

/** Identifies what an acknowledgment was given for; it changes when the
 *  verdict, owner, reasons or the fields being written do. */
export function gitOpsWriteGuardKey(guard: GitOpsWriteGuard | undefined): string {
  if (!guard || guard.pending) return ''
  const owner = guard.owner ? `${guard.owner.kind}/${guard.owner.namespace ?? ''}/${guard.owner.name}` : guard.helmRelease ? `helm/${guard.helmRelease.namespace}/${guard.helmRelease.name}` : ''
  return [guard.level, owner, guard.ownershipError ?? '', ...guard.perWrite.map((w) => `${w.write.scope}:${(w.write.paths ?? []).join(',')}:${w.level}:${w.reason}`)].join('|')
}

export function canConfirmGitOpsWrite(guard: GitOpsWriteGuard | undefined, acked: boolean): boolean {
  if (!guard || guard.pending) return false
  return !guard.requiresAck || acked
}

export function gitOpsSyncPolicy(policy: GitOpsWritePolicyEvidence | null | undefined): GitOpsSyncPolicy | null {
  if (!policy) return null
  return {
    tool: policy.tool,
    auto: policy.auto,
    selfHeal: policy.selfHeal,
    prune: policy.prune,
    suspended: policy.suspended,
    ...(policy.interval ? { interval: policy.interval } : {}),
  }
}

/** The field paths to request evidence for. */
export function gitOpsWriteEvidencePaths(writes: GitOpsWrite[]): string[] {
  const paths = new Set<string>()
  for (const write of writes) {
    if (write.scope === 'metadata' || write.scope === 'spec') {
      for (const path of write.paths ?? []) paths.add(path)
    }
  }
  return [...paths]
}

// ---------------------------------------------------------------------------
// Classification
// ---------------------------------------------------------------------------

type Verdict = { level: GitOpsWriteLevel; reason: string }

interface Context {
  input: GitOpsWriteGuardInput
  owner: GitOpsOwnerRef
  policy: GitOpsWritePolicyEvidence | null
  policyUnknown: string
}

function nextSync(ctx: Context): string {
  if (ctx.owner.tool === 'argocd') return 'the next sync'
  const every = ctx.policy?.interval ? ` (every ${ctx.policy.interval})` : ''
  return `the next reconcile${every}`
}

// What happens to a field the source sets, under the owner's policy.
function declaredOutcome(ctx: Context): Verdict {
  const { owner, policy } = ctx
  if (!policy) {
    return {
      level: 'may-revert',
      reason: `Radar couldn't read ${ownerKindRef(owner)}'s sync policy (${ctx.policyUnknown}); the next sync will overwrite it.`,
    }
  }
  if (policy.suspended) {
    return { level: 'may-revert', reason: `${capitalize(ownerKindRef(owner))} is suspended; resuming it will overwrite this change.` }
  }
  if (policy.selfHeal) {
    return { level: 'will-revert', reason: `It will be overwritten at ${nextSync(ctx)}.` }
  }
  if (owner.kind === 'helmreleases') {
    return {
      level: 'may-revert',
      reason: `Drift detection is ${policy.driftDetection || 'disabled'}, so it stays until the next Helm upgrade by Flux overwrites it.`,
    }
  }
  if (owner.tool === 'argocd' && policy.auto) {
    return { level: 'may-revert', reason: 'Self-heal is off; the next sync will overwrite it.' }
  }
  return { level: 'may-revert', reason: 'Auto-sync is off; the next sync will overwrite it.' }
}

function evidenceNote(ev: GitOpsPathEvidence, owner: GitOpsOwnerRef): string {
  const manager = ev.ownedBy.find((o) => o.tool === owner.tool && !o.subresource)?.manager
  if (ev.ownedByGitOps && manager) return `${manager} ${ev.approximate ? 'manages' : 'owns'} this field`
  return 'the last applied configuration sets this field'
}

function classifyPath(ctx: Context, path: string | null): Verdict {
  const { owner, policy } = ctx
  const tool = gitOpsToolLabel(owner.tool)
  const ev = path ? ctx.input.evidence?.paths.find((p) => p.path === path) : undefined
  if (ev?.empty) return { level: 'none', reason: '' }

  // Helm's drift exemptions only stop drift correction: the next upgrade's
  // three-way merge still resets a field the chart renders.
  const helm = owner.kind === 'helmreleases'
  const helmExemption = helm
    ? policy?.objectReconcile === 'ignore'
      ? 'the object opts out of drift detection'
      : ev?.ignored === 'effective'
        ? `drift detection ignores this field (${ev.ignoredBy ?? 'ignore rule'})`
        : null
    : null
  // A change whose fields are unknown may be the one that removes the opt-out.
  if (!helm && path && policy?.objectReconcile === 'ignore') {
    return { level: 'info', reason: `This object opts out of ${tool} reconciliation, so a sync is not expected to overwrite it.` }
  }
  // Flux reads IfNotPresent from the desired manifest; the live annotation
  // may have been added by hand or outlived a change in the source.
  if (policy?.objectReconcile === 'if-not-present') {
    return {
      level: 'may-revert',
      reason: `This object carries ${tool}'s IfNotPresent annotation, but Radar can't confirm the GitOps source declares it; if it doesn't, the next reconcile overwrites this change.`,
    }
  }
  if (!helm && ev?.ignored === 'effective') {
    return {
      level: 'info',
      reason: `${capitalize(ownerKindRef(owner))} ignores this field (${ev.ignoredBy ?? 'ignore rule'}), so a sync is not expected to overwrite it.`,
    }
  }

  const declared = Boolean(ev && !ev.error && (ev.lastApplied === 'present' || ev.ownedByGitOps))
  if (declared || policy?.replace) {
    if (ev?.ignored === 'unevaluated') {
      return {
        level: 'may-revert',
        reason: `Radar can't confirm ${ownerKindRef(owner)}'s ignore rule (${ev.ignoredBy ?? 'spec.ignoreDifferences'}) covers this field, so the next sync may overwrite it.`,
      }
    }
    if (ev?.ignored === 'comparison-only') {
      return {
        level: 'may-revert',
        reason: `${capitalize(ownerKindRef(owner))} excludes this field from comparison (${ev.ignoredBy ?? 'ignoreDifferences'}) without RespectIgnoreDifferences, so self-heal won't react, but the next sync will overwrite it.`,
      }
    }
    const outcome = helmExemption
      ? { level: 'may-revert' as const, reason: `Flux won't correct it as drift because ${helmExemption}, but the next Helm upgrade overwrites it.` }
      : declaredOutcome(ctx)
    // A wildcard the source only partly declares: which entries revert
    // depends on which one the user changes.
    if (ev?.partial && !policy?.replace && outcome.level === 'will-revert') {
      return {
        level: 'may-revert',
        reason: `The GitOps source sets this for some containers but not others; the ones it sets will be overwritten at ${nextSync(ctx)}.`,
      }
    }
    const why =
      declared && ev
        ? `The GitOps source sets this field (${evidenceNote(ev, owner)}).`
        : `${capitalize(ownerKindRef(owner))} syncs with Replace=true, which replaces the whole object.`
    return { level: outcome.level, reason: `${why} ${outcome.reason}` }
  }

  const unknown = 'It may be overwritten on sync if the GitOps source declares this field.'
  if (!path) {
    return { level: 'may-revert', reason: `Radar doesn't know which fields this change touches. ${unknown}` }
  }
  if (!ev || ev.error) {
    const cause = ev?.error || ctx.input.evidenceError
    return {
      level: 'may-revert',
      reason: `Radar can't see which fields the GitOps source declares${cause ? ` (${cause})` : ''}. ${unknown}`,
    }
  }
  return {
    level: 'may-revert',
    reason: `Radar found no record of the GitOps source setting this field, but that doesn't rule it out. ${unknown}`,
  }
}

function classifyFieldWrite(ctx: Context, write: GitOpsWrite): Verdict {
  const paths: Array<string | null> = write.paths?.length ? write.paths : [null]
  let worst = classifyPath(ctx, paths[0])
  for (const path of paths.slice(1)) {
    const verdict = classifyPath(ctx, path)
    if (LEVEL_RANK[verdict.level] > LEVEL_RANK[worst.level]) worst = verdict
  }
  return worst
}

function classifyDelete(ctx: Context): Verdict {
  const { owner, policy } = ctx
  const tool = gitOpsToolLabel(owner.tool)
  if (!policy) {
    return {
      level: 'may-revert',
      reason: `Radar couldn't read ${ownerKindRef(owner)}'s sync policy (${ctx.policyUnknown}); the next sync will recreate it.`,
    }
  }
  if (policy.objectReconcile === 'ignore') {
    if (owner.kind === 'helmreleases') {
      return { level: 'may-revert', reason: "Flux drift detection skips this object, but the next Helm upgrade recreates it." }
    }
    return { level: 'info', reason: `This object opts out of ${tool} reconciliation, so it is not expected to be recreated.` }
  }
  if (policy.suspended) {
    return { level: 'may-revert', reason: `${capitalize(ownerKindRef(owner))} is suspended; resuming it will recreate it.` }
  }
  if (policy.selfHeal) {
    return { level: 'will-revert', reason: `${tool} will recreate it at ${nextSync(ctx)}.` }
  }
  return { level: 'may-revert', reason: 'The next sync will recreate it.' }
}

function operatorOwnerOf(input: GitOpsWriteGuardInput): GitOpsWriteOperatorOwner | null {
  if (input.operatorOwner !== undefined) return input.operatorOwner
  const controller = input.evidence?.controllerOwner
  return controller ? { kind: controller.kind, name: controller.name } : null
}

function classifyWrite(input: GitOpsWriteGuardInput, owner: GitOpsOwnerRef | null, write: GitOpsWrite): Verdict {
  if (write.scope === 'status') {
    return { level: 'none', reason: 'This writes controller status; ordinary GitOps sync does not manage these fields.' }
  }
  const manager = input.manager
  if (manager) {
    const candidates = managerCandidates(manager)
    // A Helm release's annotations on an object answered through an ancestor
    // are its Deployment's, copied onto it.
    const directClaims = activeManagerClaims(manager.direct).filter(c => !manager.via || c.source !== 'helm-release')
    const names = dedupeResourceRefs([...candidates, ...(manager.via ? directClaims.map(c => c.manager) : [])]).map(managerRefLabel).join(' and ')
    if (write.scope === 'create-child' && candidates.length) {
      return { level: 'info', reason: `A new object isn't in the GitOps source, so ${names} is not expected to revert it.` }
    }
    if (write.scope === 'metadata' || write.scope === 'spec') {
      if (write.paths?.length && write.paths.every(path => input.evidence?.paths.find(p => p.path === path)?.empty)) {
        return { level: 'none', reason: '' }
      }
      if (chainUnread(manager)) {
        return { level: 'may-revert', reason: input.ownershipError! }
      }
    }
    // A walk that couldn't read its chain may have missed a manager above;
    // a controller that recreates the object makes that moot.
    if (write.scope === 'delete' && chainUnread(manager) && !operatorOwnerOf(input)) {
      return { level: 'may-revert', reason: input.ownershipError! }
    }
    if (manager.via && !directClaims.length) {
      const source = managerRefLabel(manager.via.object)
      const parent = `${source}, ${parentManagedBy(manager, names)}`
      return {
        level: 'info',
        reason: write.scope === 'delete'
          ? !NON_RECREATING_CONTROLLERS.includes(operatorOwnerOf(input)?.kind ?? '')
            ? `Its controller recreates it from ${parent}.`
            : `It was created from ${parent}; nothing is expected to recreate it, but ${operatorOwnerOf(input)!.kind} ${input.target.namespace ? `${input.target.namespace}/` : ''}${operatorOwnerOf(input)!.name} can still create new ones on its schedule.`
          : `This resource is generated from ${parent}; changes to the parent may replace it.`,
      }
    }
    if ((write.scope === 'metadata' || write.scope === 'spec') &&
        (manager.state === 'disagree' || manager.state === 'shared' || candidates.length > 1 || manager.via && directClaims.length > 0)) {
      return { level: 'may-revert', reason: `${names} may overwrite these fields. A single manager's ignore rules or sync policy cannot exempt this change.${manager.via ? ` The controller also generates it from ${managerRefLabel(manager.via.object)}.` : ''}` }
    }
    if (unconfirmed(manager, owner, input, candidates) && !targetOptedOut(input) && (write.scope === 'metadata' || write.scope === 'spec')) {
      return { level: 'may-revert', reason: `This resource's metadata names ${names}, but its recorded membership isn't confirmed. A sync may overwrite this change.` }
    }
  }
  if (write.scope === 'delete') {
    const operator = operatorOwnerOf(input)
    if (operator) {
      return {
        level: 'info',
        reason: !NON_RECREATING_CONTROLLERS.includes(operator.kind)
          ? `${operator.kind} ${operator.name} will recreate this ${input.target.kind}.`
          : `${operator.kind} ${operator.name} created this ${input.target.kind}; it is not expected to recreate it.`,
      }
    }
  }
  if (manager && write.scope === 'delete') {
    const candidates = managerCandidates(manager)
    if (chainUnread(manager)) return { level: 'may-revert', reason: input.ownershipError! }
    if (unconfirmed(manager, owner, input, candidates) && !targetOptedOut(input)) {
      return { level: 'may-revert', reason: `This resource's metadata names ${candidates.map(managerRefLabel).join(' and ')}, but its recorded membership isn't confirmed. A sync may recreate it.` }
    }
    if (manager.state === 'disagree' || manager.state === 'shared' || candidates.length > 1) {
      return { level: 'may-revert', reason: `${candidates.map(managerRefLabel).join(' and ')} may recreate this resource on sync.` }
    }
  }
  if (owner) {
    const tool = gitOpsToolLabel(owner.tool)
    if (write.scope === 'create-child') {
      return { level: 'info', reason: `A new object isn't in the GitOps source, so ${tool} is not expected to revert it.` }
    }
    const ctx: Context = {
      input,
      owner,
      policy: input.evidence?.policy ?? null,
      policyUnknown: input.evidence?.policyError || input.evidenceError || 'unavailable',
    }
    return write.scope === 'delete' ? classifyDelete(ctx) : classifyFieldWrite(ctx, write)
  }
  if (input.helmRelease) {
    if (write.scope === 'create-child') {
      return { level: 'info', reason: "A new object isn't part of the release, so a Helm upgrade is not expected to revert it." }
    }
    if (write.scope === 'delete') {
      return { level: 'may-revert', reason: 'The next Helm upgrade or rollback will recreate it.' }
    }
    const helmOwned = (write.paths ?? []).some((path) =>
      input.evidence?.paths.find((p) => p.path === path)?.ownedBy.some((o) => o.tool === 'helm'),
    )
    return {
      level: 'may-revert',
      reason: helmOwned
        ? 'Helm set this field; the next Helm upgrade or rollback will overwrite it.'
        : 'If the chart sets this field, the next Helm upgrade or rollback will overwrite it.',
    }
  }
  if (input.ownershipError && write.scope !== 'create-child') {
    return {
      level: 'may-revert',
      reason: input.ownershipError,
    }
  }
  return { level: 'none', reason: '' }
}

function headline(guard: Pick<GitOpsWriteGuard, 'owner' | 'helmRelease' | 'manager'>, level: GitOpsWriteLevel): string {
  if (guard.manager?.via && level === 'info') return `Generated from ${managerRefLabel(guard.manager.via.object)}.`
  const who = guard.owner
    ? describeGitOpsOwner(guard.owner)
    : guard.helmRelease
      ? `Helm release ${guard.helmRelease.namespace}/${guard.helmRelease.name}`
      : 'A GitOps tool'
  if (level === 'will-revert') return `${who} will revert this change.`
  if (level === 'may-revert') return `${who} may revert this change.`
  // A list alone isn't management: say what the chip says.
  if (level === 'info') return guard.owner || guard.helmRelease ? `${guard.manager?.state === 'listed' ? 'Listed by' : 'Managed by'} ${who}.` : ''
  return ''
}

export function gitOpsWriteHeadline(guard: GitOpsWriteGuard): string {
  return headline(guard, guard.level)
}

// unconfirmed reports whether the answer names candidates without confirming
// one Radar can act on: a claim, or a candidate this UI can't open (no lead,
// or a manager kind it doesn't know). A native Helm lead has its own rules.
function unconfirmed(manager: ManagerAnswer, owner: GitOpsOwnerRef | null, input: GitOpsWriteGuardInput, candidates: ResourceRef[]): boolean {
  if (!candidates.length || !owner && input.helmRelease) return false
  return manager.state === 'claimed' || !owner && manager.state !== 'none' && manager.state !== 'unknown'
}

// parentManagedBy says what the evidence says of a parent, in the words its
// state allows: only a list and metadata agreeing say a manager manages it.
function parentManagedBy(manager: ManagerAnswer, names: string): string {
  const evidence = manager.via ?? manager.direct
  const label = (refs: ResourceRef[]) => dedupeResourceRefs(refs).map(managerRefLabel).join(' and ')
  const listing = (refs: ResourceRef[]) => `${label(refs)} ${refs.length > 1 ? 'list it in their statuses' : 'lists it in its status'}`
  switch (manager.state) {
    case 'disagree':
      return `whose metadata names ${label(activeManagerClaims(evidence).map(c => c.manager))}, while ${listing(evidence.recorders ?? [])}`
    case 'agree':
      return `which ${names} manages`
    case 'listed':
    case 'shared':
      return `which ${names} ${managerCandidates(manager).length > 1 ? 'list' : 'lists'} in ${managerCandidates(manager).length > 1 ? 'their statuses' : 'its status'}`
    default:
      return `whose metadata names ${names}`
  }
}

// adoptedHelmRelease: a native Helm release's annotations on an object a GitOps
// manager other than Flux's HelmRelease answers for. Argo CD renders charts
// with helm template, and a Kustomization applies plain manifests, so the
// annotations mean a Helm install was adopted: its release can still upgrade
// or roll back the object, whatever the GitOps manager ignores.
function adoptedHelmRelease(manager: ManagerAnswer | undefined): string | null {
  if (!manager?.lead || manager.via) return null
  if (manager.lead.kind === 'HelmRelease') return null
  const claim = (manager.direct.claims ?? []).find(c => c.source === 'helm-release' && !c.copied && !c.weak)
  return claim ? `${claim.manager.namespace ? `${claim.manager.namespace}/` : ''}${claim.manager.name}` : null
}

// targetOptedOut: the target's own annotations opt it out of its manager's
// reconciliation, which holds whether or not that manager's list names it.
function targetOptedOut(input: GitOpsWriteGuardInput): boolean {
  return input.evidence?.policy?.objectReconcile === 'ignore'
}

// chainUnread: the walk up the controller chain stopped at an owner Radar
// couldn't read or decide, so a manager above it may be missed. An owner that is
// gone or was replaced ends the chain with an answer.
function chainUnread(manager: ManagerAnswer): boolean {
  // A chain end this UI doesn't know (from a newer Radar) stopped short too.
  if (manager.chainEnd && manager.chainEnd !== 'unfollowed') return true
  return Boolean(manager.stop && !['gone', 'replaced', 'excluded'].includes(manager.stop.state))
}

// The states this UI knows. A newer Radar may send another; it is read as
// claimed, which never proves a revert or waives a warning.
const MANAGER_STATES: Array<string> = ['shared', 'disagree', 'agree', 'listed', 'claimed', 'unknown', 'none']

// Controllers that don't replace an object of theirs that is deleted: a
// CronJob's Job (scheduled or created by hand) runs once.
const NON_RECREATING_CONTROLLERS = ['CronJob']

export function evaluateGitOpsWriteGuard(input: GitOpsWriteGuardInput): GitOpsWriteGuard {
  // A live check the server couldn't answer, or one that failed while the
  // cached answer names a manager, leaves that answer unconfirmed: an exemption
  // read from it could belong to the object as it was.
  const cachedNamesManager = Boolean(input.manager && (input.manager.via || !['none', 'unknown'].includes(input.manager.state)))
  const unconfirmed = input.evidence?.managerUnavailable || (!input.evidence && input.evidenceError && cachedNamesManager ? input.evidenceError : null)
  const answered = unconfirmed ? undefined : input.evidence?.manager ?? input.manager
  const manager = answered && !MANAGER_STATES.includes(answered.state) ? { ...answered, state: 'claimed' as const } : answered
  const owner = unconfirmed ? null : manager ? manager.lead ? gitOpsOwnerFromRef(manager.lead) : null : input.owner ?? null
  const helmRelease = unconfirmed ? null : manager
    ? manager.lead?.kind === 'HelmRelease' && !manager.lead.group ? { namespace: manager.lead.namespace, name: manager.lead.name } : null
    : owner ? null : (input.helmRelease ?? null)
  const ownershipError = unconfirmed
    ? `Radar couldn't check what manages this resource now (${unconfirmed}), so it can't tell whether a GitOps tool or Helm reverts this change.`
    : manager && chainUnread(manager)
      ? "Radar couldn't follow this resource's controller chain, so it can't tell whether a GitOps tool or Helm manages it."
      : owner || helmRelease ? null : (input.ownershipError ?? null)
  const normalized: GitOpsWriteGuardInput = { ...input, manager, owner, helmRelease, ownershipError, ownerMatchedByName: manager ? false : input.ownerMatchedByName }
  const adopted = adoptedHelmRelease(manager)

  const perWrite = input.writes.map((write) => {
    const verdict = classifyWrite(normalized, owner, write)
    // A list alone doesn't show its manager syncs the object (a copy carrying
    // another object's tracking ID, a CRD a HelmRelease skips): no promise,
    // in the verdict or in its reason.
    if (manager?.state === 'listed' && !manager.via && (verdict.level === 'will-revert' || verdict.level === 'may-revert')) {
      return {
        write,
        level: 'may-revert' as const,
        reason: `Radar can't confirm ${managerRefLabel(manager.lead!)} applies this object: its status lists it, but the object's metadata doesn't name it. If it does: ${verdict.reason}`,
      }
    }
    if (adopted && verdict.level === 'info' && (write.scope === 'metadata' || write.scope === 'spec' || write.scope === 'delete')) {
      return {
        write,
        level: 'may-revert' as const,
        reason: `${verdict.reason} Its metadata also says Helm release ${adopted} installed it; the next helm upgrade or rollback of that release may ${write.scope === 'delete' ? 'recreate it' : 'overwrite this change'}.`,
      }
    }
    if (owner && normalized.ownerMatchedByName && verdict.level === 'info' && write.scope !== 'create-child') {
      return {
        write,
        level: 'may-revert' as const,
        reason: `${verdict.reason} Radar matched ${ownerKindRef(owner)} by name only, so it can't confirm this.`,
      }
    }
    return { write, ...verdict }
  })
  const level = perWrite.reduce<GitOpsWriteLevel>((max, entry) => maxLevel(max, entry.level), 'none')
  const top = perWrite.find((entry) => entry.level === level)
  const summary = level === 'none' ? '' : [headline({ owner, helmRelease, manager }, level), top?.reason ?? ''].filter(Boolean).join(' ')

  return {
    ...(manager ? { manager } : {}),
    owner,
    helmRelease,
    pending: Boolean(input.ownerPending),
    level,
    perWrite,
    summary,
    syncPolicy: owner && (!manager || manager.state !== 'claimed' && managerCandidates(manager).length === 1) ? gitOpsSyncPolicy(input.evidence?.policy) : null,
    requiresAck: level === 'will-revert' || level === 'may-revert',
    ownershipError,
  }
}
