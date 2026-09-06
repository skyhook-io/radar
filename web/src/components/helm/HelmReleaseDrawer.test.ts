import { describe, expect, it } from 'vitest'
import { isUpgradeSourceIssueActionable } from './HelmReleaseDrawer'

describe('isUpgradeSourceIssueActionable', () => {
  it('allows explicit recovery from classic or cross-source ambiguity', () => {
    expect(isUpgradeSourceIssueActionable('ambiguous_repository')).toBe(true)
    expect(isUpgradeSourceIssueActionable('ambiguous_source')).toBe(true)
  })

  it('lets OCI registration help untracked and repo-index states', () => {
    expect(isUpgradeSourceIssueActionable('untracked')).toBe(true)
    expect(isUpgradeSourceIssueActionable('repo_index_error')).toBe(true)
  })

  it('does not treat a missing reason code as actionable', () => {
    expect(isUpgradeSourceIssueActionable(undefined)).toBe(false)
  })
})
