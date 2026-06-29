// Pure helpers for the "what's the blast radius of this Pod / Workload / SA"
// detection. Lives outside the renderers because it's the security-UX core
// of the RBAC visibility feature — the rule that resource-only wildcards do
// NOT trigger (because `view` covers every fluxcd CRD via wildcards and would
// alarm on every authenticated SA) is load-bearing and easily regressed
// silently. Tested via rbac-blast-radius.test.ts; keep it pure.

import type { RBACBindingRules, RBACPolicyRule, RBACSubjectResponse } from '../types/rbac'

export const RBAC_BLAST_READ_VERBS = new Set(['get', 'list', 'watch'])
export const RBAC_BLAST_WRITE_VERBS = new Set(['create', 'update', 'patch'])
export const RBAC_BLAST_DELETE_VERBS = new Set(['delete', 'deletecollection'])
export const RBAC_BLAST_ESCALATION_VERBS = new Set(['escalate', 'bind', 'impersonate'])

/**
 * Score a single PolicyRule by max permissiveness. Higher is scarier.
 * The ordering matters more than the absolute scale — used to sort the
 * rule preview so dangerous rules surface first in incident response.
 */
export function rulePermissivenessScore(r: RBACPolicyRule): number {
  let score = 0
  const verbs = r.verbs ?? []
  const resources = r.resources ?? []
  const groups = r.apiGroups ?? []
  if (verbs.includes('*')) score += 100
  if (resources.includes('*')) score += 100
  if (groups.includes('*')) score += 100
  for (const v of verbs) {
    if (RBAC_BLAST_ESCALATION_VERBS.has(v)) score += 80
    else if (RBAC_BLAST_DELETE_VERBS.has(v)) score += 40
    else if (RBAC_BLAST_WRITE_VERBS.has(v)) score += 20
    else if (RBAC_BLAST_READ_VERBS.has(v)) score += 5
    else score += 10
  }
  if (verbs.includes('create') && resources.includes('pods')) score += 50
  return score
}

export interface BlastRadiusReason {
  binding: RBACBindingRules
  reason: string
}

/**
 * Detect bindings that genuinely make a Pod's compromise a cluster takeover.
 * Triggers on, in priority order:
 *
 *   1. cluster-admin role-name match (covers the "bound my SA to cluster-admin
 *      for testing" pattern even when rules aren't loaded)
 *   2. verb wildcards (`*`) granted by a cluster-wide binding (ClusterRoleBinding)
 *   3. escalation verbs (`escalate` / `bind` / `impersonate`)
 *   4. cluster-wide `create pods` (lateral-movement vector)
 *
 * Resource-only wildcards (e.g. `view`'s fluxcd CRD coverage) deliberately
 * do NOT trigger — they fire on every authenticated SA and would train
 * operators to ignore the alarm. A verb wildcard granted by a RoleBinding is
 * likewise excluded — a RoleBinding is namespace-scoped by apiserver
 * construction, so even `verbs:['*']` over `resources:['*']` across every
 * apiGroup (namespace-admin) is not cluster takeover, and operators routinely
 * hold broad wildcards over their own CRDs. Only a cluster-wide binding counts
 * as genuine god-mode. The current set is the calibrated minimum for "the
 * Pod's identity is genuinely god-mode".
 *
 * Dedupes by binding identity so a binding with multiple risky rules
 * counts once; first matching reason wins.
 */
export function detectBlastRadius(rbacData: RBACSubjectResponse): BlastRadiusReason[] {
  const reasons: BlastRadiusReason[] = []
  const allBindings = [
    ...rbacData.direct,
    ...rbacData.inheritedFromGroups.flatMap((g) => g.bindings),
  ]
  for (const br of allBindings) {
    if (br.role.name === 'cluster-admin' && br.role.kind === 'ClusterRole') {
      reasons.push({ binding: br, reason: 'bound to cluster-admin (full god-mode access)' })
      continue
    }
    for (const r of br.rules ?? []) {
      const verbs = r.verbs ?? []
      const resources = r.resources ?? []
      // A verb wildcard is only cluster takeover when the binding is itself
      // cluster-wide. A RoleBinding is namespace-scoped by apiserver
      // construction no matter how broad its rule (apiGroups/resources `*`
      // included), so namespace-admin SAs stay collapsed; only
      // ClusterRoleBindings count.
      if (verbs.includes('*') && br.binding.kind === 'ClusterRoleBinding') {
        reasons.push({ binding: br, reason: 'grants verb wildcard (*) — every action on the listed resources' })
        break
      }
      const escalations = verbs.filter((v) => RBAC_BLAST_ESCALATION_VERBS.has(v))
      if (escalations.length > 0) {
        reasons.push({ binding: br, reason: `grants privilege-escalation verbs: ${escalations.join(', ')}` })
        break
      }
      if (verbs.includes('create') && resources.includes('pods') && br.binding.kind === 'ClusterRoleBinding') {
        reasons.push({ binding: br, reason: 'grants cluster-wide "create pods" — a known lateral-movement vector' })
        break
      }
    }
  }
  const seen = new Set<string>()
  return reasons.filter((r) => {
    const key = r.binding.binding.kind + '/' + r.binding.binding.namespace + '/' + r.binding.binding.name
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}
