import { describe, expect, it } from 'vitest'
import { isUpgradeSourceIssueActionable } from './HelmReleaseDrawer'

describe('isUpgradeSourceIssueActionable', () => {
  it('keeps classic repository ambiguity informational', () => {
    expect(isUpgradeSourceIssueActionable('ambiguous_repository')).toBe(false)
  })

  it('lets OCI registration help untracked and repo-index states', () => {
    expect(isUpgradeSourceIssueActionable('untracked')).toBe(true)
    expect(isUpgradeSourceIssueActionable('repo_index_error')).toBe(true)
  })

  it('does not treat a missing reason code as actionable', () => {
    expect(isUpgradeSourceIssueActionable(undefined)).toBe(false)
  })
})
