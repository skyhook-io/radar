import { describe, expect, it } from 'vitest'
import { helmControllerOwner, k3sManagedText } from './k3sOwner'
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

describe('helm controller owner', () => {
  it('lets Flux win over k3s', () => {
    expect(helmControllerOwner({ managedByFluxHelmRelease: 'flux-system/x', managedByK3sHelmChart: 'kube-system/x' })).toEqual({ tool: 'flux', ref: 'flux-system/x' })
    expect(helmControllerOwner({ managedByK3sHelmChart: 'kube-system/traefik', k3sHelmChartConfig: 'kube-system/traefik' })).toEqual({ tool: 'k3s', chart: 'kube-system/traefik', config: 'kube-system/traefik' })
    expect(helmControllerOwner({})).toBeNull()
  })

  it('names the HelmChartConfig or says to create one', () => {
    expect(k3sManagedText({ chart: 'kube-system/traefik', config: 'kube-system/traefik' })).toBe('Managed by k3s. HelmChart kube-system/traefik reinstalls this release from its own chart, so an upgrade or rollback here is reverted. Change values in HelmChartConfig kube-system/traefik.')
    expect(k3sManagedText({ chart: 'kube-system/traefik' })).toBe('Managed by k3s. HelmChart kube-system/traefik reinstalls this release from its own chart, so an upgrade or rollback here is reverted. Create a HelmChartConfig kube-system/traefik to change its values.')
  })
})
