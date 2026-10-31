import { describe, expect, it } from 'vitest'
import { getFluxDeployedRelease, getFluxHandledRequestToken, getFluxHelmChartVersion, getFluxLastApplyAttempt, getFluxLastStatusChange, getFluxReleaseAttemptTime } from './flux-history'

const kustomization = (history?: unknown[]) => ({
  apiVersion: 'kustomize.toolkit.fluxcd.io/v1', kind: 'Kustomization',
  status: { lastHandledReconcileAt: '2026-10-01T00:00:00Z', history },
})
const helm = (history: unknown[]) => ({ apiVersion: 'helm.toolkit.fluxcd.io/v2', kind: 'HelmRelease', status: { history } })

describe('Flux history', () => {
  it('selects the latest recorded apply across offsets and outcomes, preserving nanoseconds', () => {
    expect(getFluxLastApplyAttempt(kustomization([
      { firstReconciled: '2026-09-01T00:00:00Z', lastReconciled: '2026-10-30T12:00:00.123456788Z' },
      { lastReconciled: '2026-10-30T14:00:00.123456789+02:00', lastReconciledStatus: 'HealthCheckFailed' },
      { lastReconciled: '2026-10-30T13:00:00+02:00' },
      { lastReconciled: 'invalid' },
    ]))).toBe('2026-10-30T14:00:00.123456789+02:00')
  })

  it('never infers apply time from a request marker or a condition', () => {
    expect(getFluxLastApplyAttempt(kustomization())).toBeUndefined()
    expect(getFluxLastApplyAttempt(kustomization([{ lastReconciled: '2026-02-30T00:00:00Z' }, { lastReconciled: '0001-01-01T00:00:00Z' }]))).toBeUndefined()
    expect(getFluxLastApplyAttempt({ ...kustomization([{ lastReconciled: '2026-10-30T12:00:00Z' }]), apiVersion: 'example.io/v1' })).toBeUndefined()
  })

  it('keeps the deployed release identity and date together after a failed upgrade and rollback', () => {
    const entries = [
      { version: 5, chartVersion: '1.3.0', status: 'failed', lastDeployed: '2026-10-30T12:00:00Z' },
      { version: 4, chartVersion: '1.2.0', status: 'deployed', lastDeployed: '2026-10-27T12:00:00Z' },
    ]
    expect(getFluxDeployedRelease(helm(entries))).toMatchObject({ version: 4, chartVersion: '1.2.0', lastDeployed: '2026-10-27T12:00:00Z' })
    expect(getFluxDeployedRelease(helm([...entries, { version: 6, chartVersion: '1.1.0', status: 'deployed', lastDeployed: 'invalid' }]))).toMatchObject({ version: 6, chartVersion: '1.1.0', lastDeployed: undefined })
    expect(getFluxDeployedRelease(helm([entries[0]]))).toBeUndefined()
    expect(getFluxDeployedRelease(helm([...entries, { version: 6, chartVersion: '1.1.0', status: 'deployed', lastDeployed: '2026-10-26T12:00:00Z' }]))).toMatchObject({ version: 6, chartVersion: '1.1.0', lastDeployed: '2026-10-26T12:00:00Z' })
  })

  it('keeps the successfully applied chart version from older Flux without inventing a date', () => {
    const older = { apiVersion: 'helm.toolkit.fluxcd.io/v2beta1', kind: 'HelmRelease', status: { lastAppliedRevision: '1.2.0', lastAttemptedRevision: '1.3.0' } }
    expect(getFluxHelmChartVersion(older)).toBe('1.2.0')
    expect(getFluxDeployedRelease(older)).toBeUndefined()
  })

  it('selects a real status transition without treating it as an apply or manual request time', () => {
    const resource = { ...kustomization(), status: { conditions: [
      { type: 'Ready', lastTransitionTime: '2026-10-30T12:00:00.123456788Z' },
      { type: 'Reconciling', lastTransitionTime: '2026-10-30T14:00:00.123456789+02:00' },
      { type: 'Stalled', lastTransitionTime: 'invalid' },
    ] } }
    expect(getFluxLastStatusChange(resource)).toBe('2026-10-30T14:00:00.123456789+02:00')
    expect(getFluxLastApplyAttempt(resource)).toBeUndefined()
    expect(getFluxLastStatusChange({ ...resource, apiVersion: 'example.io/v1' })).toBeUndefined()
    expect(getFluxLastStatusChange({ ...helm([]), status: { conditions: [{ lastTransitionTime: '0001-01-01T00:00:00Z' }] } })).toBeUndefined()
  })

  it('preserves arbitrary request tokens and validates attempt dates independently of outcome', () => {
    expect(getFluxHandledRequestToken({ lastHandledReconcileAt: 'request-abc' })).toBe('request-abc')
    expect(getFluxHandledRequestToken({ lastHandledReconcileAt: '2026-10-07T09:00:00Z' })).toBe('2026-10-07T09:00:00Z')
    expect(getFluxHandledRequestToken({ lastHandledReconcileAt: 123 })).toBeUndefined()
    expect(getFluxHandledRequestToken({ lastHandledReconcileAt: '' })).toBeUndefined()
    expect(getFluxReleaseAttemptTime({ status: 'failed', lastDeployed: '2026-10-30T20:59:00Z' })).toBe('2026-10-30T20:59:00Z')
    expect(getFluxReleaseAttemptTime({ lastDeployed: '2026-02-30T00:00:00Z' })).toBeUndefined()
    expect(getFluxReleaseAttemptTime({ lastDeployed: '0001-01-01T00:00:00Z' })).toBeUndefined()
  })
})
