import { describe, expect, it } from 'vitest'
import { buildRbacRequest } from './RestrictedState'

describe('buildRbacRequest', () => {
  it('keeps the placeholder subject when the caller has no bindable groups', () => {
    const yaml = buildRbacRequest('', 'nodes', [])
    expect(yaml).toContain('name: <your-identity>')
  })

  it('binds only the first group and lists the rest as commented alternatives', () => {
    const yaml = buildRbacRequest('', 'nodes', ['radar:idp:team-a', 'radar:idp:everyone'])
    expect(yaml).toContain('    name: "radar:idp:team-a"')
    expect(yaml).toContain('  # or: name: "radar:idp:everyone"')
    expect(yaml).not.toMatch(/^ {4}name: "radar:idp:everyone"$/m)
    expect(yaml).not.toContain('<your-identity>')
  })
})
