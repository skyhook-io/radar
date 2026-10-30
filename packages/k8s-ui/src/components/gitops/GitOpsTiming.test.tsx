import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GitOpsTableView, normalizeFluxHelmRelease, normalizeFluxKustomization } from './GitOpsTableView'

describe('GitOps landing timing', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-30T21:00:00Z')) })
  afterEach(() => vi.useRealTimers())
  it('uses recorded apply and deployed history instead of condition changes or requested constraints', () => {
    const kustom = normalizeFluxKustomization({ apiVersion: 'kustomize.toolkit.fluxcd.io/v1', kind: 'Kustomization', metadata: { name: 'checkout', namespace: 'flux-system' }, status: { conditions: [{ type: 'Ready', status: 'True', lastTransitionTime: '2026-10-30T20:59:00Z' }], history: [{ lastReconciled: '2026-10-30T20:55:00Z' }] } })
    const helm = normalizeFluxHelmRelease({ apiVersion: 'helm.toolkit.fluxcd.io/v2', kind: 'HelmRelease', metadata: { name: 'payments', namespace: 'flux-system' }, spec: { chart: { spec: { version: '>=1.2.0 <2.0.0' } } }, status: { history: [
      { version: 5, chartVersion: '1.3.0', status: 'failed', lastDeployed: '2026-10-30T20:59:00Z' },
      { version: 4, chartVersion: '1.2.0', status: 'deployed', lastDeployed: '2026-10-27T21:00:00Z' },
    ] } })
    expect(kustom.lastSync).toBe('2026-10-30T20:55:00Z')
    expect(kustom.lastSyncLabel).toBe('Apply attempt')
    expect(helm.lastSync).toBe('2026-10-27T21:00:00Z')
    expect(helm.targetRevision).toBe('1.2.0')
    const html = renderToString(<GitOpsTableView rows={[kustom, helm]} counts={{ 'kustomize.toolkit.fluxcd.io/Kustomization': 1, 'helm.toolkit.fluxcd.io/HelmRelease': 1 }} onRowClick={() => {}} />)
    expect(html).toContain('Activity')
    expect(html).toContain('Apply attempt')
    expect(html).toContain('Deployed')
    expect(html).toContain('3d ago')
    expect(html).toContain('5m ago')
    expect(html).not.toContain('Last Sync')
  })

  it('does not turn conditions into an apply date on an older controller', () => {
    const row = normalizeFluxKustomization({ apiVersion: 'kustomize.toolkit.fluxcd.io/v1', kind: 'Kustomization', metadata: { name: 'old' }, status: { conditions: [{ type: 'Ready', status: 'True', lastTransitionTime: '2026-10-30T20:59:00Z' }] } })
    expect(row.lastSync).toBe('')
  })

  it('does not pair an unknown deployed chart date with a legacy or attempted revision', () => {
    const row = normalizeFluxHelmRelease({ apiVersion: 'helm.toolkit.fluxcd.io/v2', kind: 'HelmRelease', metadata: { name: 'partial' }, status: {
      lastAppliedRevision: '1.1.0', lastAttemptedRevision: '1.3.0',
      history: [{ version: 4, status: 'deployed', lastDeployed: '2026-10-27T21:00:00Z' }],
    } })
    expect(row.targetRevision).toBe('1.1.0')
    expect(row.lastSync).toBe('')
  })
})
