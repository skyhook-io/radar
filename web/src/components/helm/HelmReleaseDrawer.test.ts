import { describe, expect, it } from 'vitest'
import { effectiveHelmStorageNamespace, isUpgradeSourceIssueActionable } from './HelmReleaseDrawer'

describe('isUpgradeSourceIssueActionable', () => {
  it('allows explicit association for ambiguous and unavailable sources', () => {
    expect(isUpgradeSourceIssueActionable('ambiguous_repository')).toBe(true)
    expect(isUpgradeSourceIssueActionable('ambiguous_source')).toBe(true)
    expect(isUpgradeSourceIssueActionable('source_unavailable')).toBe(true)
  })

  it('lets OCI registration help untracked and repo-index states', () => {
    expect(isUpgradeSourceIssueActionable('untracked')).toBe(true)
    expect(isUpgradeSourceIssueActionable('repo_index_error')).toBe(true)
  })

  it('does not treat a missing reason code as actionable', () => {
    expect(isUpgradeSourceIssueActionable(undefined)).toBe(false)
  })
})

describe('effectiveHelmStorageNamespace', () => {
  it('falls back to the release namespace when storageNamespace is empty', () => {
    expect(effectiveHelmStorageNamespace({ namespace: 'production', name: 'release', storageNamespace: '' })).toBe('production')
  })

  it('uses an explicit storage namespace', () => {
    expect(effectiveHelmStorageNamespace({ namespace: 'production', name: 'release', storageNamespace: 'helm-storage' })).toBe('helm-storage')
  })
})
