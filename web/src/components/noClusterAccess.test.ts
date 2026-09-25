import { describe, expect, it } from 'vitest'
import { bindableGroups } from './noClusterAccess'

describe('bindableGroups', () => {
  it('keeps only person-identifying Radar Cloud groups', () => {
    expect(
      bindableGroups([
        'radar:viewer',
        'radar:org:o1',
        'radar:user:u1',
        'radar:email:bob@example.com',
        'radar:idp:team-a',
        'cloud:viewer',
      ]),
    ).toEqual(['radar:user:u1', 'radar:email:bob@example.com', 'radar:idp:team-a'])
  })

  it('keeps every group outside Cloud', () => {
    expect(bindableGroups(['oidc:platform', 'devs'])).toEqual(['oidc:platform', 'devs'])
  })
})
