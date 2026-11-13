import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { GitOpsOutsideChanges } from '../../../types/gitops-outside'
import { GitOpsOutsideChangesView } from './GitOpsOutsideChangesView'

const base: GitOpsOutsideChanges = {
  root: { group: 'helm.toolkit.fluxcd.io', kind: 'HelmRelease', namespace: 'prod', name: 'api' },
  policy: { tool: 'fluxcd', driftDetection: 'warn', suspended: false },
  count: 0, objects: [], harmless: [], otherWriters: [], driftEvents: [],
  coverage: { listed: 4 },
}
const render = (data: Partial<GitOpsOutsideChanges>) => renderToString(<GitOpsOutsideChangesView data={{ ...base, ...data }} />).replace(/<!-- -->/g, '')

describe('GitOpsOutsideChangesView', () => {
  it('lists a hand edit with its tool, value and what Flux does', () => {
    const html = render({
      count: 1,
      objects: [{ ref: { group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'api' }, changes: [
        { manager: 'kubectl-patch', tool: 'kubectl patch', operation: 'Update', time: '2026-11-09T10:00:00Z', paths: ['spec.replicas'], morePaths: 2, values: { 'spec.replicas': '3' }, outcome: 'reported', harmless: false },
      ] }],
      harmless: [{ tool: 'kubectl rollout restart', count: 2 }],
      otherWriters: [{ manager: 'helm', objects: 14 }],
    })
    expect(html).toContain('Deployment')
    expect(html).toContain('spec.replicas')
    expect(html).toContain('kubectl patch')
    // A long path truncates; the value stays visible beside it.
    expect(html).toMatch(/<span class="truncate">spec\.replicas<\/span><span class="shrink-0[^"]*">\s*=\s*3<\/span>/)
    expect(html).toContain('+2 more')
    expect(html).toContain('Flux reports this drift and leaves it')
    expect(html).toContain('kubectl rollout restart')
    expect(html).toContain('helm')
  })

  it('says nothing changed when no object has a hand edit', () => {
    expect(render({})).toContain('Nothing was changed outside Flux in the objects Radar could read.')
  })

  it('says when the list is incomplete', () => {
    const html = render({ coverage: { listed: 2000, truncated: true, denied: [{ resource: 'services', namespace: 'prod' }], skipped: ['secrets'] } })
    expect(html).toMatch(/incomplete/i)
    expect(html).toContain('services in prod')
    expect(html).toContain('Secrets are never read')
    const failed = render({ coverage: { listed: 3, failed: [{ resource: 'widgets', namespace: 'prod' }] } })
    expect(failed).toContain("Radar couldn&#x27;t list widgets in prod")
    expect(failed).not.toContain("You can&#x27;t list")
  })

  it('says when Radar could not read the policy', () => {
    const html = render({ count: 1, objects: [{ ref: { group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'api' }, changes: [
      { manager: 'k9s', tool: 'k9s', operation: 'Update', paths: ['spec.paused'], morePaths: 0, outcome: 'unknown', harmless: false },
    ] }] })
    expect(html).toContain("Radar couldn&#x27;t read this object&#x27;s Flux policy.")
  })

  it('lists drift Flux reported', () => {
    const html = render({ driftEvents: [{ type: 'Warning', reason: 'DriftDetected', message: 'Deployment/prod/api drifted', lastTimestamp: '2026-11-09T10:00:00Z' }] })
    expect(html).toContain('Flux reported drift')
    expect(html).toContain('Deployment/prod/api drifted')
  })

  it('says when an object opts out of reconciliation', () => {
    const html = render({ count: 1, objects: [{ ref: { group: 'apps', kind: 'Deployment', namespace: 'prod', name: 'api' }, changes: [
      { manager: 'kubectl-edit', tool: 'kubectl edit', operation: 'Update', paths: ['spec.replicas'], morePaths: 0, outcome: 'opted-out', harmless: false },
    ] }] })
    expect(html).toContain('This object opts out of Flux reconciliation')
  })

  it('says when the root applies elsewhere or records no inventory', () => {
    expect(render({ coverage: { listed: 0, remote: true } })).toContain('applies to another cluster')
    expect(render({ coverage: { listed: 0, noInventory: true } })).toContain('records no inventory')
  })
})
