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

import type { GitOpsOwnerRef, GitOpsOwnerTool } from './gitops-owner'

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
  /** `effective`: the owner is not expected to overwrite it. `comparison-only`: Argo
   *  ignoreDifferences without RespectIgnoreDifferences. `unevaluated`: a
   *  matching rule uses jqPathExpressions, which Radar doesn't evaluate. */
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
  uid: string
  resourceVersion: string
  owner: { kind: string; group?: string; namespace: string; name: string } | null
  policy: GitOpsWritePolicyEvidence | null
  policyError?: string
  controllerOwner?: { apiVersion: string; kind: string; name: string }
  paths: GitOpsPathEvidence[]
}

// ---------------------------------------------------------------------------

export interface GitOpsWriteGuardInput {
  target: GitOpsWriteTarget
  owner: GitOpsOwnerRef | null
  /** Native Helm release that manages the object when no GitOps tool does. */
  helmRelease?: GitOpsWriteHelmRelease | null
  /** A non-GitOps controller (e.g. a CNPG Cluster owning a Pod). Defaults to
   *  the evidence's controllerOwner. Only affects `delete`. */
  operatorOwner?: GitOpsWriteOperatorOwner | null
  /** Ownership or evidence is still loading. */
  ownerPending?: boolean
  /** The host could not determine ownership at all. */
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

/** The tool named in acknowledgments: `Argo CD`, `Flux`, `Helm`. */
export function guardToolLabel(guard: Pick<GitOpsWriteGuard, 'owner' | 'helmRelease'>): string {
  if (guard.owner) return gitOpsToolLabel(guard.owner.tool)
  if (guard.helmRelease) return 'Helm'
  return 'a GitOps tool'
}

/** Identifies what an acknowledgment was given for; it changes when the verdict, owner or reasons do. */
export function gitOpsWriteGuardKey(guard: GitOpsWriteGuard | undefined): string {
  if (!guard || guard.pending) return ''
  const owner = guard.owner ? `${guard.owner.kind}/${guard.owner.namespace ?? ''}/${guard.owner.name}` : guard.helmRelease ? `helm/${guard.helmRelease.namespace}/${guard.helmRelease.name}` : ''
  return [guard.level, owner, guard.ownershipError ?? '', ...guard.perWrite.map((w) => `${w.level}:${w.reason}`)].join('|')
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
      reason: `Radar couldn't read the sync policy of ${describeGitOpsOwner(owner)} (${ctx.policyUnknown}); the next sync will overwrite it.`,
    }
  }
  if (policy.suspended) {
    return { level: 'may-revert', reason: `${describeGitOpsOwner(owner)} is suspended; resuming it will overwrite this change.` }
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
  if (!helm && policy?.objectReconcile === 'ignore') {
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
      reason: `${describeGitOpsOwner(owner)} ignores this field (${ev.ignoredBy ?? 'ignore rule'}), so a sync is not expected to overwrite it.`,
    }
  }

  const declared = Boolean(ev && !ev.error && (ev.lastApplied === 'present' || ev.ownedByGitOps))
  if (declared || policy?.replace) {
    if (ev?.ignored === 'unevaluated') {
      return {
        level: 'may-revert',
        reason: `${describeGitOpsOwner(owner)} has an ignoreDifferences rule (${ev.ignoredBy ?? 'spec.ignoreDifferences'}) that Radar can't confirm covers this field, so it may or may not keep a sync from overwriting it.`,
      }
    }
    if (ev?.ignored === 'comparison-only') {
      return {
        level: 'may-revert',
        reason: `${describeGitOpsOwner(owner)} excludes this field from comparison (${ev.ignoredBy ?? 'ignoreDifferences'}) without RespectIgnoreDifferences, so self-heal won't react, but the next sync will overwrite it.`,
      }
    }
    const outcome = helmExemption
      ? { level: 'may-revert' as const, reason: `Flux won't correct it as drift because ${helmExemption}, but the next Helm upgrade overwrites it.` }
      : declaredOutcome(ctx)
    const why =
      declared && ev
        ? `The GitOps source sets this field (${evidenceNote(ev, owner)}).`
        : `${describeGitOpsOwner(owner)} syncs with Replace=true, which replaces the whole object.`
    return { level: outcome.level, reason: `${why} ${outcome.reason}` }
  }

  const unknown = 'It may be overwritten on sync if the GitOps source declares this field.'
  if (!path) {
    return { level: 'may-revert', reason: `Radar doesn't know which fields this change touches. ${unknown}` }
  }
  if (!ev || ev.error) {
    return { level: 'may-revert', reason: `Radar can't see which fields the GitOps source declares. ${unknown}` }
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
      reason: `Radar couldn't read the sync policy of ${describeGitOpsOwner(owner)} (${ctx.policyUnknown}); the next sync will recreate it.`,
    }
  }
  if (policy.objectReconcile === 'ignore') {
    if (owner.kind === 'helmreleases') {
      return { level: 'may-revert', reason: "Flux drift detection skips this object, but the next Helm upgrade recreates it." }
    }
    return { level: 'info', reason: `This object opts out of ${tool} reconciliation, so it is not expected to be recreated.` }
  }
  if (policy.suspended) {
    return { level: 'may-revert', reason: `${describeGitOpsOwner(owner)} is suspended; resuming it will recreate it.` }
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
  if (write.scope === 'delete') {
    const operator = operatorOwnerOf(input)
    if (operator) {
      return { level: 'info', reason: `${operator.kind} ${operator.name} will recreate this ${input.target.kind}.` }
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
    const release = `${input.helmRelease.namespace}/${input.helmRelease.name}`
    if (write.scope === 'create-child') {
      return { level: 'info', reason: `A new object isn't part of Helm release ${release}, so an upgrade is not expected to revert it.` }
    }
    if (write.scope === 'delete') {
      return { level: 'may-revert', reason: `The next helm upgrade or rollback of ${release} will recreate it.` }
    }
    const helmOwned = (write.paths ?? []).some((path) =>
      input.evidence?.paths.find((p) => p.path === path)?.ownedBy.some((o) => o.tool === 'helm'),
    )
    return {
      level: 'may-revert',
      reason: helmOwned
        ? `Helm release ${release} set this field; the next helm upgrade or rollback will overwrite it.`
        : `If the chart sets this field, the next helm upgrade or rollback of ${release} will overwrite it.`,
    }
  }
  if (input.ownershipError && write.scope !== 'create-child') {
    return {
      level: 'may-revert',
      reason: `Radar couldn't verify whether a GitOps tool manages this resource (${input.ownershipError}). If one does, it may revert this change.`,
    }
  }
  return { level: 'none', reason: '' }
}

function headline(guard: Pick<GitOpsWriteGuard, 'owner' | 'helmRelease'>, level: GitOpsWriteLevel): string {
  const who = guard.owner
    ? describeGitOpsOwner(guard.owner)
    : guard.helmRelease
      ? `Helm release ${guard.helmRelease.namespace}/${guard.helmRelease.name}`
      : 'A GitOps tool'
  if (level === 'will-revert') return `${who} will revert this change.`
  if (level === 'may-revert') return `${who} may revert this change.`
  if (level === 'info') return guard.owner || guard.helmRelease ? `Managed by ${who}.` : ''
  return ''
}

export function gitOpsWriteHeadline(guard: GitOpsWriteGuard): string {
  return headline(guard, guard.level)
}

export function evaluateGitOpsWriteGuard(input: GitOpsWriteGuardInput): GitOpsWriteGuard {
  const owner = input.owner ?? null
  const helmRelease = owner ? null : (input.helmRelease ?? null)
  const ownershipError = owner || helmRelease ? null : (input.ownershipError ?? null)
  const normalized: GitOpsWriteGuardInput = { ...input, owner, helmRelease, ownershipError }

  const perWrite = input.writes.map((write) => {
    const verdict = classifyWrite(normalized, owner, write)
    if (owner && input.ownerMatchedByName && verdict.level === 'info' && write.scope !== 'create-child') {
      return {
        write,
        level: 'may-revert' as const,
        reason: `${verdict.reason} Radar matched ${describeGitOpsOwner(owner)} by name only, so it can't confirm this.`,
      }
    }
    return { write, ...verdict }
  })
  const level = perWrite.reduce<GitOpsWriteLevel>((max, entry) => maxLevel(max, entry.level), 'none')
  const top = perWrite.find((entry) => entry.level === level)
  const summary = level === 'none' ? '' : [headline({ owner, helmRelease }, level), top?.reason ?? ''].filter(Boolean).join(' ')

  return {
    owner,
    helmRelease,
    pending: Boolean(input.ownerPending),
    level,
    perWrite,
    summary,
    syncPolicy: owner ? gitOpsSyncPolicy(input.evidence?.policy) : null,
    requiresAck: level === 'will-revert' || level === 'may-revert',
    ownershipError,
  }
}
