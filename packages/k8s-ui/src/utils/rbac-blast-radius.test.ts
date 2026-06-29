import { describe, it, expect } from 'vitest'
import { detectBlastRadius, rulePermissivenessScore } from './rbac-blast-radius'
import type { RBACBindingRules, RBACSubjectResponse } from '../types/rbac'

// Skeleton helpers — keep fixtures terse so each test reads as a contract.
function binding(over: Partial<RBACBindingRules> = {}): RBACBindingRules {
  return {
    binding: { kind: 'RoleBinding', namespace: 'default', name: 'b', roleRef: { kind: 'Role', namespace: 'default', name: 'r' } },
    role: { kind: 'Role', namespace: 'default', name: 'r' },
    rules: [],
    ...over,
  }
}

function subject(direct: RBACBindingRules[] = [], inherited: { groupName: string; bindings: RBACBindingRules[] }[] = []): RBACSubjectResponse {
  return {
    subject: { kind: 'ServiceAccount', namespace: 'default', name: 'app-sa' },
    direct,
    inheritedFromGroups: inherited,
    flat: [],
    truncated: false,
  }
}

describe('detectBlastRadius', () => {
  // The five documented triggers; each is a security-UX invariant the PR
  // description and inline comments call load-bearing. If any of these
  // silently regresses, the Pod blast-radius banner stops alarming on the
  // exact scenarios it was added for.

  it('flags cluster-admin role-name match', () => {
    const b = binding({ role: { kind: 'ClusterRole', name: 'cluster-admin', namespace: '' } })
    const reasons = detectBlastRadius(subject([b]))
    expect(reasons).toHaveLength(1)
    expect(reasons[0].reason).toMatch(/cluster-admin/)
  })

  it('flags an admin-shaped grant via ClusterRoleBinding', () => {
    // apiGroups/resources/verbs all `*` bound cluster-wide is god-mode.
    const b = binding({
      binding: { kind: 'ClusterRoleBinding', namespace: '', name: 'admin-crb', roleRef: { kind: 'ClusterRole', namespace: '', name: 'admin' } },
      rules: [{ verbs: ['*'], resources: ['*'], apiGroups: ['*'] }],
    })
    const reasons = detectBlastRadius(subject([b]))
    expect(reasons).toHaveLength(1)
    expect(reasons[0].reason).toMatch(/verb wildcard/)
  })

  it('does NOT flag the same admin-shaped grant via a namespace RoleBinding', () => {
    // A RoleBinding is namespace-scoped by apiserver construction — even
    // `*/*/*` is namespace-admin, not cluster takeover, so it stays collapsed.
    const b = binding({
      rules: [{ verbs: ['*'], resources: ['*'], apiGroups: ['*'] }],
    })
    expect(detectBlastRadius(subject([b]))).toHaveLength(0)
  })

  it('flags verb wildcards on a cluster-wide binding (incl. cluster-wide secrets)', () => {
    // Cluster-wide `verbs:['*']` on secrets is read-every-secret takeover and
    // MUST stay loud even though the resource is named, not `*`.
    const b = binding({
      binding: { kind: 'ClusterRoleBinding', namespace: '', name: 'cluster-b', roleRef: { kind: 'ClusterRole', namespace: '', name: 'r' } },
      rules: [{ verbs: ['*'], resources: ['secrets'], apiGroups: [''] }],
    })
    const reasons = detectBlastRadius(subject([b]))
    expect(reasons).toHaveLength(1)
    expect(reasons[0].reason).toMatch(/verb wildcard/)
  })

  it("does NOT flag a verb wildcard scoped to one operator's own CRDs", () => {
    // `verbs:['*']` confined to a specific apiGroup+resources in a single
    // namespace (RoleBinding) is the routine operator-owns-its-CRDs grant —
    // expanding the Permissions section on every such SA is the over-fire
    // this exclusion fixes.
    const b = binding({
      rules: [{ verbs: ['*'], resources: ['widgets'], apiGroups: ['acme.example.com'] }],
    })
    expect(detectBlastRadius(subject([b]))).toHaveLength(0)
  })

  it('does NOT flag a namespace RoleBinding with resource-wildcard verb-wildcard over one group', () => {
    // `verbs:['*']` over `resources:['*']` in one apiGroup, bound via a
    // namespace RoleBinding, is namespace-scoped — broad within the namespace
    // but not cluster takeover. Common operator shape; stays collapsed.
    const b = binding({
      rules: [{ verbs: ['*'], resources: ['*'], apiGroups: ['acme.example.com'] }],
    })
    expect(detectBlastRadius(subject([b]))).toHaveLength(0)
  })

  it('flags escalation verbs', () => {
    const b = binding({
      rules: [{ verbs: ['escalate'], resources: ['roles'], apiGroups: ['rbac.authorization.k8s.io'] }],
    })
    const reasons = detectBlastRadius(subject([b]))
    expect(reasons).toHaveLength(1)
    expect(reasons[0].reason).toMatch(/escalate/)
  })

  it('flags impersonate and bind too', () => {
    const b1 = binding({ binding: { kind: 'RoleBinding', namespace: 'default', name: 'b1', roleRef: { kind: 'Role', namespace: 'default', name: 'r' } }, rules: [{ verbs: ['impersonate'], resources: ['users'], apiGroups: [''] }] })
    const b2 = binding({ binding: { kind: 'RoleBinding', namespace: 'default', name: 'b2', roleRef: { kind: 'Role', namespace: 'default', name: 'r' } }, rules: [{ verbs: ['bind'], resources: ['roles'], apiGroups: ['rbac.authorization.k8s.io'] }] })
    const reasons = detectBlastRadius(subject([b1, b2]))
    expect(reasons).toHaveLength(2)
    expect(reasons[0].reason).toMatch(/impersonate/)
    expect(reasons[1].reason).toMatch(/bind/)
  })

  it('flags cluster-wide "create pods" but NOT namespace-scoped "create pods"', () => {
    // The cluster-wide form is the lateral-movement vector — a Pod with
    // namespace-scoped pod-create can only move within its namespace.
    const cluster = binding({
      binding: { kind: 'ClusterRoleBinding', namespace: '', name: 'cluster-b', roleRef: { kind: 'ClusterRole', namespace: '', name: 'r' } },
      rules: [{ verbs: ['create'], resources: ['pods'], apiGroups: [''] }],
    })
    const namespaced = binding({
      binding: { kind: 'RoleBinding', namespace: 'default', name: 'ns-b', roleRef: { kind: 'Role', namespace: 'default', name: 'r' } },
      rules: [{ verbs: ['create'], resources: ['pods'], apiGroups: [''] }],
    })
    expect(detectBlastRadius(subject([cluster]))).toHaveLength(1)
    expect(detectBlastRadius(subject([namespaced]))).toHaveLength(0)
  })

  it('does NOT flag resource-only wildcards', () => {
    // The `view` ClusterRole grants get/list/watch on `*` resources in
    // every fluxcd CRD. Flagging this would alarm on every authenticated
    // SA — calibration call documented in the source.
    const b = binding({
      rules: [{ verbs: ['get', 'list', 'watch'], resources: ['*'], apiGroups: ['source.toolkit.fluxcd.io'] }],
    })
    expect(detectBlastRadius(subject([b]))).toHaveLength(0)
  })

  it('dedupes by binding identity when multiple risky rules match', () => {
    const b = binding({
      binding: { kind: 'ClusterRoleBinding', namespace: '', name: 'cluster-b', roleRef: { kind: 'ClusterRole', namespace: '', name: 'r' } },
      rules: [
        { verbs: ['*'], resources: ['secrets'], apiGroups: [''] },
        { verbs: ['escalate'], resources: ['roles'], apiGroups: ['rbac.authorization.k8s.io'] },
      ],
    })
    const reasons = detectBlastRadius(subject([b]))
    expect(reasons).toHaveLength(1)
  })

  it('includes inherited-from-group bindings, not just direct', () => {
    const b = binding({
      binding: { kind: 'ClusterRoleBinding', namespace: '', name: 'inherited', roleRef: { kind: 'ClusterRole', namespace: '', name: 'r' } },
      rules: [{ verbs: ['*'], resources: ['secrets'], apiGroups: [''] }],
    })
    const reasons = detectBlastRadius(subject([], [{ groupName: 'system:authenticated', bindings: [b] }]))
    expect(reasons).toHaveLength(1)
    expect(reasons[0].reason).toMatch(/verb wildcard/)
  })

  it('returns empty for benign bindings', () => {
    const b = binding({
      rules: [{ verbs: ['get', 'list'], resources: ['configmaps'], apiGroups: [''] }],
    })
    expect(detectBlastRadius(subject([b]))).toHaveLength(0)
  })
})

describe('rulePermissivenessScore', () => {
  it('scores wildcards highest', () => {
    const wildcard = rulePermissivenessScore({ verbs: ['*'], resources: ['*'], apiGroups: ['*'] })
    const benign = rulePermissivenessScore({ verbs: ['get'], resources: ['configmaps'], apiGroups: [''] })
    expect(wildcard).toBeGreaterThan(benign)
  })

  it('boosts cluster-wide "create pods"', () => {
    const createPods = rulePermissivenessScore({ verbs: ['create'], resources: ['pods'], apiGroups: [''] })
    const createOthers = rulePermissivenessScore({ verbs: ['create'], resources: ['configmaps'], apiGroups: [''] })
    expect(createPods).toBeGreaterThan(createOthers)
  })

  it('scores escalation verbs higher than plain writes', () => {
    const escalate = rulePermissivenessScore({ verbs: ['escalate'], resources: ['roles'], apiGroups: ['rbac.authorization.k8s.io'] })
    const patch = rulePermissivenessScore({ verbs: ['patch'], resources: ['configmaps'], apiGroups: [''] })
    expect(escalate).toBeGreaterThan(patch)
  })
})
