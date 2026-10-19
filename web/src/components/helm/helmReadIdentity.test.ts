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

  it('cloud mode reads as the Cloud user, even if auth status never loads', () => {
    expect(helmReadIdentity(known(true), known('cloud'))).toBe('cloudUser')
    expect(helmReadIdentity(loading, known('cloud'))).toBe('cloudUser')
    expect(helmReadIdentity(failed, known('cloud'))).toBe('cloudUser')
  })

  it('auth off reads as the kubeconfig identity locally and the ServiceAccount in-cluster', () => {
    expect(helmReadIdentity(known(false), known('local'))).toBe('kubeconfig')
    expect(helmReadIdentity(known(false), known('in-cluster'))).toBe('serviceAccount')
  })

  it('waits while either fact is loading', () => {
    expect(helmReadIdentity(loading, known('in-cluster'))).toBe('pending')
    expect(helmReadIdentity(known(false), loading)).toBe('pending')
  })

  it('a Cloud viewer is told apart from other Cloud users', () => {
    expect(helmReadIdentity(known(true), known('cloud'), 'viewer')).toBe('cloudViewer')
    expect(helmReadIdentity(known(true), known('cloud'), 'member')).toBe('cloudUser')
  })

  it('a Cloud role marks Cloud even when an older agent reports no mode', () => {
    expect(helmReadIdentity(known(true), failed, 'viewer')).toBe('cloudViewer')
  })

  it('auth on still waits for the deployment mode, since cloud users get different advice', () => {
    expect(helmReadIdentity(known(true), loading)).toBe('pending')
  })

  it('auth on with an older Radar that reports no deployment mode reads as the user', () => {
    expect(helmReadIdentity(known(true), failed)).toBe('user')
  })

  it('an older Radar without deployment info is unknown, not loading forever', () => {
    expect(helmReadIdentity(known(false), { value: undefined, settled: true })).toBe('unknown')
  })

  it('a failed lookup is unknown, never a guessed remedy', () => {
    expect(helmReadIdentity(failed, known('in-cluster'))).toBe('unknown')
    expect(helmReadIdentity(known(false), failed)).toBe('unknown')
  })
})
