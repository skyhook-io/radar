import { describe, expect, it } from 'vitest'
import { describeGitOpsOwnerSource } from './useResolvedGitOpsOwner'

const owner = { tool: 'fluxcd', kind: 'kustomizations', namespace: 'flux-system', name: 'apps' } as const
const nameKey = 'kustomize.toolkit.fluxcd.io/name'
const nsKey = 'kustomize.toolkit.fluxcd.io/namespace'

describe('GitOps owner evidence', () => {
  it('shows only the paired labels that identify the resolved manager', () => {
    expect(describeGitOpsOwnerSource(owner, { metadata: { labels: { [nameKey]: 'apps', [nsKey]: 'flux-system' } } })).toBe(`${nameKey}=apps, ${nsKey}=flux-system`)
  })
  it.each([
    { [nameKey]: 'flux-system', [nsKey]: 'flux-system' },
    { [nameKey]: 'apps', [nsKey]: 'other' },
    { [nameKey]: 'apps' },
  ])('does not cite stale or incomplete origin labels: %j', labels => {
    expect(describeGitOpsOwnerSource(owner, { metadata: { labels } })).toBeNull()
  })
  it('does not cite an Argo annotation for a Flux manager', () => {
    expect(describeGitOpsOwnerSource(owner, { metadata: { annotations: { 'argocd.argoproj.io/tracking-id': 'old:apps/Deployment:prod/web' } } })).toBeNull()
  })
})
