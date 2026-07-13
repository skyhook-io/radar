import { describe, it, expect } from 'vitest'
import {
  buildSingleAppEntry,
  searchTextForEntry,
  foldAppGroups,
  type AppRow,
  type SingleAppEntry,
} from '../../utils/applications'

// Parity tests for the pure core the shared ApplicationsView is built on:
// buildSingleAppEntry (the extracted former buildEntry), the search-text helper,
// and the fold. These pin that the single variant behaves exactly as the old
// ApplicationsList body did — fold grouping, facet counts, search matching.

const wl = (over: Partial<AppRow['workloads'][number]> = {}): AppRow['workloads'][number] => ({
  kind: 'Deployment',
  namespace: 'default',
  name: 'svc',
  workload_class: 'service',
  health: 'healthy',
  ready: 1,
  desired: 1,
  restarts: 0,
  ...over,
})

const app = (over: Partial<AppRow> = {}): AppRow => ({
  key: over.key ?? 'default/svc',
  name: over.name ?? 'svc',
  namespace: 'default',
  health: 'healthy',
  workloads: [wl()],
  ...over,
})

describe('buildSingleAppEntry', () => {
  it('tags the single variant and aggregates ready/desired + kinds', () => {
    const e = buildSingleAppEntry(
      app({
        workloads: [
          wl({ kind: 'Deployment', ready: 2, desired: 3 }),
          wl({ kind: 'CronJob', workload_class: 'job', ready: 0, desired: 0 }),
        ],
      }),
    )
    expect(e.variant).toBe('single')
    expect(e.ready).toBe(2)
    expect(e.desired).toBe(3)
    expect(e.readyRatio).toBeCloseTo(2 / 3)
    expect(e.kinds).toEqual({ Deployment: 1, CronJob: 1 })
    // service + job → mixed for the facet set; class set is inclusive.
    expect(e.classSet.sort()).toEqual(['job', 'service'])
  })

  it('resolves env from the namespace heuristic and marks it inferred', () => {
    const e = buildSingleAppEntry(app({ namespace: 'billing-prod', workloads: [wl({ namespace: 'billing-prod' })] }))
    expect(e.env).toBe('prod')
    expect(e.envInferred).toBe(true)
  })

  it('takes the identity env as authoritative (not inferred when declared)', () => {
    const e = buildSingleAppEntry(
      app({
        identity: { key: 'svc', env: 'staging', confidence: 'high', evidence: 'declared source path' },
      }),
    )
    expect(e.env).toBe('staging')
    expect(e.envInferred).toBe(false)
  })

  it('uses delivery degradation in the displayed application health', () => {
    const e = buildSingleAppEntry(app({
      health: 'degraded',
      runtimeHealth: 'healthy',
      sourceStatus: { sync: 'Synced', health: 'Degraded' },
    }))

    expect(e.health).toBe('degraded')
  })

  it('preserves the latest retained batch outcome when delivery adds no degradation', () => {
    const e = buildSingleAppEntry(app({
      health: 'unhealthy',
      runtimeHealth: 'unhealthy',
      workload_class: 'job',
      workloads: [wl({
        kind: 'CronJob',
        workload_class: 'job',
        health: 'unhealthy',
        ready: 0,
        desired: 0,
        batch: { retainedRuns: 2, failedRuns: 1, succeededRuns: 1, latestRunPhase: 'Succeeded' },
      })],
    }))

    expect(e.health).toBe('healthy')
  })

  it('applies delivery degradation to a batch app with a successful latest run', () => {
    const e = buildSingleAppEntry(app({
      health: 'unhealthy',
      runtimeHealth: 'unhealthy',
      workload_class: 'job',
      sourceStatus: { sync: 'Synced', health: 'Degraded' },
      workloads: [wl({
        kind: 'CronJob',
        workload_class: 'job',
        health: 'unhealthy',
        ready: 0,
        desired: 0,
        batch: { retainedRuns: 2, failedRuns: 1, succeededRuns: 1, latestRunPhase: 'Succeeded' },
      })],
    }))

    expect(e.health).toBe('degraded')
  })
})

describe('searchTextForEntry (single)', () => {
  it('matches on name, namespace, version, env, and workload kind', () => {
    const e = buildSingleAppEntry(
      app({
        name: 'checkout',
        namespace: 'shop-prod',
        versions: ['1.4.2'],
        workloads: [wl({ kind: 'StatefulSet', namespace: 'shop-prod' })],
      }),
    )
    const text = searchTextForEntry(e)
    expect(text).toContain('checkout')
    expect(text).toContain('shop-prod')
    expect(text).toContain('1.4.2')
    expect(text).toContain('prod')
    expect(text).toContain('statefulset')
    expect(text).not.toContain('zzz-no-match')
  })
})

// One logical app across two envs must fold into a single group row with two
// instance children; an orphan (filtered to one member) must render flat.
describe('foldAppGroups over single entries', () => {
  const ident = (env: string) => ({ key: 'shop', env, confidence: 'high', evidence: 'declared source path' })

  const mkSibling = (env: string): SingleAppEntry =>
    buildSingleAppEntry(
      app({ key: `shop-${env}/web`, name: `web-${env}`, namespace: `shop-${env}`, identity: ident(env), versions: ['1.0.0'] }),
    )

  it('folds ≥2 same-identity instances into one group with nested instances', () => {
    const entries = [mkSibling('dev'), mkSibling('prod')]
    const collapsed = foldAppGroups(entries, new Set(), false)
    expect(collapsed).toHaveLength(1)
    expect(collapsed[0].kind).toBe('group')
    if (collapsed[0].kind === 'group') {
      expect(collapsed[0].members).toHaveLength(2)
      expect(collapsed[0].cells.map((c) => c.env).sort()).toEqual(['dev', 'prod'])
    }

    const expanded = foldAppGroups(entries, new Set(['shop']), false)
    // group row + two instance children
    expect(expanded).toHaveLength(3)
    expect(expanded.filter((r) => r.kind === 'instance')).toHaveLength(2)
  })

  it('renders a lone surviving member as a flat instance, not a group', () => {
    const out = foldAppGroups([mkSibling('dev')], new Set(), false)
    expect(out).toHaveLength(1)
    expect(out[0].kind).toBe('instance')
  })

  it('auto-expands every group when the search flag is set', () => {
    const out = foldAppGroups([mkSibling('dev'), mkSibling('prod')], new Set(), true)
    expect(out[0].kind).toBe('group')
    if (out[0].kind === 'group') expect(out[0].expanded).toBe(true)
    expect(out.filter((r) => r.kind === 'instance')).toHaveLength(2)
  })

  it('builds the env ladder from envsOf (per-cluster slices), not identity.env', () => {
    // A fleet member can span several per-cluster envs; envsOf supplies them so
    // the ladder shows every env, including ones that are not the member's own
    // identity.env (which the hub can stale on a same-key cross-cluster join).
    const a = mkSibling('dev')
    const b = mkSibling('prod')
    const envsOf = (e: SingleAppEntry) =>
      e.env === 'dev'
        ? [{ env: 'dev', health: 'healthy' as const }, { env: 'staging', health: 'degraded' as const }]
        : [{ env: 'prod', health: 'healthy' as const }]
    const out = foldAppGroups([a, b], new Set(), false, { envsOf })
    expect(out[0].kind).toBe('group')
    if (out[0].kind === 'group') {
      expect(out[0].cells.map((c) => c.env).sort()).toEqual(['dev', 'prod', 'staging'])
    }
  })
})
