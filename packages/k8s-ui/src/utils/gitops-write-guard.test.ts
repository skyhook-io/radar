import { describe, expect, it } from 'vitest'

import type { GitOpsOwnerRef } from './gitops-owner'
import {
  canConfirmGitOpsWrite,
  evaluateGitOpsWriteGuard,
  gitOpsWriteEvidencePaths,
  gitOpsWriteGuardKey,
  type GitOpsPathEvidence,
  type GitOpsWrite,
  type GitOpsWriteEvidence,
  type GitOpsWriteGuardInput,
  type GitOpsWritePolicyEvidence,
} from './gitops-write-guard'

const target = { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }
const argo: GitOpsOwnerRef = { tool: 'argocd', kind: 'applications', namespace: 'argocd', name: 'pg' }
const kustomization: GitOpsOwnerRef = { tool: 'fluxcd', kind: 'kustomizations', namespace: 'flux-system', name: 'apps' }
const helmRelease: GitOpsOwnerRef = { tool: 'fluxcd', kind: 'helmreleases', namespace: 'flux-system', name: 'pg' }

const HIBERNATE = 'metadata.annotations["cnpg.io/hibernation"]'
const hibernate: GitOpsWrite = { scope: 'metadata', paths: [HIBERNATE], description: 'Hibernation' }

const selfHeal: GitOpsWritePolicyEvidence = { tool: 'argocd', auto: true, selfHeal: true, prune: false, suspended: null }
const autoNoHeal: GitOpsWritePolicyEvidence = { tool: 'argocd', auto: true, selfHeal: false, prune: false, suspended: null }
const manual: GitOpsWritePolicyEvidence = { tool: 'argocd', auto: false, selfHeal: false, prune: false, suspended: null }

function pathEvidence(overrides: Partial<GitOpsPathEvidence> = {}): GitOpsPathEvidence {
  return { path: HIBERNATE, lastApplied: 'absent', ownedBy: [], ownedByGitOps: false, ...overrides }
}

function evidence(policy: GitOpsWritePolicyEvidence | null, paths: GitOpsPathEvidence[] = [], extra: Partial<GitOpsWriteEvidence> = {}): GitOpsWriteEvidence {
  return { uid: 'u', resourceVersion: '1', owner: null, policy, paths, ...extra }
}

function guard(input: Partial<GitOpsWriteGuardInput> & Pick<GitOpsWriteGuardInput, 'writes'>) {
  return evaluateGitOpsWriteGuard({ target, owner: argo, ...input })
}

describe('evaluateGitOpsWriteGuard', () => {
  it('reports nothing without an owner', () => {
    const g = evaluateGitOpsWriteGuard({ target, owner: null, writes: [hibernate] })
    expect(g.level).toBe('none')
    expect(g.requiresAck).toBe(false)
    expect(g.summary).toBe('')
  })

  it('never warns for status writes', () => {
    const g = guard({ writes: [{ scope: 'status' }], evidence: evidence(selfHeal) })
    expect(g.level).toBe('none')
    expect(g.perWrite[0].reason).toContain('ordinary GitOps sync does not manage these fields')
  })

  it('treats a new child object as info', () => {
    const g = guard({ writes: [{ scope: 'create-child' }], evidence: evidence(selfHeal) })
    expect(g.level).toBe('info')
    expect(g.requiresAck).toBe(false)
  })

  describe('declared fields', () => {
    const inLastApplied = [pathEvidence({ lastApplied: 'present' })]
    const ownedByArgo = [
      pathEvidence({
        lastApplied: 'no-annotation',
        ownedByGitOps: true,
        ownedBy: [{ manager: 'argocd-controller', operation: 'Apply', tool: 'argocd' }],
      }),
    ]

    it.each([
      ['last-applied', inLastApplied],
      ['managedFields', ownedByArgo],
    ])('%s + self-heal ⇒ will-revert', (_label, paths) => {
      const g = guard({ writes: [hibernate], evidence: evidence(selfHeal, paths) })
      expect(g.level).toBe('will-revert')
      expect(g.requiresAck).toBe(true)
      expect(g.summary).toContain('Argo CD Application argocd/pg will revert this change.')
      expect(g.summary).toContain('overwritten at the next sync')
    })

    it('names the owning field manager', () => {
      const g = guard({ writes: [hibernate], evidence: evidence(selfHeal, ownedByArgo) })
      expect(g.perWrite[0].reason).toContain('argocd-controller owns this field')
    })

    it('auto-sync without self-heal ⇒ may-revert', () => {
      const g = guard({ writes: [hibernate], evidence: evidence(autoNoHeal, inLastApplied) })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('the next sync will overwrite it')
    })

    it('manual sync ⇒ may-revert', () => {
      const g = guard({ writes: [hibernate], evidence: evidence(manual, inLastApplied) })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('Auto-sync is off; the next sync will overwrite it.')
    })

    it('unreadable owner ⇒ may-revert naming why', () => {
      const g = guard({
        writes: [hibernate],
        evidence: evidence(null, inLastApplied, { policyError: "you can't read the owner, so its sync policy is unknown" }),
      })
      expect(g.level).toBe('may-revert')
      expect(g.syncPolicy).toBeNull()
      expect(g.perWrite[0].reason).toContain("you can't read the owner")
    })

    it('Argo Replace=true treats every field as declared', () => {
      const g = guard({ writes: [hibernate], evidence: evidence({ ...selfHeal, replace: true }, [pathEvidence()]) })
      expect(g.level).toBe('will-revert')
      expect(g.perWrite[0].reason).toContain('Replace=true')
    })
  })

  describe('undeclared or unknown fields are never safe', () => {
    it('absent from last-applied and managedFields ⇒ may-revert, lower-confidence copy', () => {
      const g = guard({ writes: [hibernate], evidence: evidence(selfHeal, [pathEvidence()]) })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain("doesn't rule it out")
      expect(g.perWrite[0].reason).toContain('may be overwritten on sync if the GitOps source declares this field')
      expect(g.summary).not.toMatch(/won't be reverted|not declared in Git/i)
    })

    it('no evidence at all ⇒ may-revert', () => {
      const g = guard({ writes: [hibernate], evidenceError: 'boom' })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain("can't see which fields")
    })

    it('unknown fields (no paths) ⇒ may-revert', () => {
      const g = guard({ writes: [{ scope: 'spec' }], evidence: evidence(selfHeal) })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain("doesn't know which fields")
    })
  })

  describe('ignore rules', () => {
    it('effective ignore (RespectIgnoreDifferences) ⇒ info', () => {
      const g = guard({
        writes: [hibernate],
        evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' })]),
      })
      expect(g.level).toBe('info')
      expect(g.requiresAck).toBe(false)
    })

    it('comparison-only ignore on a declared field ⇒ may-revert', () => {
      const g = guard({
        writes: [hibernate],
        evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored: 'comparison-only', ignoredBy: 'spec.ignoreDifferences' })]),
      })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('without RespectIgnoreDifferences')
    })

    it('unevaluated jq ignore rule on a declared field ⇒ may-revert, not will-revert', () => {
      const g = guard({
        writes: [hibernate],
        evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored: 'unevaluated', ignoredBy: 'spec.ignoreDifferences jqPathExpressions' })]),
      })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('jq')
    })

    // The annotation on the live object doesn't prove the source declares it.
    it('Flux ssa: IfNotPresent on the live object ⇒ may-revert', () => {
      const g = guard({
        owner: kustomization,
        writes: [hibernate],
        evidence: evidence({ tool: 'fluxcd', auto: true, selfHeal: true, prune: true, suspended: false, objectReconcile: 'if-not-present' }, [pathEvidence({ lastApplied: 'present' })]),
      })
      expect(g.level).toBe('may-revert')
      expect(g.requiresAck).toBe(true)
      expect(g.perWrite[0].reason).toContain("can't confirm the GitOps source declares it")
    })

    it('an owner matched by name only never waives the acknowledgment', () => {
      const ignored = evidence({ ...selfHeal, respectIgnoreDifferences: true }, [pathEvidence({ lastApplied: 'present', ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' })])
      expect(guard({ writes: [hibernate], evidence: ignored }).level).toBe('info')
      const byName = guard({ writes: [hibernate], evidence: ignored, ownerMatchedByName: true })
      expect(byName.level).toBe('may-revert')
      expect(byName.requiresAck).toBe(true)
      expect(byName.perWrite[0].reason).toContain('by name only')
    })
  })

  describe('Flux', () => {
    const owned = [pathEvidence({ ownedByGitOps: true, ownedBy: [{ manager: 'kustomize-controller', operation: 'Apply', tool: 'fluxcd' }] })]

    it('active Kustomization ⇒ will-revert with its interval', () => {
      const g = guard({
        owner: kustomization,
        writes: [hibernate],
        evidence: evidence({ tool: 'fluxcd', auto: true, selfHeal: true, prune: true, suspended: false, interval: '10m' }, owned),
      })
      expect(g.level).toBe('will-revert')
      expect(g.perWrite[0].reason).toContain('next reconcile (every 10m)')
      expect(g.syncPolicy).toEqual({ tool: 'fluxcd', auto: true, selfHeal: true, prune: true, suspended: false, interval: '10m' })
    })

    it('suspended Kustomization ⇒ may-revert (resuming overwrites)', () => {
      const g = guard({
        owner: kustomization,
        writes: [hibernate],
        evidence: evidence({ tool: 'fluxcd', auto: false, selfHeal: false, prune: true, suspended: true, interval: '10m' }, owned),
      })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('is suspended; resuming it will overwrite')
    })

    it.each([
      ['disabled', false, 'may-revert'],
      ['warn', false, 'may-revert'],
      ['enabled', true, 'will-revert'],
    ] as const)('HelmRelease drift detection %s ⇒ %s', (mode, heal, level) => {
      const g = guard({
        owner: helmRelease,
        writes: [hibernate],
        evidence: evidence({ tool: 'fluxcd', auto: true, selfHeal: heal, prune: null, suspended: false, driftDetection: mode }, [
          pathEvidence({ ownedByGitOps: true, ownedBy: [{ manager: 'helm-controller', operation: 'Update', tool: 'fluxcd' }] }),
        ]),
      })
      expect(g.level).toBe(level)
    })
  })

  describe('HelmRelease drift exemptions', () => {
    const helmOwned = pathEvidence({ ownedByGitOps: true, ownedBy: [{ manager: 'helm-controller', operation: 'Update', tool: 'fluxcd' }] })
    const heal: GitOpsWritePolicyEvidence = { tool: 'fluxcd', auto: true, selfHeal: true, prune: null, suspended: false, driftDetection: 'enabled' }

    it('an ignored field still reverts on the next upgrade', () => {
      const g = guard({
        owner: helmRelease,
        writes: [hibernate],
        evidence: evidence(heal, [{ ...helmOwned, ignored: 'effective', ignoredBy: 'spec.driftDetection.ignore' }]),
      })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('next Helm upgrade overwrites it')
    })

    it('an object opted out of drift detection still reverts on the next upgrade', () => {
      const g = guard({ owner: helmRelease, writes: [hibernate], evidence: evidence({ ...heal, objectReconcile: 'ignore' }, [helmOwned]) })
      expect(g.level).toBe('may-revert')
    })

    it('deleting an opted-out object is recreated by the next upgrade', () => {
      const g = guard({ owner: helmRelease, writes: [{ scope: 'delete' }], evidence: evidence({ ...heal, objectReconcile: 'ignore' }) })
      expect(g.level).toBe('may-revert')
      expect(g.perWrite[0].reason).toContain('next Helm upgrade recreates it')
    })
  })

  describe('delete', () => {
    it('self-heal ⇒ will-revert (recreated)', () => {
      const g = guard({ writes: [{ scope: 'delete' }], evidence: evidence(selfHeal) })
      expect(g.level).toBe('will-revert')
      expect(g.perWrite[0].reason).toContain('recreate')
    })

    it('manual sync ⇒ may-revert', () => {
      expect(guard({ writes: [{ scope: 'delete' }], evidence: evidence(manual) }).level).toBe('may-revert')
    })

    it('an operator-owned object is recreated by the operator, not GitOps', () => {
      const g = guard({
        target: { kind: 'Pod', group: '', namespace: 'db', name: 'pg-1' },
        writes: [{ scope: 'delete' }],
        evidence: evidence(selfHeal, [], { controllerOwner: { apiVersion: 'postgresql.cnpg.io/v1', kind: 'Cluster', name: 'pg' } }),
      })
      expect(g.level).toBe('info')
      expect(g.perWrite[0].reason).toBe('Cluster pg will recreate this Pod.')
    })
  })

  it('native Helm ownership needs acknowledgement', () => {
    const g = evaluateGitOpsWriteGuard({ target, owner: null, helmRelease: { namespace: 'db', name: 'pg' }, writes: [hibernate] })
    expect(g.level).toBe('may-revert')
    expect(g.summary).toContain('Helm release db/pg may revert this change.')
  })

  it('unverifiable ownership needs acknowledgement', () => {
    const g = evaluateGitOpsWriteGuard({ target, owner: null, ownershipError: 'lookup failed', writes: [hibernate] })
    expect(g.requiresAck).toBe(true)
  })

  it('reports the worst write and keeps per-write reasons', () => {
    const g = guard({
      writes: [{ scope: 'status' }, { scope: 'create-child' }, hibernate],
      evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]),
    })
    expect(g.perWrite.map((w) => w.level)).toEqual(['none', 'info', 'will-revert'])
    expect(g.level).toBe('will-revert')
  })
})

describe('canConfirmGitOpsWrite', () => {
  it('blocks while pending and until acknowledged', () => {
    const managed = guard({ writes: [hibernate], evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) })
    expect(canConfirmGitOpsWrite(managed, false)).toBe(false)
    expect(canConfirmGitOpsWrite(managed, true)).toBe(true)
    expect(canConfirmGitOpsWrite({ ...managed, pending: true }, true)).toBe(false)
    expect(canConfirmGitOpsWrite(undefined, true)).toBe(false)
    expect(canConfirmGitOpsWrite(evaluateGitOpsWriteGuard({ target, owner: null, writes: [hibernate] }), false)).toBe(true)
  })
})

describe('gitOpsWriteEvidencePaths', () => {
  it('collects field paths of metadata/spec writes only', () => {
    expect(
      gitOpsWriteEvidencePaths([
        hibernate,
        { scope: 'spec', paths: ['spec.suspend', HIBERNATE] },
        { scope: 'status', paths: ['status.phase'] },
      ]),
    ).toEqual([HIBERNATE, 'spec.suspend'])
  })
})

describe('gitOpsWriteGuardKey', () => {
  it('changes with the verdict and stays put for the same one', () => {
    const mayRevert = guard({ writes: [hibernate], evidence: evidence(manual, [pathEvidence({ lastApplied: 'present' })]) })
    const willRevert = guard({ writes: [hibernate], evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) })
    expect(gitOpsWriteGuardKey(mayRevert)).not.toBe('')
    expect(gitOpsWriteGuardKey(mayRevert)).toBe(gitOpsWriteGuardKey(guard({ writes: [hibernate], evidence: evidence(manual, [pathEvidence({ lastApplied: 'present' })]) })))
    expect(gitOpsWriteGuardKey(mayRevert)).not.toBe(gitOpsWriteGuardKey(willRevert))
    expect(gitOpsWriteGuardKey(guard({ writes: [hibernate], ownerPending: true }))).toBe('')
    expect(gitOpsWriteGuardKey(undefined)).toBe('')
  })
})

describe('copy', () => {
  it('never promises a change will not be reverted', () => {
    const optedOut = evidence({ ...selfHeal, objectReconcile: 'ignore' }, [pathEvidence({ lastApplied: 'present' })])
    const ignored = evidence({ ...selfHeal, respectIgnoreDifferences: true }, [pathEvidence({ lastApplied: 'present', ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' })])
    for (const g of [
      guard({ writes: [hibernate], evidence: optedOut }),
      guard({ writes: [hibernate], evidence: ignored }),
      guard({ writes: [{ scope: 'create-child', description: 'A Backup' }] }),
      guard({ writes: [{ scope: 'delete' }], evidence: optedOut }),
    ]) {
      expect(g.level).toBe('info')
      for (const w of g.perWrite) expect(w.reason).not.toMatch(/won.t/)
    }
  })

  it('describes a manager rule that only partly covers the field as unconfirmed', () => {
    const g = guard({
      writes: [hibernate],
      evidence: evidence({ ...selfHeal, respectIgnoreDifferences: true }, [
        pathEvidence({ lastApplied: 'present', ignored: 'unevaluated', ignoredBy: 'spec.ignoreDifferences managedFieldsManagers (the manager owns only part of this field)' }),
      ]),
    })
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
    expect(g.perWrite[0].reason).toContain("can't confirm covers this field")
    expect(g.perWrite[0].reason).not.toContain('jq')
  })
})
