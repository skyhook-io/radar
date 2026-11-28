import { describe, expect, it } from 'vitest'
import {
  describeFreightWait,
  explainFreightNotInStage,
  formatFreightLabel,
  formatFreightVersions,
  getFreightAlias,
  getFreightSince,
  getFreightVersions,
  getKargoColumnValue,
  getKargoStatus,
  getPipelineOrder,
  getPromotionError,
  getPromotionPhaseTone,
  getPromotionSteps,
  getStageArgoCDApps,
  getStageCurrentFreight,
  getStageStatus,
  getWarehouseStatus,
  getWarehouseSubscriptions,
  isKargoKind,
  formatGoDurationWords,
  parseGoDuration,
  repoShortName,
  sortFreightNewestFirst,
  type KargoFreight,
  type KargoStage,
} from './resource-utils-kargo'
import { hasCuratedColumns } from './ResourcesView'
import {
  NEW_FREIGHT,
  NOW,
  OLD_FREIGHT,
  erroredPromotion,
  failedPromotion,
  newFreight,
  oldFreight,
  prodStage,
  testStage,
  uatStage,
  warehouse,
} from './kargo-test-fixtures'

const withStatus = (stage: KargoStage, status: NonNullable<KargoStage['status']>): KargoStage => ({ ...stage, status: { ...stage.status, ...status } })
const withFreightStatus = (f: KargoFreight, status: NonNullable<KargoFreight['status']>): KargoFreight => ({ ...f, status })

describe('identity', () => {
  it('claims only the exact kargo.akuity.io group', () => {
    expect(isKargoKind(testStage, 'Stage')).toBe(true)
    expect(isKargoKind({ ...testStage, apiVersion: 'tekton.dev/v1' }, 'Stage')).toBe(false)
    expect(isKargoKind({ ...testStage, apiVersion: 'sub.kargo.akuity.io/v1alpha1' }, 'Stage')).toBe(false)
    expect(getKargoStatus({ apiVersion: 'example.com/v1', kind: 'Stage' })).toBeNull()
  })
})

describe('freight display', () => {
  it('names Freight by alias and versions, never by its SHA', () => {
    expect(getFreightAlias(newFreight)).toBe('illocutionary-skunk')
    expect(getFreightAlias({ metadata: { labels: { 'kargo.akuity.io/alias': 'from-label' } } })).toBe('from-label')
    expect(formatFreightVersions(newFreight)).toBe('nginx:1.31.10')
    expect(formatFreightLabel(newFreight, 'illocutionary-skunk')).toBe('illocutionary-skunk (nginx:1.31.10)')
    expect(formatFreightLabel({ name: NEW_FREIGHT })).toBe('b56f6e3')
  })

  it('reads commits, charts and digests as short versions', () => {
    const versions = getFreightVersions({
      commits: [{ repoURL: 'https://git.example.com/acme/config.git', id: 'abcdef1234567' }, { repoURL: 'https://git.example.com/acme/app', id: 'ffff', tag: 'v2.0.0' }],
      charts: [{ repoURL: 'oci://ghcr.io/acme/charts', name: 'web', version: '0.4.0' }],
      images: [{ repoURL: 'ghcr.io/acme/api', digest: 'sha256:1234567890abcdef' }],
    })
    expect(versions.map(v => `${v.type}:${v.source}:${v.version}`)).toEqual([
      'commit:config:abcdef1', 'commit:app:v2.0.0', 'chart:web:0.4.0', 'image:api:@1234567',
    ])
  })

  // As Kargo's timeline does: by discovery, so Freight added by hand after an
  // automatic one sorts first even when it carries the older version.
  it('orders Freight newest first by discovery time', () => {
    expect(sortFreightNewestFirst([newFreight, oldFreight]).map(f => f.alias)).toEqual(['fallacious-robin', 'illocutionary-skunk'])
  })
})

describe('stage reading', () => {
  it('takes what a Stage holds from freightHistory[0], one entry per Warehouse', () => {
    const current = getStageCurrentFreight(uatStage, { [NEW_FREIGHT]: newFreight })
    expect(current).toHaveLength(1)
    expect(current[0]).toMatchObject({ origin: 'kargo-demo', name: NEW_FREIGHT, alias: 'illocutionary-skunk' })
    expect(getStageCurrentFreight(uatStage)[0].alias).toBeUndefined()
  })

  it('shows every origin when a Stage holds Freight from several Warehouses', () => {
    const multi = withStatus(testStage, {
      freightHistory: [{ items: { 'Warehouse/b-charts': { name: 'b', origin: { kind: 'Warehouse', name: 'b-charts' }, charts: [{ name: 'web', version: '1.0.0' }] }, 'Warehouse/a-images': { name: 'a', origin: { kind: 'Warehouse', name: 'a-images' }, images: [{ repoURL: 'x/api', tag: '2' }] } } }],
    })
    expect(getStageCurrentFreight(multi).map(f => f.origin)).toEqual(['a-images', 'b-charts'])
    expect(getKargoColumnValue(multi, 'kargoFreight')).toBe('api:2; web 1.0.0')
  })

  it('takes "since" only from Freight.currentlyIn, unknown otherwise', () => {
    expect(getFreightSince(newFreight, 'uat')).toBe('2026-11-25T13:09:05Z')
    expect(getFreightSince(newFreight, 'prod')).toBeUndefined()
  })

  it('reads Argo CD apps from the annotation and the health output', () => {
    expect(getStageArgoCDApps(prodStage)).toEqual([{ name: 'kargo-demo-prod', namespace: 'argocd', health: 'Healthy', healthMessage: undefined, sync: 'Synced' }])
    // test's last promotion errored, so Kargo wrote no health output; the annotation still names the app.
    expect(getStageArgoCDApps(testStage)).toEqual([{ name: 'kargo-demo-test', namespace: 'argocd' }])
    expect(getStageArgoCDApps({ metadata: { annotations: { 'kargo.akuity.io/argocd-context': 'not json' } } })).toEqual([])
  })

  it.each([
    [prodStage, 'Ready', 'healthy'],
    [testStage, 'Promotion errored', 'unhealthy'],
    [uatStage, 'Verification failed', 'unhealthy'],
    [withStatus(prodStage, { conditions: [{ type: 'Ready', status: 'False', reason: 'NoFreight' }] }), 'No Freight yet', 'neutral'],
    [withStatus(testStage, { conditions: [{ type: 'Promoting', status: 'True', reason: 'ActivePromotion' }, { type: 'Ready', status: 'False', reason: 'ActivePromotion' }] }), 'Promoting', 'neutral'],
    [withStatus(testStage, { conditions: [{ type: 'Ready', status: 'False', reason: 'WaitingForVerification' }] }), 'Verifying', 'neutral'],
    [withStatus(testStage, { conditions: [{ type: 'Ready', status: 'False', reason: 'LastPromotionAborted' }] }), 'Promotion aborted', 'neutral'],
    [withStatus(testStage, { conditions: [{ type: 'Ready', status: 'False', reason: 'Unhealthy' }] }), 'Unhealthy', 'unhealthy'],
    [{ apiVersion: 'kargo.akuity.io/v1alpha1', kind: 'Stage' }, 'Not reported', 'unknown'],
  ] as const)('reads Stage status %#', (stage, text, level) => {
    expect(getStageStatus(stage)).toMatchObject({ text, level })
  })
})

describe('promotion reading', () => {
  it('colors Failed and Errored red and a cancelled one neutral', () => {
    expect(getPromotionPhaseTone('Failed')).toBe('unhealthy')
    expect(getPromotionPhaseTone('Errored')).toBe('unhealthy')
    expect(getPromotionPhaseTone('Aborted')).toBe('neutral')
    expect(getPromotionPhaseTone('Succeeded')).toBe('healthy')
    expect(getPromotionPhaseTone(undefined)).toBe('unknown')
  })

  it('zips steps with their execution status', () => {
    const steps = getPromotionSteps(erroredPromotion)
    expect(steps.map(s => [s.alias, s.uses, s.status])).toEqual([
      ['promo-process::step-1', 'git-clone', 'Succeeded'],
      ['promo-process::step-2', 'git-clear', 'Succeeded'],
      ['promo-process::update', 'kustomize-set-image', 'Succeeded'],
      ['promo-process::step-4', 'kustomize-build', 'Succeeded'],
      ['promo-process::commit', 'git-commit', 'Skipped'],
      ['promo-process::step-6', 'git-push', 'Errored'],
      ['promo-process::step-7', 'argocd-update', 'Skipped'],
    ])
  })

  it('takes the error from the first failed step that does not continue on error', () => {
    expect(getPromotionError(erroredPromotion)?.step?.uses).toBe('git-push')
    expect(getPromotionError(erroredPromotion)?.message).toContain('could not read Username')
    expect(getPromotionError(failedPromotion)).toMatchObject({ step: { alias: 'release-gate' }, message: 'step "release-gate": failed: change freeze in effect for prod' })

    const skipsContinue = {
      ...failedPromotion,
      status: {
        ...failedPromotion.status,
        stepExecutionMetadata: [
          { alias: 'a', status: 'Failed', message: 'ignored', continueOnError: true },
          { alias: 'b', status: 'Failed', message: 'the real one' },
        ],
      },
    }
    expect(getPromotionError(skipsContinue)?.message).toBe('the real one')
  })

  it('falls back to status.message, then to nothing', () => {
    const noSteps = { ...failedPromotion, status: { phase: 'Failed', message: 'whole promotion failed' } }
    expect(getPromotionError(noSteps)).toEqual({ step: undefined, message: 'whole promotion failed' })
    expect(getPromotionError({ status: { phase: 'Failed' } })).toEqual({ step: undefined, message: undefined })
    expect(getPromotionError({ status: { phase: 'Succeeded' } })).toBeNull()
    expect(getPromotionError({ status: { phase: 'Aborted' } })).toBeNull()
  })

  it('shows the Freight in the Promotion list', () => {
    expect(getKargoColumnValue(erroredPromotion, 'kargoFreight')).toBe('nginx:1.31.10')
    expect(getKargoColumnValue(erroredPromotion, 'kargoStage')).toBe('test')
    expect(getKargoColumnValue(erroredPromotion, 'status')).toBe('Errored')
  })
})

describe('warehouse reading', () => {
  it('lists subscriptions with their constraint', () => {
    expect(getWarehouseSubscriptions(warehouse)).toEqual([{ type: 'image', name: undefined, repoURL: 'public.ecr.aws/nginx/nginx', constraint: '^1.29.0' }])
    expect(getWarehouseSubscriptions({ spec: { subscriptions: [{ git: { repoURL: 'https://g/r.git', branch: 'main' } }, { chart: { repoURL: 'oci://c', name: 'web' } }] } }).map(s => s.constraint))
      .toEqual(['branch main', undefined])
  })

  it('reads a discovery failure as unhealthy and discovery in progress as neutral', () => {
    expect(getWarehouseStatus(warehouse)).toMatchObject({ text: 'Ready', level: 'healthy' })
    expect(getWarehouseStatus({ status: { conditions: [{ type: 'Healthy', status: 'False', reason: 'DiscoveryFailed', message: 'auth required' }, { type: 'Ready', status: 'False', reason: 'DiscoveryFailure' }] } }))
      .toMatchObject({ text: 'DiscoveryFailed', level: 'unhealthy' })
    expect(getWarehouseStatus({ status: { conditions: [{ type: 'Ready', status: 'False', reason: 'DiscoveryInProgress' }] } }))
      .toMatchObject({ text: 'Discovering', level: 'neutral' })
  })
})

describe('pipeline order', () => {
  it('orders Stages by their upstream sources', () => {
    expect(getPipelineOrder([prodStage, uatStage, testStage]).map(s => [s.name, s.depth])).toEqual([['test', 0], ['uat', 1], ['prod', 2]])
    expect(getPipelineOrder([testStage])[0].warehouses).toEqual(['kargo-demo'])
  })

  it('keeps a missing upstream visible and survives a cycle', () => {
    const orphan: KargoStage = { metadata: { name: 'orphan' }, spec: { requestedFreight: [{ origin: { name: 'w' }, sources: { stages: ['gone'] } }] } }
    expect(getPipelineOrder([orphan])[0]).toMatchObject({ depth: 0, missingUpstream: ['gone'] })
    const a: KargoStage = { metadata: { name: 'a' }, spec: { requestedFreight: [{ origin: { name: 'w' }, sources: { stages: ['b'] } }] } }
    const b: KargoStage = { metadata: { name: 'b' }, spec: { requestedFreight: [{ origin: { name: 'w' }, sources: { stages: ['a'] } }] } }
    expect(getPipelineOrder([a, b])).toHaveLength(2)
  })
})

describe('why the newest Freight is not in a Stage yet', () => {
  const kargoOrigin = { kind: 'Warehouse', name: 'kargo-demo' }
  const prodFrom = (sources: NonNullable<KargoStage['spec']>['requestedFreight']): KargoStage => ({ ...prodStage, spec: { ...prodStage.spec, requestedFreight: sources } })
  const verifiedInUat = withFreightStatus(newFreight, { ...newFreight.status, verifiedIn: { ...newFreight.status?.verifiedIn, uat: { verifiedAt: '2026-11-25T13:09:30Z' } } })

  it('says the Freight is already there', () => {
    expect(explainFreightNotInStage(uatStage, newFreight, NOW)).toEqual({ kind: 'here' })
    expect(explainFreightNotInStage(prodStage, oldFreight, NOW)).toEqual({ kind: 'here' })
  })

  it('reports an upstream Stage that has not verified it', () => {
    const wait = explainFreightNotInStage(prodStage, newFreight, NOW)
    expect(wait).toEqual({ kind: 'not-verified-upstream', stages: ['uat'], strategy: 'OneOf' })
    expect(describeFreightWait(wait).text).toBe('Not verified in uat yet.')
  })

  it('reports the failed last promotion of this Freight', () => {
    const stage = withStatus(prodStage, {
      freightHistory: [],
      lastPromotion: { name: failedPromotion.metadata?.name, freight: failedPromotion.status?.freight, status: { phase: 'Failed', message: failedPromotion.status?.message } },
    })
    const wait = explainFreightNotInStage(stage, oldFreight, NOW)
    expect(wait).toMatchObject({ kind: 'last-promotion-failed', phase: 'Failed' })
    expect(describeFreightWait(wait)).toEqual({ text: 'The last promotion of this Freight failed: step "release-gate": failed: change freeze in effect for prod', tone: 'unhealthy' })
  })

  it('needs every upstream Stage under All, any one under OneOf', () => {
    const both = (strategy: 'All' | 'OneOf') => prodFrom([{ origin: kargoOrigin, sources: { stages: ['test', 'uat'], availabilityStrategy: strategy } }])
    expect(explainFreightNotInStage(both('All'), newFreight, NOW)).toEqual({ kind: 'not-verified-upstream', stages: ['uat'], strategy: 'All' })
    expect(explainFreightNotInStage(both('OneOf'), newFreight, NOW)).toEqual({ kind: 'manual-promotion' })
  })

  it('reports the soak time left, counting time spent in the upstream Stage', () => {
    const wait = explainFreightNotInStage(prodFrom([{ origin: kargoOrigin, sources: { stages: ['uat'], requiredSoakTime: '1h0m0s' } }]), verifiedInUat, NOW)
    // In uat since 13:09:05, so 55s of the hour are done at 13:10:00.
    expect(wait).toEqual({ kind: 'soaking', stage: 'uat', remainingMs: 3_600_000 - 55_000 })
    expect(describeFreightWait(wait).text).toBe('Soaking in uat, 1h left.')
  })

  it('counts a longer completed soak recorded on the Freight', () => {
    const stage = prodFrom([{ origin: kargoOrigin, sources: { stages: ['uat'], requiredSoakTime: '30s' } }])
    expect(explainFreightNotInStage(withStatus(stage, { freightHistory: [] }), oldFreight, NOW)).toEqual({ kind: 'manual-promotion' })
  })

  it('lets an approval bypass verification', () => {
    const approved = withFreightStatus(newFreight, { ...newFreight.status, approvedFor: { prod: {} } })
    expect(explainFreightNotInStage(prodStage, approved, NOW)).toEqual({ kind: 'manual-promotion' })
  })

  it('waits for approval when the Stage names no source at all', () => {
    expect(explainFreightNotInStage(prodFrom([{ origin: kargoOrigin, sources: {} }]), newFreight, NOW)).toEqual({ kind: 'not-approved' })
  })

  it('says auto-promotion is off when the Freight is available', () => {
    // Kargo omits autoPromotionEnabled when it is false, as on every Stage of the live project.
    expect(explainFreightNotInStage(prodStage, verifiedInUat, NOW)).toEqual({ kind: 'manual-promotion' })
    expect(describeFreightWait({ kind: 'manual-promotion' }).text).toBe('Ready to promote. Auto-promotion is off, so someone has to promote it.')
  })

  const auto = withStatus(prodStage, { autoPromotionEnabled: true })
  it.each<[string, KargoStage]>([
    ['a running promotion', withStatus(auto, { currentPromotion: { name: 'p' } })],
    ['MatchUpstream', { ...auto, spec: { ...auto.spec, requestedFreight: [{ origin: kargoOrigin, sources: { stages: ['uat'], autoPromotionOptions: { selectionPolicy: 'MatchUpstream' } } }] } }],
    ['a hold', withStatus(auto, { effectiveAutoPromotionHolds: { 'Warehouse/kargo-demo': {} } })],
    ['no recorded reason', auto],
  ])('is unknown, with a reason, for %s', (_label, stage) => {
    const wait = explainFreightNotInStage(stage, verifiedInUat, NOW)
    expect(wait.kind).toBe('unknown')
    expect(describeFreightWait(wait)).toMatchObject({ tone: 'unknown' })
    expect(describeFreightWait(wait).text).toMatch(/^Unknown: .+/)
  })

  it('applies an auto-promotion hold only to its own origin, as Kargo keys holds', () => {
    const heldHere = explainFreightNotInStage(withStatus(auto, { effectiveAutoPromotionHolds: { 'Warehouse/kargo-demo': {} } }), verifiedInUat, NOW)
    expect(heldHere).toEqual({ kind: 'unknown', reason: 'Auto-promotion is on hold for this Stage.' })
    const heldElsewhere = explainFreightNotInStage(withStatus(auto, { effectiveAutoPromotionHolds: { 'Warehouse/other': {} } }), verifiedInUat, NOW)
    expect(heldElsewhere).toEqual({ kind: 'unknown', reason: 'Kargo has not promoted it yet, and the Stage does not say why.' })
  })

  it('does not apply to Freight from a Warehouse the Stage does not use', () => {
    expect(explainFreightNotInStage(prodStage, { ...newFreight, origin: { kind: 'Warehouse', name: 'other' } }, NOW)).toEqual({ kind: 'not-requested' })
  })
})

describe('repoShortName', () => {
  it.each([
    ['ghcr.io/acme/web', 'web'], ['https://github.com/acme/app.git', 'app'], ['https://github.com/acme/app//', 'app'], ['', ''],
  ])('names %s', (url, name) => expect(repoShortName(url)).toBe(name))
})

describe('parseGoDuration', () => {
  it.each([
    ['1h0m0s', 3_600_000], ['90s', 90_000], ['1.5h', 5_400_000], ['500ms', 500], ['0s', 0], ['0', 0], ['1m30s', 90_000], ['2µs', 0.002],
  ])('parses %s', (text, ms) => expect(parseGoDuration(text)).toBe(ms))
  it('rejects text that is not a duration', () => {
    expect(parseGoDuration('soon')).toBeUndefined()
    expect(parseGoDuration('1h junk')).toBeUndefined()
    expect(parseGoDuration('5')).toBeUndefined()
    expect(parseGoDuration('1.h')).toBeUndefined()
    expect(parseGoDuration('h')).toBeUndefined()
  })
})

describe('formatGoDurationWords', () => {
  it.each([
    ['5m0s', '5 minutes'], ['1m0s', '1 minute'], ['1h30m0s', '1 hour 30 minutes'], ['90s', '1 minute 30 seconds'], ['48h0m0s', '2 days'], ['0s', '0 seconds'],
  ])('reads %s as %s', (text, words) => expect(formatGoDurationWords(text)).toBe(words))
  it('keeps text it cannot put in words as written', () => {
    expect(formatGoDurationWords('soon')).toBe('soon')
    expect(formatGoDurationWords('500ms')).toBe('500ms')
    expect(formatGoDurationWords(undefined)).toBeUndefined()
  })
})

describe('curated columns', () => {
  it('curates Stage and Promotion lists only for kargo.akuity.io', () => {
    expect(hasCuratedColumns('stages', 'kargo.akuity.io')).toBe(true)
    expect(hasCuratedColumns('Stage', 'kargo.akuity.io')).toBe(true)
    expect(hasCuratedColumns('promotions', 'kargo.akuity.io')).toBe(true)
    expect(hasCuratedColumns('stages', 'tekton.dev')).toBe(false)
    expect(hasCuratedColumns('promotions', 'example.com')).toBe(false)
    // Freight and Warehouse keep Kargo's own printer columns.
    expect(hasCuratedColumns('freights', 'kargo.akuity.io')).toBe(false)
    expect(hasCuratedColumns('warehouses', 'kargo.akuity.io')).toBe(false)
  })

  it('keeps the Freight name out of the Stage cell text', () => {
    expect(getKargoColumnValue(prodStage, 'kargoFreight')).toBe('nginx:1.31.9')
    expect(getKargoColumnValue(prodStage, 'kargoFreight')).not.toContain(OLD_FREIGHT.slice(0, 7))
  })
})
