import { describe, expect, it } from 'vitest'
import { DEFAULT_SHOW_EFFECTIVE_VALUES, isUpgradeSourceIssueActionable } from './HelmReleaseDrawer'

describe('Helm values view', () => {
  it('opens on user overrides', () => {
    expect(DEFAULT_SHOW_EFFECTIVE_VALUES).toBe(false)
  })
})

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
