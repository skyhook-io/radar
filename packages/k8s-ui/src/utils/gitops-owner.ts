// Maps server manager evidence to drawer copy and navigation. Older Radars
// without the answer retain their managedBy projection.

import type { ManagerAnswer, ManagerClaim, ManagerEvidence, Relationships, ResourceRef } from '../types/core'
import { dedupeResourceRefs, resourceRefKey } from './resource-refs'

// GitOpsOwnerRef is a discriminated union — the tool determines which kind is
// valid. Modeling it this way prevents callers (and consumers of the returned
// type) from constructing `{ tool: 'argo', kind: 'helmreleases' }` which would
// route to a non-existent page.
export type GitOpsOwnerRef =
  | { tool: 'argocd'; kind: 'applications'; namespace: string; name: string }
  | { tool: 'fluxcd'; kind: 'kustomizations' | 'helmreleases'; namespace: string; name: string }

// Vocabulary mirrors `pkg/gitops/tree.Tool` so the wire labels match end-to-end.
export type GitOpsOwnerTool = GitOpsOwnerRef['tool']

// API groups for GitOps manager kinds. Used to disambiguate Flux HelmRelease
// from a future native-Helm kind, etc.
const ARGO_APPLICATION_GROUP = 'argoproj.io'
const FLUX_KUSTOMIZE_GROUP = 'kustomize.toolkit.fluxcd.io'
const FLUX_HELM_GROUP = 'helm.toolkit.fluxcd.io'

// gitOpsOwnerFromRelationships reads relationships.managedBy[0] and maps it to
// a GitOpsOwnerRef when the manager is a GitOps controller. Returns null when
// the manager is a native K8s owner (Deployment, ReplicaSet, etc.), a plain
// Helm release, or when no manager is reported.
//
// Old radar binaries (pre-T2) emit no managedBy field; this returns null in
// that case and the drawer silently skips the chip — natural pressure to
// upgrade rather than carry a label-parsing fallback in the client.
export function gitOpsOwnerFromRelationships(
  rel: Relationships | undefined | null,
): GitOpsOwnerRef | null {
  const answer = rel?.ownership?.manager
  if (answer) return answer.lead ? gitOpsOwnerFromRef(answer.lead) : null
  const refs = rel?.managedBy
  if (!refs || refs.length === 0) return null
  return gitOpsOwnerFromRef(refs[0])
}

/** A manager ref (relationships.managedBy, a workspace's managedBy) as a GitOps owner, or null when the manager is not a GitOps controller. */
export function gitOpsOwnerFromRef(ref: ResourceRef): GitOpsOwnerRef | null {
  switch (true) {
    case ref.kind === 'Application' && ref.group === ARGO_APPLICATION_GROUP:
      return { tool: 'argocd', kind: 'applications', namespace: ref.namespace, name: ref.name }
    case ref.kind === 'Kustomization' && ref.group === FLUX_KUSTOMIZE_GROUP:
      return { tool: 'fluxcd', kind: 'kustomizations', namespace: ref.namespace, name: ref.name }
    case ref.kind === 'HelmRelease' && ref.group === FLUX_HELM_GROUP:
      return { tool: 'fluxcd', kind: 'helmreleases', namespace: ref.namespace, name: ref.name }
    default:
      return null
  }
}

/**
 * How the managers whose status lists a resource (relationships.managers) bear
 * on the GitOps owner its own tracking metadata names. `confirmed`: that owner
 * is the only manager listing it. `conflict`: GitOps managers list it and the
 * owner isn't one of them. Undefined when there is nothing to compare, the
 * owner is one of several, the owner isn't verified in this cluster, or the
 * search left managers unsettled (`settled` false): then a listed manager may
 * be a topology link rather than a status list Radar read, and the owner's own
 * list may be the one it couldn't read.
 */
export type ManagerListing = { state: 'confirmed' } | { state: 'conflict'; listedBy: GitOpsOwnerRef[] }

export function managerListing(owner: GitOpsOwnerRef | null | undefined, managers: ResourceRef[] | undefined, verified: boolean, settled: boolean): ManagerListing | undefined {
  if (!owner || !managers?.length || !verified || !settled) return undefined
  const listed = managers.map(gitOpsOwnerFromRef).filter((m): m is GitOpsOwnerRef => m !== null)
  if (listed.length === 0) return undefined
  // An Argo CD owner named only by its instance label has no namespace yet.
  const isOwner = (m: GitOpsOwnerRef) => m.tool === owner.tool && m.kind === owner.kind && m.name === owner.name && (!owner.namespace || m.namespace === owner.namespace)
  if (!listed.some(isOwner)) return { state: 'conflict', listedBy: listed }
  return listed.length === 1 && managers.length === 1 ? { state: 'confirmed' } : undefined
}


export function activeManagerClaims(evidence: ManagerEvidence): ManagerClaim[] {
  const claims = (evidence.claims ?? []).filter(c => !c.copied)
  const hasGitOps = Boolean(evidence.recorders?.length) || claims.some(c => c.source !== 'helm-release')
  return hasGitOps ? claims.filter(c => c.source !== 'helm-release') : claims
}

export function managerCandidates(answer: ManagerAnswer): ResourceRef[] {
  const evidence = answer.via ?? answer.direct
  const recorders = evidence.recorders ?? []
  const qualified = [...recorders, ...(answer.lead ? [answer.lead] : []), ...activeManagerClaims(evidence).map(c => c.manager).filter(r => r.namespace)]
  // A claim without a namespace names the qualified manager of its kind and
  // name the answer already holds, when there is exactly one.
  const claims = activeManagerClaims(evidence).map(c => {
    const ref = c.manager
    if (ref.namespace) return ref
    const matches = dedupeResourceRefs(qualified.filter(r => r.kind === ref.kind && r.group === ref.group && r.name === ref.name))
    return matches.length === 1 ? matches[0] : ref
  })
  return dedupeResourceRefs([...(answer.lead ? [answer.lead] : []), ...claims, ...recorders])
}

export function managerRefLabel(ref: ResourceRef): string {
  return `${ref.kind} ${ref.namespace ? `${ref.namespace}/` : ''}${ref.name}`
}

export function managerChipPresentation(answer: ManagerAnswer): { prefix: string; label: string; via?: string; tooltip: string; warning: boolean } | null {
  if (answer.state === 'unknown' || answer.state === 'none') return null
  const evidence = answer.via ?? answer.direct
  const candidates = managerCandidates(answer)
  const names = (refs: ResourceRef[]) => refs.map(managerRefLabel).join(' and ')
  const claims = activeManagerClaims(evidence)
  const claimed = dedupeResourceRefs(claims.map(c => c.manager))
  const recorders = evidence.recorders ?? []
  // A list alone records what a manager compared or applied, which can be an
  // object it doesn't sync (a copy carrying another's tracking ID, a CRD a
  // HelmRelease skips): only a list and the object's metadata agreeing says
  // it manages the object.
  const prefix = answer.state === 'agree' ? 'Managed by' : answer.state === 'shared' || answer.state === 'listed' ? 'Listed by' : 'Tracked by'
  const listed = answer.lead && recorders.some(r => resourceRefKey(r) === resourceRefKey(answer.lead!))
    ? dedupeResourceRefs([answer.lead, ...recorders]) : recorders
  const displayed = answer.state === 'disagree' ? claimed : answer.state === 'shared' ? listed : candidates
  const label = displayed.map(r => r.namespace ? `${r.namespace}/${r.name}` : r.name).join(' and ')
  const reasons: Record<ManagerClaim['check'], string> = {
    recorded: 'its status lists this resource',
    omitted: "its status doesn't list this resource",
    unreadable: "Radar can't read its status",
    remote: "Radar can't confirm it deploys to this cluster",
    missing: 'no such manager here',
    'not-recording': 'Helm records an installation, rather than continuously reconciling it',
    unresolved: 'Radar found no manager of that name listing this resource; it may be managed from another cluster',
    withheld: "you can't view it",
    unchecked: "Radar couldn't check its status",
  }
  const subject = answer.via ? managerRefLabel(evidence.object) : 'this resource'
  const details: string[] = []
  const whose = answer.via ? `${managerRefLabel(evidence.object)}'s` : "This resource's"
  // A newer Radar may send a check this UI doesn't know.
  const why = (c: ManagerClaim, object: string) => c.check === 'missing'
    ? `there's no such ${c.manager.kind} here`
    : (reasons[c.check] ?? "Radar couldn't confirm its status lists this resource").replace('manager of', `${c.manager.kind} of`).replace('this resource', object)
  // Tracked by names what metadata says, which Radar hasn't confirmed: one
  // claim says why in the same sentence.
  const single = answer.state === 'claimed' && claims.length === 1 && claims[0].check !== 'recorded' ? claims[0] : null
  if (single) {
    details.push(`${whose} ${single.source.includes('label') ? 'label' : 'annotation'} names ${managerRefLabel(single.manager)}, but ${why(single, answer.via ? subject : 'it')}.`)
  } else if (answer.state === 'claimed') {
    details.push(`${whose} labels or annotations name ${names(claimed)}; Radar hasn't confirmed ${claimed.length === 1 ? 'it manages' : 'they manage'} it.`)
  }
  for (const c of claims) {
    if (c.check === 'recorded' || c === single) continue
    details.push(`${managerRefLabel(c.manager)}: ${why(c, subject)}.`)
  }
  if (recorders.length) {
    // A claim its list confirms adds nothing but the agreement.
    const confirmed = claims.some(c => c.check === 'recorded')
    details.push(`${names(recorders)} ${recorders.length === 1 ? 'lists' : 'list'} ${subject} in ${recorders.length === 1 ? 'its status' : 'their statuses'}${confirmed ? `, and ${answer.via ? 'its' : 'this resource\'s'} tracking metadata names ${recorders.length === 1 ? 'it' : 'one of them'}` : ''}.`)
  }
  if (answer.via) details.push(`Through ${managerRefLabel(answer.via.object)}.`)
  // Claims the answer passed on the way stay visible, with or without Via.
  {
    for (const e of [...(answer.via ? [answer.direct] : []), ...(answer.between ?? [])]) {
      for (const c of e.claims ?? []) {
        if (c.copied) continue
        details.push(`${managerRefLabel(e.object)}: its own ${c.source.includes('label') ? 'label' : 'annotation'} names ${managerRefLabel(c.manager)}.`)
      }
    }
  }
  if (answer.narrowed) details.push('This answer covers what you can view.')
  return { prefix, label, via: answer.via?.object.kind, tooltip: details.join(' '), warning: answer.state === 'disagree' }
}

/**
 * A candidate manager of an answer, with what names it: its list, its
 * metadata, or both. found: Radar read the manager, so a link to it opens it;
 * a claim of a manager that is missing, withheld or unresolved isn't.
 */
export type ManagerChoice = { ref: ResourceRef; listed: boolean; claimed: boolean; found: boolean }

/** Every candidate manager of an answer, lead first, each with what names it. */
export function managerChoices(answer: ManagerAnswer): ManagerChoice[] {
  const evidence = answer.via ?? answer.direct
  const recorders = new Set((evidence.recorders ?? []).map(resourceRefKey))
  const candidates = managerCandidates(answer)
  // A manager is found when any claim naming it read it; a check this UI
  // doesn't know never counts as one.
  const read: Array<string> = ['recorded', 'omitted', 'unreadable', 'remote']
  const claimed = new Set<string>()
  const found = new Set<string>()
  for (const c of activeManagerClaims(evidence)) {
    const ref = c.manager
    const key = resourceRefKey(ref.namespace ? ref : candidates.find(r => r.kind === ref.kind && r.group === ref.group && r.name === ref.name) ?? ref)
    claimed.add(key)
    if (read.includes(c.check)) found.add(key)
  }
  return candidates.map(ref => {
    const key = resourceRefKey(ref)
    const listed = recorders.has(key)
    return { ref, listed, claimed: claimed.has(key), found: listed || found.has(key) }
  })
}

export function managerChipCoversGroup(relationships: Relationships): boolean {
  const answer = relationships.ownership?.manager
  if (!answer || !['agree', 'listed'].includes(answer.state) || answer.via || relationships.ownership?.managers) return false
  const recorders = answer.direct.recorders ?? []
  const managers = relationships.managers ?? []
  return recorders.length === 1 && managers.length === 1 && resourceRefKey(recorders[0]) === resourceRefKey(managers[0])
}
