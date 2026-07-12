import { describe, it, expect } from 'vitest'
import { compareVersions, appGroupingExplainer, APP_IDENTITY_ANNOTATION, appGroupLagMessage, matchWorkloadAcrossInstances, foldAppGroups, identityEnvInferred, worstHealth, buildAppMembershipIndex, batchActivityForApp, batchRuntimeForApp, servingReadiness, type AppGroupFoldEntry, type AppRow, type AppWorkload } from './applications'

describe('batch application runtime', () => {
  it('uses the latest retained outcome instead of historical failure count', () => {
    const app: AppRow = {
      key: 'batch', name: 'batch', health: 'unhealthy', workload_class: 'job',
      workloads: [{
        kind: 'CronJob', namespace: 'demo', name: 'nightly', workload_class: 'job', health: 'unhealthy', ready: 0, desired: 0, restarts: 0,
        batch: { retainedRuns: 4, failedRuns: 1, succeededRuns: 3, latestRunName: 'nightly-new', latestRunPhase: 'Succeeded', latestFinishedAt: '2026-07-10T00:00:00Z' },
      }],
    }
    expect(batchRuntimeForApp(app)).toMatchObject({ label: 'Succeeded', health: 'healthy' })
  })

  it('does not let one workload success mask another workload latest failure', () => {
    const app: AppRow = {
      key: 'batch', name: 'batch', health: 'healthy', workload_class: 'job',
      workloads: [
        {
          kind: 'CronJob', namespace: 'demo', name: 'nightly', workload_class: 'job', health: 'healthy', ready: 0, desired: 0, restarts: 0,
          batch: { retainedRuns: 1, succeededRuns: 1, latestRunName: 'nightly-new', latestRunPhase: 'Succeeded', latestFinishedAt: '2026-07-10T01:00:00Z' },
        },
        {
          kind: 'CronWorkflow', namespace: 'demo', name: 'sync', workload_class: 'job', health: 'unhealthy', ready: 0, desired: 0, restarts: 0,
          batch: { retainedRuns: 1, failedRuns: 1, latestRunName: 'sync-old', latestRunPhase: 'Failed', latestFinishedAt: '2026-07-10T00:00:00Z' },
        },
      ],
    }

    expect(batchRuntimeForApp(app)).toEqual({
      label: 'Failed',
      health: 'unhealthy',
      detail: 'CronWorkflow/sync latest retained run sync-old failed.',
    })
  })

  it('excludes batch workloads from serving readiness', () => {
    expect(servingReadiness([
      { kind: 'Deployment', namespace: 'demo', name: 'api', workload_class: 'service', health: 'healthy', ready: 2, desired: 3, restarts: 0 },
      { kind: 'Job', namespace: 'demo', name: 'run', workload_class: 'job', health: 'healthy', ready: 1, desired: 1, restarts: 0, batch: { retainedRuns: 1 } },
    ])).toEqual({ ready: 2, desired: 3 })
  })
})

describe('compareVersions', () => {
  it('orders semver', () => {
    expect(compareVersions('1.2.0', '1.10.0')).toBe(-1)
    expect(compareVersions('v2.0.0', 'v2.0.0')).toBe(0)
  })

  // Date-stamped CI tags are the dominant shape on real clusters
  // (main_2026-03-26_05) — semver-only made promotion lag inert on them.
  it('orders same-prefix date-stamped tags by date then sequence', () => {
    expect(compareVersions('main_2026-03-26_05', 'main_2026-06-02_03')).toBe(-1)
    expect(compareVersions('main_2026-06-02_03', 'main_2026-06-02_01')).toBe(1)
    expect(compareVersions('main_2026-06-02_03', 'main_2026-06-02_03')).toBe(0)
  })

  it('refuses date tags with different prefixes', () => {
    expect(compareVersions('main_2026-06-02_03', 'hotfix_2026-06-02_03')).toBeNull()
    expect(compareVersions('billing_main_2026-05-18_00', 'project-infra_main_2026-06-05_01')).toBeNull()
  })

  it('refuses mixed date-tag vs non-date and unparseable input', () => {
    expect(compareVersions('main_2026-06-02_03', '1.2.0')).toBeNull()
    expect(compareVersions('latest', 'abc123')).toBeNull()
    expect(compareVersions(undefined, '1.0.0')).toBeNull()
  })

  it('handles long compound prefixes as one prefix', () => {
    expect(compareVersions('billing_main_2026-05-18_00', 'billing_main_2026-06-05_01')).toBe(-1)
  })
})

// The lag arrow is the trust-fatal output: direction, unranked exclusion, and
// same-env refusal each have a distinct silent-inversion failure mode.
describe('appGroupLagMessage', () => {
  it('fires when a strictly-lower env runs a strictly-newer version, with correct direction', () => {
    expect(appGroupLagMessage([
      { env: 'dev', version: '2.0.0' },
      { env: 'staging', version: '1.0.0' },
    ])).toBe('staging is behind dev')
  })

  it('does not fire when the higher env is newer (healthy promotion)', () => {
    expect(appGroupLagMessage([
      { env: 'dev', version: '1.0.0' },
      { env: 'staging', version: '2.0.0' },
    ])).toBeNull()
  })

  it('never draws arrows through unranked (discovered) envs', () => {
    expect(appGroupLagMessage([
      { env: 'qa', version: '9.0.0' },
      { env: 'prod', version: '1.0.0' },
    ])).toBeNull()
  })

  it('never compares two instances of the same env', () => {
    expect(appGroupLagMessage([
      { env: 'prod', version: '2.0.0' },
      { env: 'prod', version: '1.0.0' },
    ])).toBeNull()
  })

  it('treats missing or incomparable versions as no signal', () => {
    expect(appGroupLagMessage([{ env: 'dev' }, { env: 'prod', version: '1.0.0' }])).toBeNull()
    expect(appGroupLagMessage([
      { env: 'dev', version: 'latest' },
      { env: 'prod', version: '1.0.0' },
    ])).toBeNull()
  })

  it('orders date-stamped CI tags through the ladder', () => {
    expect(appGroupLagMessage([
      { env: 'dev', version: 'main_2026-06-07_02' },
      { env: 'staging', version: 'main_2026-03-26_05' },
    ])).toBe('staging is behind dev')
  })
})

describe('identityEnvInferred', () => {
  it('marks only namespace-derived identity envs as inferred', () => {
    expect(identityEnvInferred({ key: 'billing', env: 'staging', confidence: 'medium', evidence: 'namespace stem "billing" + shared image repo repo/app' })).toBe(true)
    expect(identityEnvInferred({ key: 'billing', env: 'staging', confidence: 'medium', evidence: 'name stem "billing" + shared image repo repo/app' })).toBe(false)
    expect(identityEnvInferred({ key: 'billing', env: 'staging', confidence: 'medium', evidence: 'environment label "staging" + name/repo evidence' })).toBe(false)
    expect(identityEnvInferred({ key: 'billing', env: 'staging', confidence: 'high', evidence: 'Argo CD source path billing (env overlay staging)' })).toBe(false)
  })
})

describe('batchActivityForApp', () => {
  const workload = (kind: string, name: string, batch?: AppWorkload['batch']): AppWorkload => ({
    kind,
    namespace: 'prod',
    name,
    health: 'neutral',
    ready: 0,
    desired: 0,
    restarts: 0,
    workload_class: kind === 'Deployment' ? 'service' : 'job',
    batch,
  })
  const app = (workloads: AppWorkload[]): AppRow => ({
    key: 'billing',
    name: 'billing',
    health: 'neutral',
    workloads,
  })

  it('ranks failed, active, suspended, and quiet batch workloads without inventing retained history', () => {
    const activity = batchActivityForApp(app([
      workload('Deployment', 'api'),
      workload('CronJob', 'nightly-success', { retainedRuns: 2, latestRunPhase: 'Succeeded', latestRunName: 'nightly-success-1' }),
      workload('CronWorkflow', 'hourly-suspended', { suspended: true, retainedRuns: 0, schedule: '0 * * * *' }),
      workload('Job', 'active-reindex', { activeRuns: 1, retainedRuns: 1, latestRunPhase: 'Running' }),
      workload('Workflow', 'failed-migration', { failedRuns: 1, retainedRuns: 1, latestRunPhase: 'Failed', latestRunName: 'failed-migration', message: 'pod crashed' }),
    ]))

    expect(activity.map((item) => item.workload.name)).toEqual([
      'failed-migration',
      'active-reindex',
      'hourly-suspended',
      'nightly-success',
    ])
    expect(activity[0]).toMatchObject({ tone: 'rose', label: 'Latest run failed', failedRuns: 1, detail: 'pod crashed' })
    expect(activity[1]).toMatchObject({ tone: 'sky', label: '1 active run', activeRuns: 1 })
    expect(activity[3]).toMatchObject({ tone: 'muted', label: 'Latest run succeeded', retainedRuns: 2 })
  })
})

// Position-preserving env switch: exact match, stem fallback (suffix, prefix,
// discovered tokens), and the explicit no-counterpart null.
describe('matchWorkloadAcrossInstances', () => {
  const dep = (name: string, namespace = 'staging') => ({ kind: 'Deployment', namespace, name })

  it('prefers the exact kind+name match', () => {
    expect(matchWorkloadAcrossInstances('Deployment/dev/billing', [dep('billing')])).toEqual(dep('billing'))
  })

  it('prefers namespace-specific exact matches and refuses ambiguous same-name matches', () => {
    expect(matchWorkloadAcrossInstances('Deployment/dev/billing', [dep('billing', 'prod'), dep('billing', 'dev')])).toEqual(dep('billing', 'dev'))
    expect(matchWorkloadAcrossInstances('Deployment/dev/billing', [dep('billing', 'prod'), dep('billing', 'staging')])).toBeNull()
  })

  it('falls back to the env-affix-stripped stem (suffix and prefix)', () => {
    expect(matchWorkloadAcrossInstances('Deployment/dev/billing-dev', [dep('billing-staging')])).toEqual(dep('billing-staging'))
    expect(matchWorkloadAcrossInstances('Deployment/qa/qa-koala', [dep('staging-koala')])).toEqual(dep('staging-koala'))
  })

  it('strips discovered env tokens passed via extraTokens', () => {
    const tokens = new Set(['loadtest'])
    expect(matchWorkloadAcrossInstances('Deployment/team/api-loadtest', [dep('api', 'dev')], tokens)).toEqual(dep('api', 'dev'))
    expect(matchWorkloadAcrossInstances('Deployment/team/api-loadtest', [dep('api', 'dev')])).toBeNull()
  })

  it('returns null when no counterpart exists', () => {
    expect(matchWorkloadAcrossInstances('Deployment/dev/billing', [dep('finops')])).toBeNull()
    expect(matchWorkloadAcrossInstances('garbage', [dep('billing')])).toBeNull()
  })
})

// foldAppGroups pins the collapse experiment's safety rails — each fails
// silently in a component-embedded loop.
describe('foldAppGroups', () => {
  const entry = (key: string, name: string, famEnv?: string, over: Partial<AppGroupFoldEntry> = {}): AppGroupFoldEntry => ({
    row: { key, name, identity: famEnv ? { key: 'billing', env: famEnv, confidence: 'medium', evidence: 'e' } : undefined },
    health: 'healthy',
    versions: [],
    ready: 1,
    desired: 1,
    kinds: { Deployment: 1 },
    classComposition: [{ cls: 'service', count: 1 }],
    ...over,
  })

  it('folds app group members into one ladder row with instances hidden by default', () => {
    const rows = foldAppGroups([entry('a', 'billing', 'dev'), entry('b', 'billing-staging', 'staging'), entry('c', 'lonely')], new Set(), false)
    expect(rows.map((r) => r.kind)).toEqual(['group', 'instance'])
    const group = rows[0] as Extract<(typeof rows)[0], { kind: 'group' }>
    expect(group.label).toBe('billing')
    expect(group.cells.map((c) => c.env)).toEqual(['dev', 'staging'])
    expect(group.ready).toBe(2)
  })

  it('renders a filter-orphaned member as the plain instance it is', () => {
    const rows = foldAppGroups([entry('a', 'billing', 'dev')], new Set(), false)
    expect(rows).toEqual([{ kind: 'instance', entry: entry('a', 'billing', 'dev') }])
  })

  it('search auto-expansion emits the member rows', () => {
    const rows = foldAppGroups([entry('a', 'billing', 'dev'), entry('b', 'billing-staging', 'staging')], new Set(), true)
    expect(rows.map((r) => r.kind)).toEqual(['group', 'instance', 'instance'])
    expect(rows.filter((r) => r.kind === 'instance').every((r) => (r as { child?: boolean }).child)).toBe(true)
  })

  it('aggregates same-env instances into one cell: count, worst health, newest version', () => {
    const rows = foldAppGroups(
      [
        entry('a', 'billing', 'staging', { versions: ['1.0.0'], health: 'healthy' }),
        entry('b', 'billing-2', 'staging', { versions: ['2.0.0'], health: 'unhealthy' }),
        entry('c', 'billing-dev', 'dev'),
      ],
      new Set(),
      false,
    )
    const group = rows[0] as Extract<(typeof rows)[0], { kind: 'group' }>
    const staging = group.cells.find((c) => c.env === 'staging')!
    expect(staging.count).toBe(2)
    expect(staging.health).toBe('unhealthy')
    expect(staging.version).toBe('2.0.0')
  })

  it('derives the group workload class like the server: service+worker collapses, jobs make mixed', () => {
    const mk = (cls: 'service' | 'worker' | 'job', key: string, env: string) =>
      entry(key, key, env, { classComposition: [{ cls, count: 1 }] })
    const sw = foldAppGroups([mk('service', 'a', 'dev'), mk('worker', 'b', 'staging')], new Set(), false)
    expect((sw[0] as { workloadClass?: string }).workloadClass).toBe('service')
    const sj = foldAppGroups([mk('service', 'a', 'dev'), mk('job', 'b', 'staging')], new Set(), false)
    expect((sj[0] as { workloadClass?: string }).workloadClass).toBe('mixed')
  })

  it('scopes non-portable identities but folds portable identities across scopes', () => {
    const portable = (key: string, name: string, env: string): AppGroupFoldEntry => ({
      ...entry(key, name, env),
      row: { key, name, identity: { key: 'billing', env, confidence: 'high', evidence: 'e', portable: true } },
    })

    const oss = foldAppGroups(
      [portable('a', 'billing', 'dev'), entry('b', 'billing-staging', 'staging')],
      new Set(),
      false,
    )
    expect(oss.map((r) => r.kind)).toEqual(['group'])

    const local = foldAppGroups(
      [entry('a', 'billing', 'dev'), entry('b', 'billing-staging', 'staging')],
      new Set(),
      false,
      { localScope: (e) => e.row.key },
    )
    expect(local.map((r) => r.kind)).toEqual(['instance', 'instance'])

    const grouped = foldAppGroups([portable('a', 'billing', 'dev'), portable('b', 'billing-staging', 'staging')], new Set(), false, {
      localScope: (e) => e.row.key,
    })
    expect(grouped.map((r) => r.kind)).toEqual(['group'])
  })
})

describe('worstHealth', () => {
  // Mirrors pkg/health.WorseOf: unhealthy > degraded > unknown > healthy > neutral,
  // with neutral as the most-benign identity. Regression for the rank-inversion
  // fix — seeding the fold with `unknown` (now rank 2) made all-healthy/all-idle
  // sets wrongly return `unknown`.
  it('all-healthy stays healthy (not unknown)', () => {
    expect(worstHealth(['healthy', 'healthy'])).toBe('healthy')
  })
  it('all-neutral stays neutral', () => {
    expect(worstHealth(['neutral', 'neutral'])).toBe('neutral')
  })
  it('healthy + neutral resolves to healthy (healthy out-ranks idle)', () => {
    expect(worstHealth(['healthy', 'neutral'])).toBe('healthy')
    expect(worstHealth(['neutral', 'healthy'])).toBe('healthy')
  })
  it('unknown out-ranks healthy (a node-lost workload is worse than a running one)', () => {
    expect(worstHealth(['unknown', 'healthy'])).toBe('unknown')
  })
  it('unhealthy dominates everything', () => {
    expect(worstHealth(['unhealthy', 'degraded', 'unknown', 'healthy', 'neutral'])).toBe('unhealthy')
  })
  it('empty set is the most-benign identity', () => {
    expect(worstHealth([])).toBe('neutral')
  })
})

describe('foldAppGroups health rollup', () => {
  const grp = (env: string, health: string): AppGroupFoldEntry => ({
    row: { key: `k-${env}`, name: `billing-${env}`, identity: { key: 'billing', env, confidence: 'medium', evidence: 'e' } },
    health: health as AppGroupFoldEntry['health'],
    versions: [],
    ready: 1,
    desired: 1,
    kinds: { Deployment: 1 },
    classComposition: [{ cls: 'service', count: 1 }],
  })
  const rollup = (...hs: string[]) => {
    const rows = foldAppGroups(hs.map((h, i) => grp(`env${i}`, h)), new Set(), false)
    return (rows[0] as Extract<(typeof rows)[0], { kind: 'group' }>).health
  }
  it('all-healthy group rolls up healthy (regression: was unknown)', () => {
    expect(rollup('healthy', 'healthy')).toBe('healthy')
  })
  it('all-idle group rolls up neutral (Idle), not green', () => {
    expect(rollup('neutral', 'neutral')).toBe('neutral')
  })
  it('mixed healthy + idle reads healthy', () => {
    expect(rollup('healthy', 'neutral')).toBe('healthy')
  })
  it('healthy + unknown reads unknown (node-lost dominates)', () => {
    expect(rollup('healthy', 'unknown')).toBe('unknown')
  })
})

describe('appGroupingExplainer', () => {
  it('declared origins fold across clusters with no fix needed', () => {
    for (const source of ['explicit', 'argo-path', 'argo-appset', 'flux-source']) {
      const e = appGroupingExplainer({ key: 'k', env: 'prod', confidence: 'high', evidence: '', source })
      expect(e.folds).toBe(true)
      expect(e.fix).toBeUndefined()
    }
  })

  it('NAME sources stay per-cluster and tell the user how to fold', () => {
    for (const source of ['label', 'name-stem', 'namespace', undefined]) {
      const e = appGroupingExplainer({ key: 'k', env: 'prod', confidence: 'high', evidence: '', source })
      expect(e.folds).toBe(false)
      expect(e.fix).toContain(APP_IDENTITY_ANNOTATION)
    }
  })
})

describe('foldAppGroups pathKey disambiguation', () => {
  const fleetEntry = (key: string, env: string, pathKey: string): AppGroupFoldEntry => ({
    row: { key, name: 'billing', identity: { key: 'billing', env, confidence: 'high', evidence: 'e', portable: true, source: 'argo-path', pathKey } },
    health: 'healthy', versions: [], ready: 1, desired: 1, kinds: { Deployment: 1 }, classComposition: [{ cls: 'service', count: 1 }],
  })
  const opts = { localScope: (e: AppGroupFoldEntry) => e.row.key }

  it('folds same-name portable rows that share a pathKey', () => {
    const rows = foldAppGroups([fleetEntry('cl-a', 'dev', 'apps/billing'), fleetEntry('cl-b', 'prod', 'apps/billing')], new Set(), false, opts)
    expect(rows.filter((r) => r.kind === 'group').length).toBe(1)
  })

  it('does NOT fold same-name portable rows with different pathKeys (two teams, two paths)', () => {
    const rows = foldAppGroups([fleetEntry('cl-a', 'dev', 'teamA/billing'), fleetEntry('cl-b', 'prod', 'teamB/billing')], new Set(), false, opts)
    expect(rows.filter((r) => r.kind === 'group').length).toBe(0)
    expect(rows.filter((r) => r.kind === 'instance').length).toBe(2)
  })
})

describe('buildAppMembershipIndex', () => {
  // Minimal AppRow builder — only the fields the index reads.
  const row = (over: Partial<AppRow>): AppRow => ({
    key: 'k', name: 'app', health: 'healthy', workloads: [], ...over,
  })

  it('indexes workloads by Kind/ns/name and satellites by the app namespace', () => {
    const idx = buildAppMembershipIndex([
      row({
        key: 'team-a/app/billing', name: 'billing', namespace: 'team-a',
        workloads: [{ kind: 'Deployment', namespace: 'team-a', name: 'billing-api', health: 'healthy', ready: 1, desired: 1, restarts: 0 }],
        relationships: { services: ['billing-svc'], ingresses: ['billing-ing'] },
      }),
    ])
    expect(idx.byResource.get('Deployment/team-a/billing-api')?.appName).toBe('billing')
    expect(idx.byResource.get('Service/team-a/billing-svc')?.appName).toBe('billing')
    expect(idx.byResource.get('Ingress/team-a/billing-ing')?.appName).toBe('billing')
  })

  it('indexes routes under their concrete kind, not a generic "Route"', () => {
    // The server ships "Kind/name" for polymorphic routes; the index must key on
    // the concrete kind so it matches the route lane id (HTTPRoute/…/name).
    const idx = buildAppMembershipIndex([
      row({
        key: 'team-a/app/web', name: 'web', namespace: 'team-a',
        relationships: { routes: ['HTTPRoute/web-http', 'GRPCRoute/web-grpc'] },
      }),
    ])
    expect(idx.byResource.get('HTTPRoute/team-a/web-http')?.appName).toBe('web')
    expect(idx.byResource.get('GRPCRoute/team-a/web-grpc')?.appName).toBe('web')
    expect(idx.byResource.has('Route/team-a/HTTPRoute/web-http')).toBe(false)
  })

  it('skips satellites for a multi-namespace app (can not place them unambiguously)', () => {
    const idx = buildAppMembershipIndex([
      row({
        key: 'a', name: 'span', namespaces: ['team-a', 'team-b'],
        workloads: [{ kind: 'Deployment', namespace: 'team-a', name: 'api', health: 'healthy', ready: 1, desired: 1, restarts: 0 }],
        relationships: { services: ['svc'] },
      }),
    ])
    expect(idx.byResource.has('Deployment/team-a/api')).toBe(true)
    expect([...idx.byResource.keys()].some((k) => k.startsWith('Service/'))).toBe(false)
  })

  it('indexes matchKeys by evidence but excludes name-stem (exact kinds only in v1)', () => {
    // matchKeys are namespace-scoped (kind:namespace:value); name-stem stays unscoped.
    const idx = buildAppMembershipIndex([
      row({ key: 'a', name: 'billing', identity: { key: 'billing', env: 'prod', confidence: 'high', evidence: 'x' }, matchKeys: ['instance:team-a:billing-prod', 'helm:team-a:billing-v2', 'name-stem:billing'] }),
    ])
    expect(idx.byEvidence.get('instance:team-a:billing-prod')?.appName).toBe('billing')
    expect(idx.byEvidence.get('helm:team-a:billing-v2')?.appName).toBe('billing')
    expect(idx.byEvidence.has('name-stem:billing')).toBe(false)
  })

  it('carries env + evidence onto the membership', () => {
    const idx = buildAppMembershipIndex([
      row({ key: 'a', name: 'billing', identity: { key: 'billing', env: 'prod', confidence: 'high', evidence: 'name stem billing' }, matchKeys: ['name:team-a:billing'] }),
    ])
    const m = idx.byEvidence.get('name:team-a:billing')
    expect(m?.env).toBe('prod')
    expect(m?.evidence).toBe('name stem billing')
  })

  it('collision resolves first-wins (server-sorted row order)', () => {
    const idx = buildAppMembershipIndex([
      row({ key: 'first', name: 'first', matchKeys: ['name:team-a:shared'] }),
      row({ key: 'second', name: 'second', matchKeys: ['name:team-a:shared'] }),
    ])
    expect(idx.byEvidence.get('name:team-a:shared')?.appName).toBe('first')
  })

  it('same label value in two namespaces indexes as distinct evidence keys', () => {
    // The whole point of namespace scoping: "shared" in team-a and team-b must
    // NOT collapse to one key — each namespace maps to its own app.
    const idx = buildAppMembershipIndex([
      row({ key: 'a', name: 'app-a', matchKeys: ['instance:team-a:shared'] }),
      row({ key: 'b', name: 'app-b', matchKeys: ['instance:team-b:shared'] }),
    ])
    expect(idx.byEvidence.get('instance:team-a:shared')?.appName).toBe('app-a')
    expect(idx.byEvidence.get('instance:team-b:shared')?.appName).toBe('app-b')
  })
})
