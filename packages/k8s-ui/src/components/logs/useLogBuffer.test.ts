import { describe, expect, it } from 'vitest'
import { detectLogLevel } from './useLogBuffer'

describe('detectLogLevel with CloudNativePG records', () => {
  it('uses the nested PostgreSQL severity over the instance manager level', () => {
    const line = (sev: string) => JSON.stringify({ level: 'info', logger: 'postgres', msg: 'record', record: { error_severity: sev, message: 'x' } })
    expect(detectLogLevel(line('ERROR'))).toBe('error')
    expect(detectLogLevel(line('FATAL'))).toBe('error')
    expect(detectLogLevel(line('WARNING'))).toBe('warn')
    expect(detectLogLevel(line('LOG'))).toBe('info')
    expect(detectLogLevel(line('DEBUG1'))).toBe('debug')
  })
  it('ignores a record that is not a PostgreSQL one', () => {
    expect(detectLogLevel(JSON.stringify({ level: 'error', msg: 'request failed', record: { error_severity: 20 } }))).toBe('error')
    expect(detectLogLevel(JSON.stringify({ level: 'error', msg: 'x', record: { error_severity: 'minor' } }))).toBe('error')
  })
})
