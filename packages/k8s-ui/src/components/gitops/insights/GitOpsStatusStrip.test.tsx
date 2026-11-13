import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsStatusStrip } from './GitOpsInsightViews'
import type { GitOpsInsightSummary } from '../../../types/gitops-insights'

const summary: GitOpsInsightSummary = { tool: 'fluxcd', kind: 'Kustomization', namespace: 'flux-system', name: 'checkout', lastRevision: 'main@sha1:abcdef1234567890', lastReconcile: '2026-10-07T09:00:00Z' }
const render = (patch: Partial<GitOpsInsightSummary> = {}) => renderToString(<GitOpsStatusStrip insight={{ summary: { ...summary, ...patch } }} renderRevisionMeta={() => <span>commit metadata</span>} />).replace(/<!-- -->/g, '')

describe('GitOps status timing', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-30T21:00:00Z')) })
  afterEach(() => vi.useRealTimers())

  it('keeps recorded apply time outside the revision and preserves commit metadata', () => {
    const html = render({ lastApplyAttempt: '2026-10-30T20:55:00Z' })
    expect(html).toContain('Revision:')
    expect(html).toContain('Last apply attempt:')
    expect(html).toContain('5m ago')
    expect(html.indexOf('Last apply attempt:')).toBeGreaterThan(html.indexOf('commit metadata'))
    expect(html).not.toContain('23d ago')
    expect(html).not.toContain('Latest revision')
  })

  it('labels the apply time by its recorded outcome', () => {
    const applied = render({ lastApplied: '2026-10-30T20:55:00Z' })
    expect(applied).toContain('Last applied:')
    expect(applied).toContain('5m ago')
    expect(applied).not.toContain('attempt')
    const failed = render({ lastApplyAttempt: '2026-10-30T20:50:00Z' })
    expect(failed).toContain('Last apply attempt:')
    expect(failed).toContain('10m ago')
  })

  it.each(['Kustomization', 'HelmRelease'])('ignores the old backend Flux timestamp for %s', kind => {
    const html = render({ kind, lastRevision: undefined, targetRevision: '1.2.0' })
    expect(html).toContain('Revision:')
    expect(html).not.toContain('Attempted revision')
    expect(html).not.toContain('ago')
    expect(html).not.toContain('Last reconcile')
  })

  it('shows Helm deployment time only with the deployed revision', () => {
    expect(render({ kind: 'HelmRelease', lastRevision: '1.2.0', targetRevision: '1.3.0', lastDeployed: '2026-10-27T21:00:00Z' })).toContain('Last deployed:')
    expect(render({ kind: 'HelmRelease', lastRevision: undefined, targetRevision: '1.3.0', lastDeployed: '2026-10-27T21:00:00Z' })).not.toContain('Last deployed:')
  })

  it('names Argo comparison time independently and rejects invalid dates', () => {
    expect(render({ tool: 'argocd', kind: 'Application', lastReconcile: '2026-10-30T20:55:00Z' })).toContain('Last checked:')
    expect(render({ tool: 'argocd', kind: 'Application', lastReconcile: 'request-marker' })).not.toContain('Last checked:')
    expect(render({ tool: 'argocd', kind: 'ApplicationSet' })).not.toContain('Last checked:')
  })

  it('uses the same safe fields in terminating historical metadata', () => {
    const html = render({ terminating: true, terminationStartedAt: '2026-10-30T20:58:00Z', lastApplyAttempt: '2026-10-30T20:55:00Z' })
    expect(html).toContain('Pending deletion:')
    expect(html).toContain('Last apply attempt:')
    expect(html).not.toContain('23d ago')
  })
})

describe('Flux revision chain', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-10T10:05:00Z')) })
  afterEach(() => vi.useRealTimers())

  it('says when the source is ahead of what is applied', () => {
    const html = render({ sourceRevision: 'main@sha1:bbbbbbb1234567890', sourceFetchedAt: '2026-11-10T10:04:00Z', appliedRevision: 'main@sha1:aaaaaaa1234567890', pending: true, objects: 3 })
    expect(html).toContain('Pending: source is ahead of what is applied')
    expect(html).toContain('bbbbbbb')
    expect(html).toContain('aaaaaaa')
    expect(html).toContain('3 objects')
    expect(html).toContain('commit metadata')
    expect(html).not.toContain('Revision:')
  })

  it('shortens sha256 digests', () => {
    const html = render({ kind: 'HelmRelease', sourceRevision: 'latest@sha256:53884aebf2e5aa', appliedRevision: 'sha256:53884aebf2e5aa', pending: false })
    expect(html).toContain('53884ae')
    expect(html).not.toContain('53884aebf')
  })

  it('never claims up to date when the source is unknown', () => {
    const html = render({ appliedRevision: 'main@sha1:aaaaaaa1234567890' })
    expect(html).toContain('aaaaaaa')
    expect(html).not.toContain('Pending')
    expect(html).not.toMatch(/up to date/i)
    expect(html).not.toContain('Source')
  })

  it('shows the fetched source before the first apply', () => {
    const html = render({ lastRevision: undefined, sourceRevision: 'main@sha1:bbbbbbb1234567890', objects: 2 })
    expect(html).toContain('bbbbbbb')
    expect(html).toContain('2 objects')
    expect(html).not.toContain('Applied')
    expect(html).not.toContain('Revision:')
  })

  it('names the deployed chart beside a HelmRelease digest', () => {
    const html = render({ kind: 'HelmRelease', lastRevision: '5.5.0', sourceRevision: 'latest@sha256:53884aebf2e5aa', appliedRevision: 'latest@sha256:53884aebf2e5aa', pending: false })
    expect(html).toContain('5.5.0 (53884ae)')
  })

  it('keeps an OCI tag in a non-Flux revision', () => {
    const html = renderToString(<GitOpsStatusStrip insight={{ summary: { tool: 'argocd', kind: 'Application', namespace: 'argocd', name: 'app', lastRevision: '1.0.0@sha256:53884aebf2e5aa' } }} />).replace(/<!-- -->/g, '')
    expect(html).toContain('1.0.0@sha256:53884aebf2e5aa')
  })

  it('renders an older Radar summary as before', () => {
    const html = render()
    expect(html).toContain('Revision:')
    expect(html).not.toContain('Applied')
  })

  it('estimates the next Kustomization run', () => {
    const base = { appliedRevision: 'main@sha1:aaaaaaa1234567890', interval: '10m', lastApplied: '2026-11-10T10:00:00Z' }
    expect(render(base)).toContain('next run in ~5m')
    expect(render({ ...base, autoSyncMode: 'Suspended' })).not.toContain('next run')
    expect(render({ ...base, lastApplied: '2026-11-10T09:00:00Z' })).not.toContain('next run')
    expect(render({ ...base, kind: 'HelmRelease' })).not.toContain('next run')
    const failed = { appliedRevision: 'main@sha1:aaaaaaa1234567890', interval: '10m', retryInterval: '2m', lastApplyAttempt: '2026-11-10T10:04:00Z' }
    expect(render(failed)).toContain('next run in ~1m')
    expect(render({ ...failed, retryInterval: undefined })).toContain('next run in ~9m')
    expect(render({ ...failed, lastApplied: '2026-11-10T09:00:00Z' })).toContain('next run in ~1m')
  })
})
