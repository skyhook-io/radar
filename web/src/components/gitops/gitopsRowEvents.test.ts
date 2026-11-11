import { describe, expect, it } from 'vitest'
import { changesGitOpsRow } from './gitopsRowEvents'

describe('changesGitOpsRow', () => {
  it('matches the kinds the GitOps table lists', () => {
    expect(changesGitOpsRow('Application', 'argoproj.io')).toBe(true)
    expect(changesGitOpsRow('ApplicationSet', 'argoproj.io')).toBe(true)
    expect(changesGitOpsRow('Kustomization', 'kustomize.toolkit.fluxcd.io')).toBe(true)
    expect(changesGitOpsRow('HelmRelease', 'helm.toolkit.fluxcd.io')).toBe(true)
  })

  it('ignores other Argo kinds and same-named kinds from other groups', () => {
    expect(changesGitOpsRow('Rollout', 'argoproj.io')).toBe(false)
    expect(changesGitOpsRow('Workflow', 'argoproj.io')).toBe(false)
    expect(changesGitOpsRow('Application', 'app.k8s.io')).toBe(false)
    expect(changesGitOpsRow('Kustomization', 'kustomize.config.k8s.io')).toBe(false)
    expect(changesGitOpsRow('HelmRelease', undefined)).toBe(false)
  })
})
