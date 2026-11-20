import { describe, expect, it } from 'vitest'
import { unreadHelmNamespacesLine } from './HelmSummary'

describe('the Home Helm card when some namespaces went unread', () => {
  it('says nothing when everything was read', () => {
    expect(unreadHelmNamespacesLine(undefined)).toBeNull()
  })

  it('names a single namespace', () => {
    expect(unreadHelmNamespacesLine({ count: 1, names: ['team-b'] })).toBe('Helm’s records weren’t read in team-b. Open Helm for releases there.')
  })

  it('counts several', () => {
    expect(unreadHelmNamespacesLine({ count: 30, names: ['a', 'b'] })).toContain('in 30 namespaces')
  })
})
