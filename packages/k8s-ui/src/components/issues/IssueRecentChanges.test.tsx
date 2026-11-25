import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { IssueRow } from './IssuesView'
import { changeVerb } from './change-verb'
import { changeTimeLabel, groupRecentChanges, noChangesScope, policyOnlyAdmission, recentChangeRef, type IssueExpansion } from './IssueRecentChanges'
import { issueTimingForDisplay } from './issue-timing'
import type { Issue, IssueCorrelationUnknownReason, IssueRecentChange } from './types'

const FIRST_SEEN = '2026-11-18T12:00:00Z'
const at = (offsetSeconds: number) => new Date(Date.parse(FIRST_SEEN) + offsetSeconds * 1000).toISOString()

const change = (overrides: Partial<IssueRecentChange>): IssueRecentChange => ({
  kind: 'Deployment',
  apiVersion: 'apps/v1',
  namespace: 'shop',
  name: 'api',
  changeType: 'update',
  timestamp: at(-240),
  ...overrides,
})

const listIssue: Issue = {
  id: 'crash-api',
  severity: 'critical',
  source: 'problem',
  category: 'crashloop',
  category_group: 'runtime',
  grouping_scope: 'workload',
  kind: 'Deployment',
  group: 'apps',
  namespace: 'shop',
  name: 'api',
  reason: 'CrashLoopBackOff',
  first_seen: FIRST_SEEN,
  issue_timing: 'started_at_resource_creation',
  issue_timing_basis: 'pod_creation',
}

const correlated = (overrides: Partial<Issue> = {}): Issue => ({
  ...listIssue,
  correlation: { window_from: at(-3600), window_to: at(600) },
  correlated_changes: [],
  ...overrides,
})

const withContext: Issue = {
  ...listIssue,
  cause: 'Container api exits with code 1 shortly after start.',
  change_context: { changed: true, what: 'image_change', when: '5m', evidence: 'ReplicaSet api-7d9 replaced api-5c4' },
  diagnostic_context: { role: 'candidate', facts: [{ type: 'pvc_blast_radius', message: 'Blocks every Pod that mounts this claim.' }] },
}

const loaded = (issue: Issue): IssueExpansion => ({ issue, recentChanges: { status: 'loaded' }, onRetry: () => {} })

function openRow(expandedIssue?: IssueExpansion, onResourceClick?: () => void) {
  return renderToString(
    <IssueRow issue={listIssue} open onToggle={() => {}} expandedIssue={expandedIssue} onResourceClick={onResourceClick} />,
  )
}

const markers = (html: string) => [...html.matchAll(/data-recent-changes="([^"]+)"/g)].map((m) => m[1])
const reasons = (html: string) => [...html.matchAll(/data-reason="([^"]+)"/g)].map((m) => m[1])
const groups = (html: string) => [...html.matchAll(/data-change-group="([^"]+)"/g)].map((m) => m[1])
const hasSection = (html: string) => html.includes('Recent changes')

describe('changeVerb', () => {
  it.each([
    ['image', change({ fields: [{ path: 'spec.template.spec.containers[api].image', oldValue: 'api:v1', newValue: 'api:v2' }] }), 'Image changed'],
    ['bracketed image', change({ fields: [{ path: 'spec.template.spec.containers[sidecar][image]' }] }), 'Image changed'],
    ['image beside env', change({ fields: [{ path: 'spec.template.spec.containers[api].env[LOG]' }, { path: 'spec.template.spec.containers[api].image' }] }), 'Image changed'],
    ['workload env', change({ fields: [{ path: 'spec.template.spec.containers[frontend].env[CART_ADDR]' }] }), 'Config changed'],
    ['workload envFrom', change({ fields: [{ path: 'spec.template.spec.initContainers[migrate].envFrom' }] }), 'Config changed'],
    ['workload command', change({ fields: [{ path: 'spec.template.spec.containers[db].command' }] }), 'Config changed'],
    ['ConfigMap keys', change({ kind: 'ConfigMap', apiVersion: 'v1', fields: [{ path: 'data (modified keys)' }] }), 'Config changed'],
    ['ConfigMap nested key', change({ kind: 'ConfigMap', apiVersion: 'v1', fields: [{ path: 'data.flags.paymentFailure.defaultVariant' }] }), 'Config changed'],
    ['ConfigMap key named image', change({ kind: 'ConfigMap', apiVersion: 'v1', fields: [{ path: 'data.features.json.image' }] }), 'Config changed'],
    ['Secret key named image', change({ kind: 'Secret', apiVersion: 'v1', fields: [{ path: 'stringData.image' }] }), 'Config changed'],
    ['Secret binary keys', change({ kind: 'Secret', apiVersion: 'v1', fields: [{ path: 'binaryData (modified keys)' }] }), 'Config changed'],
    ['replicas', change({ fields: [{ path: 'spec.replicas', oldValue: 1, newValue: 3 }] }), 'Scaled'],
    // kubectl rollout restart, as the correlation answer carries it.
    ['rollout restart', change({ summary: 'pod template annotation kubectl.kubernetes.io/restartedAt added (new pod template revision)', rank_reason: 'desired-state or configuration field changed',
      fields: [{ path: 'spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]' }] }), 'Restarted'],
    ['second rollout restart', change({ summary: 'pod template annotation kubectl.kubernetes.io/restartedAt changed (new pod template revision)',
      fields: [{ path: 'spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]', oldValue: '2026-11-19T13:41:26+02:00', newValue: '2026-11-19T15:02:00+02:00' }] }), 'Restarted'],
    ['restart with another template change', change({ fields: [{ path: 'spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]' }, { path: 'spec.template.metadata.labels[tier]' }] }), 'Changed'],
    ['restart with an image change', change({ fields: [{ path: 'spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]' }, { path: 'spec.template.spec.containers[api].image' }] }), 'Image changed'],
    ['restart text in the summary only', change({ summary: 'pod template annotation kubectl.kubernetes.io/restartedAt added', fields: [{ path: 'spec.template.metadata.labels[tier]' }] }), 'Changed'],
    ['ConfigMap key named like the annotation', change({ kind: 'ConfigMap', apiVersion: 'v1', fields: [{ path: 'data.spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]' }] }), 'Config changed'],
    ['recreated', change({ changeType: 'add', rank_reason: 'recreated with desired-state or configuration changes', fields: [{ path: 'spec.template.spec.containers[api].image' }] }), 'Recreated'],
    ['created', change({ changeType: 'add', rank_reason: 'lifecycle' }), 'Created'],
    ['created ConfigMap (real row)', change({ kind: 'ConfigMap', apiVersion: 'v1', name: 'worker-cfg', changeType: 'add', summary: 'add', rank_reason: 'resource create/delete for config or desired state', change_category: 'lifecycle', fields: [] }), 'Created'],
    ['new ClusterRoleBinding (creation snapshot)', change({
      kind: 'ClusterRoleBinding', apiVersion: 'rbac.authorization.k8s.io/v1', namespace: undefined, name: 'ops-admin', changeType: 'add',
      summary: 'created; it binds ClusterRole/cluster-admin', rank_reason: 'binding grants cluster-wide access',
      fields: [{ path: 'subjects', newValue: ['Group ops'] }, { path: 'roleRef', newValue: 'ClusterRole/cluster-admin' }],
    }), 'Created'],
    ['new RoleBinding (compacted create)', change({ kind: 'RoleBinding', apiVersion: 'rbac.authorization.k8s.io/v1', name: 'view', changeType: 'add', summary: 'add', fields: [] }), 'Created'],
    ['recreated ClusterRoleBinding', change({
      kind: 'ClusterRoleBinding', apiVersion: 'rbac.authorization.k8s.io/v1', namespace: undefined, name: 'ops-admin', changeType: 'add',
      summary: 'recreated with changes: roleRef: ClusterRole/view\u2192ClusterRole/cluster-admin', rank_reason: 'binding grants cluster-wide access',
      fields: [{ path: 'roleRef', oldValue: 'ClusterRole/view', newValue: 'ClusterRole/cluster-admin' }, { path: 'subjects (unchanged)', newValue: ['Group ops'] }],
    }), 'Recreated'],
    ['deleted ClusterRoleBinding', change({
      kind: 'ClusterRoleBinding', apiVersion: 'rbac.authorization.k8s.io/v1', namespace: undefined, name: 'ops-admin', changeType: 'delete',
      summary: 'deleted; it bound ClusterRole/cluster-admin', fields: [{ path: 'subjects', oldValue: ['Group ops'] }, { path: 'roleRef', oldValue: 'ClusterRole/cluster-admin' }],
    }), 'Deleted'],
    ['recreated ResourceQuota named by an admission rejection (real row)', change({
      kind: 'ResourceQuota', apiVersion: 'v1', name: 'compute', changeType: 'add',
      summary: 'recreated with changes: quota pods: 0\u21925, other spec changes: scopes',
      rank_reason: 'the object named by the admission rejection', change_category: 'spec_config',
      fields: [{ path: 'spec.hard.pods' }, { path: 'spec.scopes' }],
    }), 'Recreated'],
    ['deleted', change({ kind: 'Secret', changeType: 'delete', rank_reason: 'secret deleted (name only)' }), 'Deleted'],
    ['other spec', change({ fields: [{ path: 'spec.strategy.type' }] }), 'Changed'],
    ['mixed replicas and spec', change({ fields: [{ path: 'spec.replicas' }, { path: 'spec.strategy.type' }] }), 'Changed'],
    ['no fields', change({}), 'Changed'],
    ['Helm upgrade', change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, changeType: 'helm_release_revision', fields: [{ path: 'helm.revision', newValue: 7 }] }), 'Helm upgraded'],
    ['Helm install', change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, changeType: 'helm_release_revision', fields: [{ path: 'helm.revision', newValue: 1 }] }), 'Helm installed'],
    ['Helm rolled back upgrade', change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, changeType: 'upgrade_rolled_back' }), 'Helm upgrade rolled back'],
    ['Helm failed upgrade', change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, changeType: 'upgrade_failed' }), 'Helm upgrade failed'],
    ['Helm rollback', change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, changeType: 'rollback' }), 'Helm rolled back'],
    ['Helm pending', change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, changeType: 'pending' }), 'Helm operation stuck'],
    ['Flux HelmRelease spec', change({ kind: 'HelmRelease', apiVersion: 'helm.toolkit.fluxcd.io/v2', fields: [{ path: 'spec.chart.spec.version' }] }), 'Changed'],
  ])('%s', (_name, input, verb) => {
    expect(changeVerb(input)).toBe(verb)
  })
})

describe('grouping around first seen', () => {
  const trigger = change({ name: 'trigger', timestamp: at(-3 * 3600) })
  const close = change({ name: 'close', timestamp: at(-240) })
  const slack = change({ name: 'slack', timestamp: at(20) })
  const repairOld = change({ name: 'repair-old', timestamp: at(300) })
  const repairNew = change({ name: 'repair-new', timestamp: at(900) })

  it('puts changes up to 30s after first seen before it, closest first, and later ones after it, newest first', () => {
    const g = groupRecentChanges(correlated({ correlated_changes: [trigger, repairOld, close, repairNew, slack] }))
    expect(g.all).toBeNull()
    expect(g.before?.map((c) => c.name)).toEqual(['slack', 'close', 'trigger'])
    expect(g.after?.map((c) => c.name)).toEqual(['repair-new', 'repair-old'])
  })

  it.each([
    ['onset is unknown', { onset_unknown: true }],
    ['every grouped onset is unknown', { onset_unknown: true, onset_coverage: { known: 0, unknown: 3 } }],
  ])('still groups around first seen when %s', (_name, overrides) => {
    const g = groupRecentChanges(correlated({ ...overrides, correlated_changes: [close, repairNew, trigger] }))
    expect(g.all).toBeNull()
    expect(g.before?.map((c) => c.name)).toEqual(['close', 'trigger'])
    expect(g.after?.map((c) => c.name)).toEqual(['repair-new'])
  })

  it.each([
    ['mixed grouped onset', { onset_coverage: { known: 2, unknown: 1 } }],
    ['no first seen', { first_seen: undefined }],
    ['no first seen and onset unknown', { first_seen: undefined, onset_unknown: true }],
  ])('does not group when %s', (_name, overrides) => {
    const g = groupRecentChanges(correlated({ ...overrides, correlated_changes: [close, repairNew, trigger] }))
    expect(g.before).toBeNull()
    expect(g.after).toBeNull()
    expect(g.all?.map((c) => c.name)).toEqual(['repair-new', 'close', 'trigger'])
  })

  it('times grouped rows against first seen and ungrouped rows against now', () => {
    const anchor = Date.parse(FIRST_SEEN)
    expect(changeTimeLabel(close, anchor)).toBe('4 min earlier')
    expect(changeTimeLabel(repairOld, anchor)).toBe('5 min later')
    expect(changeTimeLabel(close, null, anchor)).toBe('4m ago')
  })
})

describe('change links', () => {
  it('opens the changed object, a native Helm release, and nothing for deleted or unknown-group rows', () => {
    expect(recentChangeRef(listIssue, change({}))).toMatchObject({ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'api' })
    expect(recentChangeRef(listIssue, change({ kind: 'ConfigMap', apiVersion: undefined, name: 'cfg' }))).toMatchObject({ kind: 'ConfigMap', group: '' })
    expect(recentChangeRef(listIssue, change({ source: 'helm', kind: 'HelmRelease', apiVersion: undefined, name: 'shop' }))).toMatchObject({ kind: 'HelmRelease', group: 'helm.sh' })
    expect(recentChangeRef(listIssue, change({ changeType: 'delete' }))).toBeNull()
    expect(recentChangeRef(listIssue, change({ kind: 'Widget', apiVersion: undefined }))).toBeNull()
  })

  it('renders a deleted object as text and a live one as a link', () => {
    const html = openRow(
      loaded(correlated({ correlated_changes: [change({ name: 'live-one' }), change({ kind: 'Secret', apiVersion: 'v1', name: 'gone-one', changeType: 'delete' })] })),
      () => {},
    )
    const buttonsAround = (name: string) => new RegExp(`<button[^>]*>(?:(?!</button>).)*${name}`).test(html)
    expect(buttonsAround('live-one')).toBe(true)
    expect(buttonsAround('gone-one')).toBe(false)
  })
})

const rows = (html: string) => [...html.matchAll(/<li[^>]*data-change-row[^>]*>(.*?)<\/li>/g)].map((m) => m[1])
const detailOf = (row: string) => row.match(/data-change-detail="([a-z]+)"[^>]*>(.*?)<\/p>/)

describe('change rows', () => {
  const helmRow = change({
    source: 'helm', kind: 'HelmRelease', apiVersion: undefined, name: 'shop', changeType: 'upgrade_rolled_back',
    summary: 'Helm upgrade for "shop" failed at rev 7 and rolled back to rev 6.',
    fields: [{ path: 'helm.operation' }, { path: 'helm.failedRevision' }],
  })

  it('never shows Helm field names, with or without a summary', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [helmRow, { ...helmRow, summary: undefined, timestamp: at(-60) }] })))
    expect(html).not.toContain('helm.operation')
    const withSummary = rows(html).find((r) => r.includes('rolled back to rev 6'))!
    const without = rows(html).find((r) => !r.includes('rolled back to rev 6'))!
    expect(detailOf(withSummary)?.[1]).toBe('summary')
    expect(detailOf(without)).toBeNull()
  })

  it('shows the summary instead of field paths, and paths only when there is no summary', () => {
    const path = 'spec.template.spec.containers[web].image'
    const html = openRow(loaded(correlated({ correlated_changes: [
      change({ name: 'other', summary: 'image: web:1 -> web:2', fields: [{ path }] }),
      change({ name: 'bare', summary: 'update', fields: [{ path }], timestamp: at(-60) }),
    ] })))
    const summarized = rows(html).find((r) => r.includes('web:2'))!
    const bare = rows(html).find((r) => !r.includes('web:2'))!
    expect(detailOf(summarized)?.[1]).toBe('summary')
    expect(detailOf(summarized)?.[2]).not.toContain(path)
    expect(detailOf(bare)?.[1]).toBe('paths')
    expect(detailOf(bare)?.[2]).toContain(path)
  })

  it('uses the code font only for field paths, never for a Referenced by line', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [
      change({ kind: 'ConfigMap', apiVersion: 'v1', name: 'refs-only', summary: undefined, consumed_by: ['Deployment/api'] }),
      change({ kind: 'ConfigMap', apiVersion: 'v1', name: 'paths-and-refs', summary: undefined, consumed_by: ['Deployment/api'], fields: [{ path: 'data.mode' }], timestamp: at(-60) }),
    ] })))
    const spans = [...html.matchAll(/<span([^>]*)data-change-detail="([a-z]+)"[^>]*>/g)].map((m) => ({ attrs: m[1], kind: m[2] }))
    const refs = spans.filter((sp) => sp.kind === 'references')
    const paths = spans.filter((sp) => sp.kind === 'paths')
    expect(refs).toHaveLength(2)
    refs.forEach((sp) => expect(sp.attrs).not.toContain('font-mono'))
    expect(paths).toHaveLength(1)
    expect(paths[0].attrs).toContain('font-mono')
    expect(html).not.toMatch(/<p[^>]*font-mono[^>]*>[^<]*Referenced by/)
  })

  it('names a change on the issue subject by its kind, without a link, and links other objects', () => {
    const html = openRow(
      loaded(correlated({ correlated_changes: [change({ name: listIssue.name }), change({ kind: 'ConfigMap', apiVersion: 'v1', name: 'app-config', timestamp: at(-60) })] })),
      () => {},
    )
    const other = rows(html).find((r) => r.includes('app-config'))!
    const own = rows(html).find((r) => !r.includes('app-config'))!
    expect(own).not.toContain('<button')
    expect(own.toLowerCase()).toContain(`this ${listIssue.kind.toLowerCase()}`)
    expect(own).not.toContain(`>${listIssue.name}<`)
    expect(other).toMatch(/<button[^>]*>app-config<\/button>/)
  })

  it('keeps a same-named object of another group as a linked object', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [change({ name: listIssue.name, apiVersion: 'other.example.io/v1' })] })), () => {})
    expect(rows(html)[0]).toContain('<button')
  })
})

describe('what a no-changes answer covered', () => {
  it.each([
    ['a workload', { kind: 'Deployment' }, ['Deployment', 'ConfigMaps and Secrets'], ['ResourceQuota', 'webhook']],
    ['a Pod', { kind: 'Pod', group: '' }, ['Pod', 'ConfigMaps and Secrets'], []],
    ['a non-workload', { kind: 'Service', group: '' }, ['Service'], ['ConfigMaps', 'ResourceQuota']],
    ['a quota rejection', { kind: 'Job', group: 'batch', category: 'quota_exceeded' }, ['Job', 'ResourceQuota', 'LimitRange'], ['ConfigMaps']],
    ['a webhook rejection', { kind: 'ReplicaSet', category: 'admission_webhook_blocking' }, ['ReplicaSet', 'webhook configuration'], ['ConfigMaps']],
    ['a native Helm release', { kind: 'HelmRelease', group: 'helm.sh' }, ['Helm release'], ['ConfigMaps']],
  ] as const)('%s', (_name, overrides, includes, excludes) => {
    const scope = noChangesScope({ ...listIssue, ...overrides })
    for (const part of includes) expect(scope).toContain(part)
    for (const part of excludes) expect(scope).not.toContain(part)
  })

  const policyScope = { window_seconds: 3600, scope: 'admission_policy' as const }
  it.each([
    ['a Job the server scoped to the policy', { kind: 'Job', group: 'batch', category: 'quota_exceeded', no_recent_changes: policyScope }, true],
    ['a ReplicaSet the server scoped to the policy', { kind: 'ReplicaSet', category: 'admission_webhook_blocking', no_recent_changes: policyScope }, true],
    ['a Job without the scope field', { kind: 'Job', group: 'batch', category: 'quota_exceeded', no_recent_changes: { window_seconds: 3600 } }, false],
    ['a Deployment', { kind: 'Deployment', category: 'quota_exceeded', no_recent_changes: { window_seconds: 3600 } }, false],
  ] as const)('follows the server scope for a policy-only answer: %s', (_name, overrides, policyOnly) => {
    const issue = { ...listIssue, ...overrides }
    expect(policyOnlyAdmission(issue)).toBe(policyOnly)
    expect(noChangesScope(issue).startsWith(`this ${issue.kind}`)).toBe(!policyOnly)
    expect(noChangesScope(issue)).toContain(issue.kind)
  })

  it('qualifies the claim as tracked changes, for every scope', () => {
    for (const overrides of [{ kind: 'Deployment' }, { kind: 'Service', group: '' }, { kind: 'Job', group: 'batch', category: 'quota_exceeded' }, { kind: 'HelmRelease', group: 'helm.sh' }]) {
      const html = openRow(loaded(correlated({ ...overrides, no_recent_changes: { window_seconds: 3600 } })))
      expect(html).toMatch(/data-recent-changes="none"[^>]*>[^<]*tracked changes/)
    }
  })

  it('adds that the subject itself is not tracked when the answer covers only the policy', () => {
    const line = (issue: Issue) => openRow(loaded(issue)).match(/data-recent-changes="none"[^>]*>(.*?)<\/p>/)?.[1] ?? ''
    const job = line(correlated({ kind: 'Job', group: 'batch', category: 'quota_exceeded', no_recent_changes: { window_seconds: 3600, scope: 'admission_policy' } }))
    const jobWithoutScope = line(correlated({ kind: 'Job', group: 'batch', category: 'quota_exceeded', no_recent_changes: { window_seconds: 3600 } }))
    const deployment = line(correlated({ kind: 'Deployment', category: 'quota_exceeded', no_recent_changes: { window_seconds: 3600 } }))
    expect(job.length).toBeGreaterThan(deployment.length)
    expect(job.match(/Job/g)?.length).toBe(2)
    expect(jobWithoutScope.match(/Job/g)?.length).toBe(1)
    expect(jobWithoutScope.length).toBe(deployment.length - 'Deployment'.length + 'Job'.length)
  })

  it('names that scope in the none line', () => {
    const html = openRow(loaded(correlated({ kind: 'Service', group: '', no_recent_changes: { window_seconds: 3600 } })))
    expect(html).toMatch(/data-recent-changes="none"[^>]*>[^<]*this Service/)
  })
})

describe('Recent changes section states', () => {
  it('leaves an expanded row with no host override as it was: existing sections and change context, no Recent changes', () => {
    const html = renderToString(<IssueRow issue={withContext} open onToggle={() => {}} onResourceClick={() => {}} />)
    expect(hasSection(html)).toBe(false)
    expect(markers(html)).toEqual([])
    expect(html).toContain(withContext.cause)
    expect(html).toContain('ReplicaSet api-7d9 replaced api-5c4')
    expect(html).toContain('Blocks every Pod that mounts this claim')
    expect(html).toContain('Subject')
  })

  it('groups found changes before and after first seen', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [change({ timestamp: at(-240) }), change({ name: 'web', timestamp: at(600) })] })))
    expect(groups(html)).toEqual(['before', 'after'])
    expect(markers(html)).toEqual([])
  })

  it('omits an empty group', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [change({ timestamp: at(-240) })] })))
    expect(groups(html)).toEqual(['before'])
  })

  it('lists found changes ungrouped when grouped onset is mixed', () => {
    const html = openRow(loaded(correlated({ onset_coverage: { known: 1, unknown: 1 }, correlated_changes: [change({}), change({ name: 'web', timestamp: at(600) })] })))
    expect(groups(html)).toEqual(['all'])
  })

  it('says more changes exist and that the read was saturated', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [change({})], correlated_changes_more_not_shown: true, correlated_changes_incomplete: true })))
    expect(markers(html)).toEqual(['more-not-shown', 'incomplete'])
  })

  it('claims no changes only when the server says none were found', () => {
    expect(markers(openRow(loaded(correlated({ no_recent_changes: { window_seconds: 3600 } }))))).toEqual(['none'])
    expect(markers(openRow(loaded(correlated({ correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason: 'user_forbidden', kinds: [{ kind: 'ConfigMap' }] } } }))))).toEqual(['limit'])
  })

  it.each([
    'user_forbidden',
    'user_permission_check_failed',
    'radar_kind_not_collected',
    'radar_namespace_not_collected',
    'kind_sync_pending',
    'sources_unresolved',
    'history_incomplete',
    'read_error',
  ] as const)('shows one line for unknown reason %s', (reason) => {
    const html = openRow(loaded(correlated({ correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason, kinds: [{ kind: 'ConfigMap' }, { kind: 'Secret' }] } } })))
    expect(reasons(html)).toEqual([reason])
  })

  it('names each kind once, whatever its group', () => {
    const html = openRow(loaded(correlated({ correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason: 'user_forbidden', kinds: [{ kind: 'Secret' }, { group: '', kind: 'Secret' }, { kind: 'ConfigMap' }] } } })))
    const line = html.match(/data-reason="user_forbidden"[^>]*>(.*?)<\/p>/)?.[1] ?? ''
    expect(line.match(/Secrets/g)).toHaveLength(1)
    expect(line).toContain('ConfigMaps')
  })

  it.each(['user_forbidden', 'read_error'] as const)('says why when Radar could not build the issue (%s)', (reason) => {
    const helm = { ...listIssue, kind: 'HelmRelease', group: 'helm.sh', name: 'shop' }
    const html = renderToString(<IssueRow issue={helm} open onToggle={() => {}} expandedIssue={{ recentChanges: { status: 'unavailable', reason }, onRetry: () => {} }} />)
    expect(markers(html)).toEqual(['limit'])
    expect(reasons(html)).toEqual([reason])
    expect(/data-reason="[a-z_]+"[^>]*>.*?<button/.test(html)).toBe(reason === 'read_error')
  })

  describe('names the source Radar could not identify', () => {
    const lineFor = (issue: Issue) => openRow(loaded(issue)).match(/data-reason="sources_unresolved"[^>]*>(.*?)<\/p>/)?.[1] ?? ''
    const unresolved = (overrides: Partial<Issue>, kinds?: { group?: string; kind: string }[]) =>
      correlated({ ...overrides, correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason: 'sources_unresolved', ...(kinds ? { kinds } : {}) } } })
    const workload = lineFor(unresolved({}, [{ group: 'argoproj.io', kind: 'Rollout' }]))
    const webhook = lineFor(unresolved({ kind: 'ReplicaSet', category: 'admission_webhook_blocking' }, [
      { group: 'admissionregistration.k8s.io', kind: 'MutatingWebhookConfiguration' },
      { group: 'admissionregistration.k8s.io', kind: 'ValidatingWebhookConfiguration' },
    ]))
    const quota = lineFor(unresolved({ kind: 'Job', group: 'batch', category: 'quota_exceeded' }, [{ kind: 'ResourceQuota' }]))
    const policy = lineFor(unresolved({ kind: 'Job', group: 'batch', category: 'quota_exceeded' }))

    it('gives each case its own line', () => {
      const all = [workload, webhook, quota, policy]
      all.forEach((line) => expect(line).not.toBe(''))
      expect(new Set(all).size).toBe(4)
    })
    it('mentions ConfigMaps only for a workload', () => {
      expect(workload).toContain('ConfigMaps')
      for (const line of [webhook, quota, policy]) expect(line).not.toContain('ConfigMaps')
    })
    it('names the policy kind when the server names it', () => {
      expect(webhook).toContain('webhook configuration')
      expect(quota).toContain('ResourceQuota')
    })
  })

  it('says the read was incomplete, and claims no absence, for an empty incomplete answer', () => {
    const html = openRow(loaded(correlated({ correlated_changes: [], correlated_changes_incomplete: true, no_recent_changes: { window_seconds: 3600 } })))
    expect(markers(html)).toEqual(['incomplete'])
    expect(groups(html)).toEqual([])
  })

  it('names the kinds an unknown reason is about', () => {
    const html = openRow(loaded(correlated({ correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason: 'user_forbidden', kinds: [{ kind: 'ConfigMap' }, { kind: 'Secret' }] } } })))
    expect(html).toContain('ConfigMaps and Secrets')
  })

  it('gives each distinct unknown reason a distinct line', () => {
    const line = (reason: IssueCorrelationUnknownReason) => {
      const html = openRow(loaded(correlated({ correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason, kinds: [{ kind: 'ConfigMap' }] } } })))
      return html.match(new RegExp(`data-reason="${reason}"[^>]*>(.*?)</p>`))?.[1]
    }
    const lines = (['user_forbidden', 'user_permission_check_failed', 'radar_kind_not_collected', 'kind_sync_pending', 'sources_unresolved', 'read_error'] as const).map(line)
    expect(new Set(lines).size).toBe(lines.length)
  })

  it('offers Retry on a read error', () => {
    const html = openRow(loaded(correlated({ correlation: { window_from: at(-3600), window_to: at(0), unknown: { reason: 'read_error' } } })))
    expect(html).toMatch(/data-reason="read_error"[^>]*>.*<button/)
  })

  it.each(['observation_started_late', 'events_evicted'])('explains incomplete history (%s) and keeps recorded changes', (reason) => {
    const html = openRow(loaded(correlated({
      correlated_changes: [change({})],
      correlation: { window_from: at(-3600), window_to: at(0), observed_since: at(-120), unknown: { reason: 'history_incomplete' }, history: { incomplete: true, reasons: [reason] } },
    })))
    expect(reasons(html)).toEqual([reason])
    expect(groups(html)).toEqual(['before'])
  })

  it.each([
    ['no changes, no marker, no unknown', {}],
    ['history coverage alone', { history: { incomplete: true, reasons: ['observation_started_late'] } }],
  ])('renders no section for an answer with nothing to say: %s', (_name, extra) => {
    const html = openRow(loaded(correlated({ kind: 'Job', group: 'batch', category: 'quota_exceeded', correlated_changes: [], correlation: { window_from: at(-3600), window_to: at(0), ...extra } })))
    expect(hasSection(html)).toBe(false)
    expect(markers(html)).toEqual([])
  })

  describe('the started-watching line', () => {
    const lineFor = (history: { incomplete: boolean; complete_since?: string; reasons: string[] }) => {
      const html = openRow(loaded(correlated({
        correlated_changes: [change({})],
        correlation: { window_from: at(-3600), window_to: at(0), observed_since: at(-7200), unknown: { reason: 'history_incomplete' }, history },
      })))
      return html.match(/data-reason="observation_started_late"[^>]*>(.*?)<\/p>/)?.[1] ?? ''
    }
    const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })

    it('names the coverage boundary, not the process start', () => {
      const line = lineFor({ incomplete: true, complete_since: at(-600), reasons: ['observation_started_late'] })
      expect(line).toContain(clock(at(-600)))
      expect(line).not.toContain(clock(at(-7200)))
    })

    it('names no time when the boundary is withheld', () => {
      const line = lineFor({ incomplete: true, reasons: ['observation_started_late'] })
      expect(line).not.toBe('')
      expect(line).not.toContain(clock(at(-7200)))
      expect(line).not.toMatch(/\d{1,2}:\d{2}/)
    })
  })

  it('shows no history line when the server sends history coverage but no unknown reason', () => {
    const html = openRow(loaded(correlated({
      correlated_changes: [change({})],
      correlation: { window_from: at(-3600), window_to: at(0), observed_since: at(-120), history: { incomplete: true, reasons: ['observation_started_late', 'events_evicted'] } },
    })))
    expect(markers(html)).toEqual([])
    expect(reasons(html)).toEqual([])
  })

  it('is absent for an issue the server did not correlate (ineligible kind), with no loading or not-found state', () => {
    const html = openRow(loaded({ ...listIssue }))
    expect(hasSection(html)).toBe(false)
    expect(markers(html)).toEqual([])
  })

  it.each([
    ['loading', { recentChanges: { status: 'loading' } } as IssueExpansion],
    ['failed', { recentChanges: { status: 'failed' }, onRetry: () => {} } as IssueExpansion],
    ['not-found', { recentChanges: { status: 'not_found' } } as IssueExpansion],
    ['unsupported', { recentChanges: { status: 'unsupported', upgrade: { feature: 'Recent changes on issues' } } } as IssueExpansion],
  ])('renders the %s state', (marker, expansion) => {
    const html = openRow(expansion)
    expect(markers(html)).toEqual([marker])
  })

  it('keeps the last answer and says the refresh failed, with Retry', () => {
    const fetchedAt = Date.parse(at(-30))
    const fresh = correlated({ correlated_changes: [change({})], issue_timing: 'started_after_resource_was_healthy', issue_timing_basis: 'condition' })
    const html = openRow({ ...loaded(fresh), refreshFailed: { fetchedAt } })
    expect(markers(html)).toEqual(['refresh-failed'])
    expect(html).toMatch(/data-recent-changes="refresh-failed"[^>]*>.*?<button/)
    expect(groups(html)).toEqual(['before'])
    expect(html).toContain(new Date(fetchedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))
    expect(html).toContain(issueTimingForDisplay(fresh)!.chip)
  })

  it('gives screen readers the field paths that only the tooltip shows', () => {
    const c = change({ summary: 'image: web:1 -> web:2', fields: [{ path: 'spec.template.spec.containers[web].image' }] })
    const html = openRow(loaded(correlated({ correlated_changes: [c] })))
    const time = html.match(new RegExp(`<time[^>]*dateTime="${c.timestamp}"[^>]*>.*?</time>`))?.[0] ?? ''
    const srOnly = time.match(/class="sr-only">(.*?)<\/span>/)?.[1] ?? ''
    expect(srOnly).toContain('spec.template.spec.containers[web].image')
  })

  it('makes the exact change time reachable by keyboard and screen readers', () => {
    const c = change({})
    const html = openRow(loaded(correlated({ correlated_changes: [c] })))
    const time = html.match(new RegExp(`<time[^>]*dateTime="${c.timestamp}"[^>]*>.*?</time>`))?.[0] ?? ''
    expect(time).toMatch(/tabindex="0"/)
    expect(time).toMatch(/class="sr-only"/)
    expect(time).toContain(new Date(c.timestamp).toLocaleString())
  })

  it('names the feature in the old-Radar note', () => {
    const html = openRow({ recentChanges: { status: 'unsupported', upgrade: { feature: 'Recent changes on issues' } } })
    expect(html).toContain('Recent changes on issues')
  })
})

describe('the open row renders the fresh issue', () => {
  const fresh = correlated({ issue_timing: 'started_after_resource_was_healthy', issue_timing_basis: 'condition', no_recent_changes: { window_seconds: 3600 } })
  const listChip = issueTimingForDisplay(listIssue)!.chip
  const freshChip = issueTimingForDisplay(fresh)!.chip

  it('uses the fresh timing in the header chip and diagnosis', () => {
    expect(listChip).not.toBe(freshChip)
    const html = openRow(loaded(fresh))
    expect(html).toContain(freshChip)
    expect(html).not.toContain(listChip)
  })

  it('keeps the list copy while collapsed', () => {
    const html = renderToString(<IssueRow issue={listIssue} open={false} onToggle={() => {}} expandedIssue={loaded(fresh)} />)
    expect(html).toContain(listChip)
    expect(html).not.toContain(freshChip)
  })

  it('keeps the list copy when the fresh issue is not available', () => {
    const html = openRow({ recentChanges: { status: 'not_found' } })
    expect(html).toContain(listChip)
  })
})
