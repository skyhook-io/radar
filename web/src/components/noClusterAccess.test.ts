import { describe, expect, it } from 'vitest'
import { bindableGroups, buildNoAccessBinding } from './noClusterAccess'

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

describe('buildNoAccessBinding', () => {
  it('grants nothing as-is and lists the groups as comments', () => {
    const yaml = buildNoAccessBinding(['radar:idp:team-a', 'radar:idp:everyone'])
    expect(yaml).toContain('    name: <group>')
    expect(yaml).toContain('  name: <cluster-role>')
    expect(yaml).toContain('#   radar:idp:everyone')
    expect(yaml).not.toMatch(/^\s+name: "?radar:idp/m)
  })

  it('omits the group list when there are none', () => {
    expect(buildNoAccessBinding([])).not.toContain('# Your groups')
  })
})
