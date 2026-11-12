import { describe, expect, it } from 'vitest'
import { controllerManagedText, otherController, releaseControllerOf } from './releaseController'
import { DEFAULT_SHOW_EFFECTIVE_VALUES, effectiveHelmStorageNamespace, isUpgradeSourceIssueActionable, upgradePickerVersions } from './HelmReleaseDrawer'

describe('Helm values view', () => {
  it('opens on user overrides', () => {
    expect(DEFAULT_SHOW_EFFECTIVE_VALUES).toBe(false)
  })
})

describe('isUpgradeSourceIssueActionable', () => {
  it('allows explicit association for ambiguous and unavailable sources', () => {
    expect(isUpgradeSourceIssueActionable('ambiguous_repository')).toBe(true)
    expect(isUpgradeSourceIssueActionable('ambiguous_source')).toBe(true)
    expect(isUpgradeSourceIssueActionable('source_unavailable')).toBe(true)
  })

  it('lets OCI registration help untracked and repo-index states', () => {
    expect(isUpgradeSourceIssueActionable('untracked')).toBe(true)
    expect(isUpgradeSourceIssueActionable('repo_index_error')).toBe(true)
  })

  it('does not treat a missing reason code as actionable', () => {
    expect(isUpgradeSourceIssueActionable(undefined)).toBe(false)
  })
})

describe('effectiveHelmStorageNamespace', () => {
  it('falls back to the release namespace when storageNamespace is empty', () => {
    expect(effectiveHelmStorageNamespace({ namespace: 'production', name: 'release', storageNamespace: '' })).toBe('production')
  })

  it('uses an explicit storage namespace', () => {
    expect(effectiveHelmStorageNamespace({ namespace: 'production', name: 'release', storageNamespace: 'helm-storage' })).toBe('helm-storage')
  })
})

describe('upgradePickerVersions', () => {
  it('lists the repository versions when the installed one is still published', () => {
    expect(upgradePickerVersions(['1.5.0', '1.4.0'], '1.4.0')).toEqual([
      { version: '1.5.0', selectable: true },
      { version: '1.4.0', selectable: true },
    ])
  })

  it('shows a dropped installed version in order but not selectable', () => {
    expect(upgradePickerVersions(['1.6.0', '1.5.0', '1.3.0'], '1.4.0')).toEqual([
      { version: '1.6.0', selectable: true },
      { version: '1.5.0', selectable: true },
      { version: '1.4.0', selectable: false },
      { version: '1.3.0', selectable: true },
    ])
  })

  it('offers a picker even when only one newer version is left', () => {
    expect(upgradePickerVersions(['1.5.0'], '1.4.0')).toEqual([
      { version: '1.5.0', selectable: true },
      { version: '1.4.0', selectable: false },
    ])
  })

  it('puts a dropped version older than every listed one at the end', () => {
    expect(upgradePickerVersions(['2.1.0', '2.0.0'], '1.0.0')?.at(-1)).toEqual({ version: '1.0.0', selectable: false })
  })

  it('has nothing to pick with a single published version that is the installed one', () => {
    expect(upgradePickerVersions(['1.5.0'], '1.5.0')).toBeNull()
    expect(upgradePickerVersions(undefined, '1.5.0')).toBeNull()
  })
})

describe('release controller', () => {
  const k3s = (exists: boolean) => ({ tool: 'k3s', kind: 'HelmChart', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', valuesRef: { kind: 'HelmChartConfig', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik', exists } })

  it('reads the generic field, and the Flux field from an older Radar', () => {
    expect(releaseControllerOf({ controller: k3s(true) })).toEqual(k3s(true))
    expect(releaseControllerOf({ managedByFluxHelmRelease: 'flux-system/app' })).toEqual({ tool: 'flux', kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', namespace: 'flux-system', name: 'app' })
    expect(releaseControllerOf({})).toBeUndefined()
  })

  it('words any controller, naming where values change', () => {
    expect(controllerManagedText(k3s(true))).toBe('Managed by k3s. HelmChart kube-system/traefik reinstalls this release from its own definition, so an upgrade or rollback here is reverted. Change values in HelmChartConfig kube-system/traefik.')
    expect(controllerManagedText(k3s(false))).toBe('Managed by k3s. HelmChart kube-system/traefik reinstalls this release from its own definition, so an upgrade or rollback here is reverted. Create a HelmChartConfig kube-system/traefik to change its values.')
    expect(controllerManagedText(k3s(false), { observed: true })).toContain('(create it if it does not exist)')
    const fleet = { tool: 'fleet', kind: 'Bundle', group: 'fleet.cattle.io', namespace: 'fleet-local', name: 'app' }
    expect(controllerManagedText(fleet)).toBe('Managed by fleet. Bundle fleet-local/app reinstalls this release from its own definition, so an upgrade or rollback here is reverted. Change it through that Bundle.')
  })
})

describe('controllers other than Flux', () => {
  it('are the ones that hide upgrade hints and get the generic note; Flux keeps its own routing', () => {
    expect(otherController({ controller: { tool: 'k3s', kind: 'HelmChart', group: 'helm.cattle.io', namespace: 'kube-system', name: 'traefik' } })?.tool).toBe('k3s')
    expect(otherController({ managedByFluxHelmRelease: 'flux-system/x' })).toBeUndefined()
    expect(otherController({})).toBeUndefined()
  })
})
