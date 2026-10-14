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
