import { describe, it, expect } from 'vitest'
import { compareVersions, appGroupLagMessage, matchWorkloadAcrossInstances, foldAppGroups, identityEnvInferred, type AppGroupFoldEntry } from './applications'

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
