import { describe, expect, it } from 'vitest'
import { helmReadIdentity } from './helmReadIdentity'

const known = <T,>(value: T) => ({ value, settled: true })
const loading = { value: undefined, settled: false }
const failed = { value: undefined, settled: true }

describe('helmReadIdentity', () => {
  it('auth on reads as the user, wherever Radar runs', () => {
    expect(helmReadIdentity(known(true), known('local'))).toBe('user')
    expect(helmReadIdentity(known(true), known('in-cluster'))).toBe('user')
  })

  it('cloud mode always reads as the user, even if auth status never loads', () => {
    expect(helmReadIdentity(loading, known('cloud'))).toBe('user')
    expect(helmReadIdentity(failed, known('cloud'))).toBe('user')
  })

  it('auth off reads as the kubeconfig identity locally and the ServiceAccount in-cluster', () => {
    expect(helmReadIdentity(known(false), known('local'))).toBe('kubeconfig')
    expect(helmReadIdentity(known(false), known('in-cluster'))).toBe('serviceAccount')
  })

  it('waits while either fact is loading', () => {
    expect(helmReadIdentity(loading, known('in-cluster'))).toBe('pending')
    expect(helmReadIdentity(known(false), loading)).toBe('pending')
  })

  it('an older Radar without deployment info is unknown, not loading forever', () => {
    expect(helmReadIdentity(known(false), { value: undefined, settled: true })).toBe('unknown')
  })

  it('a failed lookup is unknown, never a guessed remedy', () => {
    expect(helmReadIdentity(failed, known('in-cluster'))).toBe('unknown')
    expect(helmReadIdentity(known(false), failed)).toBe('unknown')
  })
})
