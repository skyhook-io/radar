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
  gitOpsWriteHeadline,
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

    it('no evidence at all ⇒ may-revert, and says why', () => {
      const g = guard({ writes: [hibernate], evidenceError: 'GitOps revert warnings need a newer Radar' })
      expect(g.level).toBe('may-revert')
      expect(g.requiresAck).toBe(true)
      expect(g.perWrite[0].reason).toContain("can't see which fields the GitOps source declares (GitOps revert warnings need a newer Radar)")
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
    const why = "Radar couldn't read this resource, so it can't tell whether a GitOps tool or Helm manages it."
    const g = evaluateGitOpsWriteGuard({ target, owner: null, ownershipError: why, writes: [hibernate] })
    expect(g.requiresAck).toBe(true)
    expect(g.summary).toBe(`A GitOps tool may revert this change. ${why}`)
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

  it("doesn't let an object's opt-out waive a change whose fields are unknown", () => {
    const optedOut = evidence({ ...selfHeal, objectReconcile: 'ignore' })
    const g = guard({ writes: [{ scope: 'spec' }], evidence: optedOut })
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
  })

  it('describes a rule that covers only some of the entries as unconfirmed', () => {
    const g = guard({
      writes: [hibernate],
      evidence: evidence({ ...selfHeal, respectIgnoreDifferences: true }, [
        pathEvidence({ lastApplied: 'present', ignored: 'unevaluated', ignoredBy: 'spec.ignoreDifferences jsonPointers' }),
      ]),
    })
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
    expect(g.perWrite[0].reason).toBe(
      "Radar can't confirm the Application's ignore rule (spec.ignoreDifferences jsonPointers) covers this field, so the next sync may overwrite it.",
    )
  })

  it('a wildcard the source declares for only some entries may revert, not will', () => {
    const partial = guard({ writes: [hibernate], evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', partial: true })]) })
    expect(partial.level).toBe('may-revert')
    expect(partial.requiresAck).toBe(true)
    expect(partial.perWrite[0].reason).toBe('The GitOps source sets this for some containers but not others; the ones it sets will be overwritten at the next sync.')
    const whole = guard({ writes: [hibernate], evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) })
    expect(whole.level).toBe('will-revert')
  })

  it('a path that matches nothing in the live object adds no verdict', () => {
    const g = guard({
      writes: [{ scope: 'spec', paths: ['spec.template.spec.containers[*].image', 'spec.template.spec.initContainers[*].image'] }],
      evidence: evidence({ ...selfHeal, respectIgnoreDifferences: true }, [
        pathEvidence({ path: 'spec.template.spec.containers[*].image', lastApplied: 'present', ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' }),
        pathEvidence({ path: 'spec.template.spec.initContainers[*].image', empty: true }),
      ]),
    })
    expect(g.level).toBe('info')
    expect(g.requiresAck).toBe(false)
  })

  it('names the owner once, in the headline', () => {
    for (const ev of [
      evidence({ ...selfHeal, respectIgnoreDifferences: true }, [pathEvidence({ lastApplied: 'present', ignored: 'effective', ignoredBy: 'spec.ignoreDifferences' })]),
      evidence({ ...selfHeal, suspended: true }, [pathEvidence({ lastApplied: 'present' })]),
      evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored: 'comparison-only', ignoredBy: 'spec.ignoreDifferences' })]),
    ]) {
      const g = guard({ writes: [hibernate], evidence: ev })
      expect(g.summary.split('argocd/pg').length - 1).toBe(1)
    }
  })
})

describe('manager answer composition', () => {
  const a = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
  const b = { ...a, name: 'b' }
  const subject = { kind: 'Pod', namespace: 'db', name: 'pg-1' }
  const parent = { kind: 'Deployment', group: 'apps', namespace: 'db', name: 'pg' }
  const claim = { manager: a, source: 'argo-instance-label' as const, check: 'omitted' as const, weak: true }
  const shared: import('../types/core').ManagerAnswer = { state: 'shared', lead: a, direct: { object: subject, recorders: [a, b] } }
  const via: import('../types/core').ManagerAnswer = { state: 'listed', lead: b, direct: { object: subject }, via: { object: parent, recorders: [b] } }
  const run = (manager: import('../types/core').ManagerAnswer, writes = [hibernate], extra: Partial<GitOpsWriteGuardInput> = {}) =>
    guard({ manager, writes, evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored: 'effective' })]), ...extra })

  it('disagree field writes warn with both names despite a self-heal policy', () => {
    const g = run({ state: 'disagree', direct: { object: subject, recorders: [b], claims: [claim] } })
    expect(g.level).toBe('may-revert')
    expect(g.summary).toContain('Application argocd/a and Application argocd/b')
    expect(g.owner).toBeNull()
  })

  it('shared field writes cannot use one candidate’s ignore rule or policy exemption', () => {
    const g = run(shared)
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
    expect(g.summary).toContain('argocd/a')
    expect(g.summary).toContain('argocd/b')
    expect(g.syncPolicy).toBeNull()
  })

  it('several claims without a lead warn in the plural', () => {
    const g = run({ state: 'claimed', direct: { object: subject, claims: [claim, { ...claim, manager: b }] } })
    expect(g.level).toBe('may-revert')
    expect(g.summary).toContain('argocd/a and Application argocd/b')
  })

  it.each(['metadata', 'spec', 'delete'] as const)('via without an active direct claim makes %s informational without acknowledgment', scope => {
    const g = run({ ...via, direct: { object: subject, claims: [{ ...claim, copied: true }] } }, [{ scope, paths: [HIBERNATE] }])
    expect(g.level).toBe('info')
    expect(g.requiresAck).toBe(false)
    expect(g.summary).toContain('Deployment db/pg')
    expect(g.summary).toContain('Application argocd/b')
    if (scope === 'delete') expect(g.summary).toContain('controller recreates it from')
  })

  it('native Helm annotations do not compete with a GitOps ancestor', () => {
    const helm = { kind: 'HelmRelease', group: '', namespace: 'db', name: 'chart' }
    const g = run({ ...via, direct: { object: subject, claims: [{ manager: helm, source: 'helm-release', check: 'not-recording', weak: true }] } })
    expect(g.level).toBe('info')
    expect(g.requiresAck).toBe(false)
    expect(g.summary).not.toContain('HelmRelease')
  })

  it.each(['disagree', 'shared', 'claimed'] as const)('%s deletes without a lead warn with every candidate', state => {
    const direct = state === 'claimed' ? { object: subject, claims: [claim, { ...claim, manager: b }] } : { object: subject, recorders: state === 'shared' ? [a, b] : [b], claims: [claim] }
    const g = run({ state, direct }, [{ scope: 'delete' }])
    expect(g.level).toBe('may-revert')
    expect(g.summary).toContain('Application argocd/a')
    expect(g.summary).toContain('Application argocd/b')
  })

  it('via with a weak non-copied direct claim keeps the field warning', () => {
    const g = run({ ...via, direct: { object: subject, claims: [claim] } })
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
    expect(g.summary).toContain('argocd/a')
    expect(g.summary).toContain('Deployment db/pg')
  })

  it.each([shared, via])('status writes remain none', manager => {
    expect(run(manager, [{ scope: 'status' }]).level).toBe('none')
  })

  it('shared create-child stays informational', () => {
    expect(run(shared, [{ scope: 'create-child' }]).level).toBe('info')
  })

  it('operator-owned delete stays informational', () => {
    expect(run(shared, [{ scope: 'delete' }], { operatorOwner: { kind: 'Cluster', name: 'pg' } }).level).toBe('info')
  })

  it.each([shared, via])('empty wildcard stays none', manager => {
    expect(run(manager, [hibernate], { evidence: evidence(selfHeal, [pathEvidence({ empty: true })]) }).level).toBe('none')
  })

  it('a single agreeing candidate with declared field evidence can still prove will-revert', () => {
    const manager = { state: 'agree' as const, lead: a, direct: { object: subject, recorders: [a], claims: [{ manager: a, source: 'argo-tracking-id' as const, check: 'recorded' as const }] } }
    expect(run(manager, [hibernate], { evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) }).level).toBe('will-revert')
  })

  // A list alone can name an object its manager never syncs: a copy carrying
  // another object's tracking ID, a CRD a HelmRelease skips.
  it.each([hibernate, { scope: 'delete' as const }])('a list without metadata naming its manager promises no revert (%o)', write => {
    const manager = { state: 'listed' as const, lead: a, direct: { object: subject, recorders: [a], claims: [{ manager: b, source: 'argo-tracking-id' as const, check: 'missing' as const, copied: true }] } }
    const g = run(manager, [write], { evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) })
    expect(g.level).toBe('may-revert')
    expect(g.summary).toContain("Radar can't confirm Application argocd/a applies this object")
    // The field evidence stays, behind the condition.
    expect(g.summary).toMatch(/If it does: .+/)
  })

  it('a parent the managers disagree on says which names it and which lists it', () => {
    const g = run({ state: 'disagree', direct: { object: subject }, via: { object: parent, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } })
    expect(g.level).toBe('info')
    expect(g.summary).toContain('whose metadata names Application argocd/a, while Application argocd/b lists it in its status')
  })

  // A CronJob's Job runs once: deleting it, even one created by hand, gets no
  // replacement, whatever manages the CronJob.
  it("a CronJob's Job is not said to be recreated", () => {
    const cron = { kind: 'CronJob', group: 'batch', namespace: 'db', name: 'backup' }
    const g = run({ state: 'agree', lead: a, direct: { object: subject }, via: { object: cron, recorders: [a], claims: [{ manager: a, source: 'argo-tracking-id', check: 'recorded' }] } }, [{ scope: 'delete' }], { operatorOwner: { kind: 'CronJob', name: 'backup' } })
    expect(g.level).toBe('info')
    expect(g.summary).toContain('nothing is expected to recreate it')
    expect(g.summary).toContain('CronJob db/backup can still create new ones on its schedule')
    expect(g.summary).not.toContain('recreates it')
  })

  // A newer Radar's state with no lead, or one candidate of a kind this UI
  // can't open, still warns.
  it.each([
    ['an unfamiliar state without a lead', { state: 'future-state' as never, direct: { object: subject, recorders: [a] } }],
    ['an unfamiliar manager kind', { state: 'claimed' as const, lead: { kind: 'Stack', group: 'example.io', namespace: 'x', name: 's' }, direct: { object: subject, claims: [{ manager: { kind: 'Stack', group: 'example.io', namespace: 'x', name: 's' }, source: 'argo-tracking-id' as never, check: 'omitted' as const }] } }],
  ])('%s still warns on field writes and deletes', (_, manager) => {
    for (const write of [hibernate, { scope: 'delete' as const }]) {
      const g = run(manager, [write])
      expect(g.level).toBe('may-revert')
      expect(g.requiresAck).toBe(true)
    }
  })

  it('a chain end this UI does not know keeps the conservative warning', () => {
    const g = run({ state: 'unknown', direct: { object: subject }, chainEnd: 'future-stop' as never }, [hibernate])
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
  })

  it('an agreeing answer whose manager kind this UI does not know still warns', () => {
    const stack = { kind: 'Stack', group: 'example.io', namespace: 'x', name: 's' }
    for (const write of [hibernate, { scope: 'delete' as const }]) {
      const g = run({ state: 'agree', lead: stack, direct: { object: subject, recorders: [stack], claims: [{ manager: stack, source: 'argo-tracking-id', check: 'recorded' }] } }, [write])
      expect(g.level).toBe('may-revert')
    }
  })

  // A fallback via whose walk stopped at an unreadable owner may have missed
  // a manager above it: a delete isn't just informational.
  it('a delete through a fallback via with an unread chain keeps the warning', () => {
    const g = run({ state: 'claimed', lead: a, direct: { object: subject }, via: { object: parent, claims: [{ manager: a, source: 'argo-instance-label', check: 'omitted', weak: true }] }, chainEnd: 'unfollowed', stop: { at: parent, state: 'unknown' } }, [{ scope: 'delete' }])
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
  })

  it('a state this UI does not know is read as claimed', () => {
    const g = run({ state: 'future-state' as never, lead: a, direct: { object: subject, recorders: [a] } }, [hibernate], { evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) })
    expect(g.level).toBe('may-revert')
  })

  // The schedule belongs to the Job's own CronJob, even when the answer came
  // from an ancestor above it.
  it("names the Job's own CronJob for future runs, not the answering ancestor", () => {
    const op = { kind: 'Widget', group: 'example.io', namespace: 'db', name: 'pg' }
    const g = run({ state: 'agree', lead: a, direct: { object: subject }, via: { object: op, recorders: [a], claims: [{ manager: a, source: 'argo-tracking-id', check: 'recorded' }] } }, [{ scope: 'delete' }], { operatorOwner: { kind: 'CronJob', name: 'backup' } })
    expect(g.summary).toContain('but CronJob ')
    expect(g.summary).toContain('backup can still create new ones on its schedule')
    expect(g.summary).not.toContain('Widget db/pg can still')
  })

  it('an informational note on a list-only answer says listed, not managed', () => {
    const g = run({ state: 'listed', lead: a, direct: { object: subject, recorders: [a] } }, [{ scope: 'create-child' }])
    expect(g.level).toBe('info')
    expect(gitOpsWriteHeadline(g)).toBe('Listed by Argo CD Application argocd/a.')
  })

  it('a parent the metadata alone names is not said to be managed', () => {
    const g = run({ state: 'claimed', lead: a, direct: { object: subject }, via: { object: parent, claims: [{ manager: a, source: 'argo-tracking-id', check: 'missing' }] } })
    expect(g.level).toBe('info')
    expect(g.summary).toContain('whose metadata names Application argocd/a')
    expect(g.summary).not.toContain('manages')
  })

  it('an adopted Helm release keeps its upgrade risk past an Argo CD ignore rule', () => {
    const helm = { kind: 'HelmRelease', group: '', namespace: 'db', name: 'chart' }
    const manager = { state: 'agree' as const, lead: a, direct: { object: subject, recorders: [a], claims: [
      { manager: a, source: 'argo-tracking-id' as const, check: 'recorded' as const },
      { manager: helm, source: 'helm-release' as const, check: 'not-recording' as const },
    ] } }
    const g = run(manager)
    expect(g.level).toBe('may-revert')
    expect(g.summary).toContain('Helm release db/chart')
  })

  const agree = { state: 'agree' as const, lead: a, direct: { object: subject, recorders: [a], claims: [{ manager: a, source: 'argo-tracking-id' as const, check: 'recorded' as const }] } }

  it('a refused live answer drops the cached answer and the owner hint', () => {
    const g = run(agree, [hibernate], { evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored: 'effective' })], { managerUnavailable: 'objects_changed' }) })
    expect(g.level).toBe('may-revert')
    expect(g.owner).toBeNull()
    expect(g.summary).toContain("Radar couldn't check what manages this resource right now (the read failed)")
    expect(g.summary).not.toContain('objects_changed')
  })

  it.each([
    ['kind_sync_pending', '(still loading)', true],
    ['read_error', '(the read failed)', true],
    ['user_forbidden', '(you may not list it)', false],
    // Spellings an older Radar sends read as the code they became.
    ['kind_sync_failed', '(loading is overdue)', true],
    ['rbac_denied', '(you may not list it)', false],
  ])('a refused live answer explains %s in words', (code, words, reopen) => {
    const g = run(agree, [hibernate], { evidence: evidence(selfHeal, [], { managerUnavailable: code }) })
    expect(g.summary).toContain(words)
    expect(g.summary).not.toContain(code)
    expect(g.summary.includes('Close and reopen this dialog to check again.')).toBe(reopen)
  })

  it("a failed request keeps the request's own message", () => {
    const g = run(agree, [hibernate], { evidence: undefined, evidenceError: 'Request timed out' })
    expect(g.summary).toContain('(Request timed out)')
    expect(g.summary).not.toContain('reopen')
  })

  it("a failed live check doesn't keep a cached via answer's reassurance", () => {
    const g = run(via, [hibernate], { evidence: undefined, evidenceError: 'forbidden' })
    expect(g.level).toBe('may-revert')
    expect(g.requiresAck).toBe(true)
  })

  it('a failed live check of a cached none stays quiet', () => {
    const g = run({ state: 'none', direct: { object: subject } }, [hibernate], { evidence: undefined, evidenceError: 'timeout' })
    expect(g.level).toBe('none')
  })

  it.each(['unknown', 'none'] as const)('server %s replaces client owner, Helm and manager inputs', state => {
    const negative = { state, direct: { object: subject } }
    const g = run(shared, [hibernate], { helmRelease: { namespace: 'db', name: 'old' }, evidence: evidence(selfHeal, [], { manager: negative }) })
    expect(g.level).toBe('none')
    expect(g.owner).toBeNull()
    expect(g.helmRelease).toBeNull()
  })

  it('a stopped ancestor read retains conservative ownershipError behavior', () => {
    const g = run({ state: 'unknown', direct: { object: subject }, stop: { at: subject, state: 'unknown', owner: parent } })
    expect(g.level).toBe('may-revert')
    expect(g.ownershipError).toContain("couldn't follow")
  })

  // A gone or replaced owner ends the chain with an answer: nothing above it
  // was missed, so an object no manager names gets no warning.
  it.each(['gone', 'replaced'] as const)('a walk ended by a %s owner is not an unread chain', state => {
    const g = run({ state: 'none', direct: { object: subject }, chainEnd: 'unfollowed', stop: { at: subject, state, owner: parent } })
    expect(g.level).toBe('none')
    expect(g.ownershipError).toBeNull()
  })

  it('a stopped walk with a weak claim cannot use that candidate’s exemption', () => {
    const g = run({ state: 'claimed', lead: a, direct: { object: subject, claims: [claim] }, stop: { at: subject, state: 'unknown', owner: parent } })
    expect(g.level).toBe('may-revert')
    expect(g.ownershipError).toContain("couldn't follow")
  })

  it('a failed target read retains conservative ownershipError behavior', () => {
    const g = run({ state: 'unknown', direct: { object: subject } }, [hibernate], { ownershipError: 'Target read failed' })
    expect(g.level).toBe('may-revert')
    expect(g.ownershipError).toBe('Target read failed')
  })

  it('absent manager preserves legacy evidence verdicts', () => {
    for (const policy of [selfHeal, manual, null]) {
      const input = { writes: [hibernate], evidence: evidence(policy, [pathEvidence({ lastApplied: 'present' })]) }
      expect(guard({ ...input, manager: undefined })).toEqual(guard(input))
    }
  })
})


// A ReplicaSet carries its Helm-installed Deployment's release annotations;
// they are the Deployment's, so the ReplicaSet gets the generated-from note.
it('a ReplicaSet of a plain Helm Deployment is generated from it, not claimed', () => {
  const rs = { kind: 'ReplicaSet', group: 'apps', namespace: 'db', name: 'web-1' }
  const release = { kind: 'HelmRelease', namespace: 'db', name: 'web' }
  const g = guard({
    target: rs,
    manager: { state: 'claimed', lead: release,
      direct: { object: rs, claims: [{ manager: release, source: 'helm-release', check: 'not-recording', weak: true }] },
      via: { object: { kind: 'Deployment', group: 'apps', namespace: 'db', name: 'web' }, claims: [{ manager: release, source: 'helm-release', check: 'not-recording' }] } },
    writes: [hibernate],
  })
  expect(g.level).toBe('info')
  expect(g.requiresAck).toBe(false)
})

describe('unconfirmed manager boundaries', () => {
  const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'candidate' }
  const claimed: import('../types/core').ManagerAnswer = { state: 'claimed', lead: app, direct: { object: target, claims: [{ manager: app, source: 'argo-tracking-id', check: 'omitted' }] } }
  it.each(['omitted', 'remote', 'unreadable', 'withheld', 'missing'] as const)('%s cannot prove a revert or waive acknowledgment', check => {
    const manager = { ...claimed, direct: { object: target, claims: [{ ...claimed.direct.claims![0], check }] } }
    for (const ignored of [undefined, 'effective'] as const) {
      const g = guard({ manager, writes: [hibernate, { scope: 'delete' }], evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present', ignored })]) })
      expect(g.perWrite.map(w => w.level)).toEqual(['may-revert', 'may-revert'])
      expect(g.requiresAck).toBe(true)
      expect(g.syncPolicy).toBeNull()
    }
  })
  // An opt-out on the target's own annotations holds whether or not the
  // claimed manager's list names it.
  it('an unconfirmed claim keeps an opt-out on the target itself', () => {
    const g = guard({ manager: claimed, writes: [hibernate, { scope: 'delete' }], evidence: evidence({ ...selfHeal, objectReconcile: 'ignore' }, [pathEvidence({ lastApplied: 'present' })]) })
    expect(g.perWrite.map(w => w.level)).toEqual(['info', 'info'])
    expect(g.requiresAck).toBe(false)
  })
  // A tracking ID and an instance label on one Application are one manager.
  it('a namespaced and an unqualified claim on one Application are one candidate', () => {
    const manager = { ...claimed, direct: { object: target, claims: [
      { manager: app, source: 'argo-tracking-id' as const, check: 'omitted' as const },
      { manager: { ...app, namespace: '' }, source: 'argo-instance-label' as const, check: 'unresolved' as const },
    ] } }
    const g = guard({ manager, writes: [hibernate], evidence: evidence(selfHeal, [pathEvidence({ lastApplied: 'present' })]) })
    expect(g.summary).not.toContain(' and ')
    expect(g.summary).not.toContain("A single manager's ignore rules")
  })
  it.each(['cycle', 'truncated'] as const)('%s ancestry remains conservative without a stop object', chainEnd => {
    const g = guard({ manager: { state: 'unknown', direct: { object: target }, chainEnd }, writes: [hibernate, { scope: 'delete' }] })
    expect(g.perWrite.map(w => w.level)).toEqual(['may-revert', 'may-revert'])
    expect(g.ownershipError).toContain("couldn't follow")
  })
  it('stopped claimed deletes retain the floor after the operator rule', () => {
    const manager = { ...claimed, stop: { at: target, state: 'unknown' } }
    const input = { manager, writes: [{ scope: 'delete' as const }], evidence: evidence(selfHeal) }
    expect(guard(input).perWrite[0].reason).toContain("couldn't follow")
    expect(guard({ ...input, operatorOwner: { kind: 'Cluster', name: 'pg' } }).level).toBe('info')
  })
})
