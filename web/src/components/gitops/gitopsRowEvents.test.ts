import { describe, expect, it } from 'vitest'
import { isGitOpsCountGroup } from '@skyhook-io/k8s-ui'
import { changesGitOpsRow, gitopsTableChange } from './gitopsRowEvents'

describe('changesGitOpsRow', () => {
  it('matches the kinds the GitOps table lists', () => {
    expect(changesGitOpsRow('Application', 'argoproj.io')).toBe(true)
    expect(changesGitOpsRow('ApplicationSet', 'argoproj.io')).toBe(true)
    expect(changesGitOpsRow('Kustomization', 'kustomize.toolkit.fluxcd.io')).toBe(true)
    expect(changesGitOpsRow('HelmRelease', 'helm.toolkit.fluxcd.io')).toBe(true)
  })

  it('matches the Flux sources whose URLs the rows show', () => {
    for (const kind of ['GitRepository', 'HelmRepository', 'OCIRepository', 'Bucket']) {
      expect(changesGitOpsRow(kind, 'source.toolkit.fluxcd.io')).toBe(true)
    }
    expect(changesGitOpsRow('Alert', 'notification.toolkit.fluxcd.io')).toBe(false)
  })

  it('ignores other Argo kinds and same-named kinds from other groups', () => {
    expect(changesGitOpsRow('Rollout', 'argoproj.io')).toBe(false)
    expect(changesGitOpsRow('Workflow', 'argoproj.io')).toBe(false)
    expect(changesGitOpsRow('Application', 'app.k8s.io')).toBe(false)
    expect(changesGitOpsRow('Kustomization', 'kustomize.config.k8s.io')).toBe(false)
    expect(changesGitOpsRow('HelmRelease', undefined)).toBe(false)
  })
})

describe('isGitOpsCountGroup', () => {
  it('covers every group the GitOps totals count, and nothing else', () => {
    for (const group of ['argoproj.io', 'source.toolkit.fluxcd.io', 'kustomize.toolkit.fluxcd.io', 'helm.toolkit.fluxcd.io', 'notification.toolkit.fluxcd.io']) {
      expect(isGitOpsCountGroup(group)).toBe(true)
    }
    expect(isGitOpsCountGroup('apps')).toBe(false)
    expect(isGitOpsCountGroup('')).toBe(false)
    expect(isGitOpsCountGroup(undefined)).toBe(false)
  })
})

describe('gitopsTableChange', () => {
  it('refreshes rows on any row change but totals only on add or delete', () => {
    expect(gitopsTableChange('Application', 'argoproj.io', 'update')).toEqual({ rows: true, counts: false })
    expect(gitopsTableChange('Application', 'argoproj.io', 'add')).toEqual({ rows: true, counts: true })
    expect(gitopsTableChange('Alert', 'notification.toolkit.fluxcd.io', 'delete')).toEqual({ rows: false, counts: true })
    expect(gitopsTableChange('Alert', 'notification.toolkit.fluxcd.io', 'update')).toEqual({ rows: false, counts: false })
    expect(gitopsTableChange('Deployment', 'apps', 'add')).toEqual({ rows: false, counts: false })
  })
})
