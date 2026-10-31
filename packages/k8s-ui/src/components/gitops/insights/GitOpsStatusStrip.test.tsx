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
