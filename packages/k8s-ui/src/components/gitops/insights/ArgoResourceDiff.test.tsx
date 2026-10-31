import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, test } from 'vitest'

import type { GitOpsChange, GitOpsDrift, GitOpsInsight, GitOpsResourceDiff } from '../../../types'
import { ArgoResourceDiff } from './ArgoResourceDiff'
import { GitOpsChangesView } from './GitOpsInsightViews'

// Shape seen live on Argo CD 3.5.2: the Deployment was scaled by hand (1 -> 3)
// and a Git commit bumping the image hasn't synced yet. last-applied holds the
// manifest from the last sync, so it only sees the scale.
const lastApplied: GitOpsDrift = {
  source: 'lastAppliedAnnotation',
  entries: [{ path: 'spec.replicas', op: 'changed', desired: '1', live: '3' }],
}

const argoDiff: GitOpsResourceDiff = {
  source: 'argocd-api',
  desired: 'spec:\n  replicas: 1\n  image: nginx:1.28-alpine\n',
  live: 'spec:\n  replicas: 3\n  image: nginx:1.27-alpine\n',
  fieldEntries: [
    { path: 'spec.replicas', op: 'changed', desired: '1', live: '3' },
    { path: 'spec.template.spec.containers.[0].image', op: 'changed', desired: '"nginx:1.28-alpine"', live: '"nginx:1.27-alpine"' },
  ],
  redacted: false,
  hook: false,
}

const IMAGE_PATH = 'spec.template.spec.containers.[0].image'

const count = (html: string, needle: string) => html.split(needle).length - 1

describe('ArgoResourceDiff field list', () => {
  test("shows Argo's field list above the YAML diff, including the unsynced Git change", () => {
    const html = renderToStaticMarkup(<ArgoResourceDiff diff={argoDiff} loading={false} fallbackDrift={lastApplied} />)
    expect(html).toContain('Field diff')
    expect(html).toContain(IMAGE_PATH)
    expect(html).toContain('via Argo CD API')
    expect(html.indexOf('Field diff')).toBeLessThan(html.indexOf('desired (Git-rendered)'))
    // One list, not Argo's plus the last-applied fallback.
    expect(count(html, 'Field diff')).toBe(1)
  })

  test('renders no field list box when Argo reports no field differences', () => {
    const html = renderToStaticMarkup(<ArgoResourceDiff diff={{ ...argoDiff, fieldEntries: [] }} loading={false} fallbackDrift={lastApplied} />)
    expect(html).not.toContain('Field diff')
  })

  test('treats a null field list (sent by older Radars) as empty instead of crashing', () => {
    const html = renderToStaticMarkup(<ArgoResourceDiff diff={{ ...argoDiff, fieldEntries: null }} loading={false} fallbackDrift={lastApplied} />)
    expect(html).not.toContain('Field diff')
    expect(html).toContain('desired (Git-rendered)')
  })

  test('says the list was cut short instead of presenting the cap as the count', () => {
    const html = renderToStaticMarkup(<ArgoResourceDiff diff={{ ...argoDiff, fieldEntriesTruncated: true }} loading={false} />)
    expect(html).toContain('showing first 50 entries')
    expect(html).not.toContain('2 fields')
  })

  test("falls back to the last-applied list when Argo's diff fails to load", () => {
    const html = renderToStaticMarkup(<ArgoResourceDiff loading={false} error="Failed to fetch the diff from the Argo CD API server." fallbackDrift={lastApplied} />)
    expect(html).toContain('Field diff')
    expect(html).toContain('spec.replicas')
    expect(html).not.toContain('via Argo CD API')
    expect(html).toContain('Failed to fetch the diff from the Argo CD API server.')
  })
})

describe('GitOpsChangesView expanded Argo row', () => {
  const change: GitOpsChange = {
    ref: { group: 'apps', kind: 'Deployment', namespace: 'web', name: 'web' },
    category: 'OutOfSync',
    sync: 'OutOfSync',
    health: 'Healthy',
    hasDesired: true,
    hasLive: true,
    drift: lastApplied,
    partial: false,
  }
  const insight = (argoDiffAvailable: boolean): GitOpsInsight => ({
    summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'web', sync: 'OutOfSync', health: 'Healthy' },
    changes: [change],
    capabilities: {
      sync: true, refresh: true, terminate: false, suspend: false, resume: false,
      syncWithSource: false, selectiveSync: true, rollback: false, argoDiffAvailable,
    },
  })

  test('does not offer a live drawer for confirmed absence with unknown health', () => {
    const data = insight(false)
    data.changes = [{ ...change, missing: true, hasLive: false, health: 'Unknown', drift: undefined }]
    const html = renderToStaticMarkup(<GitOpsChangesView insight={data} onOpenResource={() => {}} />)
    expect(html).not.toContain('Open Deployment')
    expect(html).toContain('No live resource')
    expect(html).toContain('Not present in the cluster')
  })

  test('keeps unknown-health live objects navigable', () => {
    const data = insight(false)
    data.changes = [{ ...change, health: 'Unknown', drift: undefined }]
    const html = renderToStaticMarkup(<GitOpsChangesView insight={data} onOpenResource={() => {}} />)
    expect(html).toContain('Open Deployment')
  })

  test('does not report confirmed absence as healthy or tell a Synced resource to sync', () => {
    const data = insight(false)
    data.changes = [{ ...change, missing: true, hasLive: false, health: 'Healthy', sync: 'Synced', category: 'Synced', message: 'Controller diagnostic detail', drift: undefined }]
    const html = renderToStaticMarkup(<GitOpsChangesView insight={data} onOpenResource={() => {}} />)
    expect(html).not.toContain('1/1 resources healthy')
    expect(html).toContain('Missing')
    expect(html).toContain('Refresh the application')
    expect(html).toContain('Controller diagnostic detail')
    expect(html).not.toContain('Sync the application to create it')
  })

  test.each([
    ['ManualDrift', 'The render includes none of the resources this Application manages.', 'Manual'],
    ['AutoSyncBlockedEmpty', 'A pruning sync could delete every listed resource.', 'Auto · prune'],
    ['SharedResourceWarning', 'Resolve shared ownership before syncing.', 'Manual'],
    ['SelfHealDisabled', 'Resolve shared ownership before syncing.', 'Auto'],
    ['Failed', 'Fix the comparison error and refresh before syncing.', 'Manual'],
  ])('defers resource sync/create advice to the %s hazard', (reason, hazard, autoSyncMode) => {
    const data = insight(false)
    data.summary.autoSyncMode = autoSyncMode
    data.issues = [{ severity: 'warning', scope: 'operation', reason, message: 'Sync warning', hazard }]
    data.changes = [
      { ...change, drift: undefined },
      { ...change, ref: { ...change.ref, name: 'missing' }, missing: true, hasLive: false, health: 'Missing', drift: undefined },
      { ...change, ref: { ...change.ref, name: 'health-missing' }, sync: 'Unknown', health: 'Missing', drift: undefined },
    ]
    const html = renderToStaticMarkup(<GitOpsChangesView insight={data} />)
    expect(count(html, 'See the sync warning above before syncing this Application.')).toBe(3)
    expect(html).not.toContain('Click Sync')
    expect(html).not.toContain('click Sync to force it')
    expect(html).not.toContain('Auto-sync should reconcile')
    expect(html).not.toContain('Sync the application to create it')
    expect(html).not.toContain('Sync to create it')
  })

  test('keeps ordinary sync advice when there is no hazard', () => {
    const data = insight(false)
    data.summary.autoSyncMode = 'Manual'
    const html = renderToStaticMarkup(<GitOpsChangesView insight={data} />)
    expect(html).toContain('Click Sync to apply the desired state.')
    expect(html).not.toContain('See the sync warning above')
  })

  test("hands the last-applied list to the Argo diff instead of rendering it alongside", () => {
    let passed: GitOpsDrift | undefined
    const html = renderToStaticMarkup(
      <GitOpsChangesView
        insight={insight(true)}
        focusKey="Deployment/web/web"
        renderResourceDiff={(_ref, fallbackDrift) => {
          passed = fallbackDrift
          return <div data-testid="argo-diff" />
        }}
      />,
    )
    expect(html).toContain('argo-diff')
    expect(html).not.toContain('Field diff')
    expect(passed).toEqual(lastApplied)
    // The row's "N diff" badge counts the last-applied list, which can
    // undercount here, so it isn't shown.
    expect(html).not.toContain('1 diff')
  })

  test('without the Argo diff, the row still shows the last-applied list', () => {
    const html = renderToStaticMarkup(<GitOpsChangesView insight={insight(false)} focusKey="Deployment/web/web" />)
    expect(html).toContain('Field diff')
    expect(html).toContain('spec.replicas')
    expect(html).toContain('1 diff')
  })
})
