import { describe, expect, it } from 'vitest'
import { ownerLookup, type VulnerabilityWorkloadOwner } from '../../api/vulnerabilities'
import { managerLabel, ownerTip, shortRepo } from './owner'

const helm: VulnerabilityWorkloadOwner = {
  kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web',
  manager: { tool: 'helm', kind: 'HelmRelease', namespace: 'shop', name: 'web', chart: 'web-1.2.0' },
  team: 'payments', teamFrom: 'workload',
}

describe('owner', () => {
  it('finds a workload by its identity, whatever its container', () => {
    const find = ownerLookup([helm])
    expect(find({ kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web' })).toBe(helm)
    expect(find({ kind: 'Deployment', group: 'apps', namespace: 'other', name: 'web' })).toBeUndefined()
  })

  it('shortens repository addresses for a cell', () => {
    expect(shortRepo('https://github.com/acme/deploy.git')).toBe('github.com/acme/deploy')
    expect(shortRepo('git@github.com:acme/deploy.git')).toBe('github.com/acme/deploy')
    expect(shortRepo('ssh://git@github.com/acme/fleet')).toBe('git@github.com/acme/fleet')
  })

  it('says what Radar knows and nothing when it knows nothing', () => {
    expect(ownerTip(helm)).toBe('Deployed by Helm release shop/web, chart web-1.2.0. Team payments.')
    expect(ownerTip({ ...helm, manager: { tool: 'argocd', kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'shop', sources: [{ repo: 'https://github.com/acme/deploy.git', path: 'apps' }] }, team: undefined }))
      .toBe('Deployed by Argo CD Application argocd/shop, from github.com/acme/deploy (apps).')
    expect(ownerTip({ ...helm, team: undefined, manager: { tool: 'argocd', kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'shop', shared: true, sources: [{ repo: 'https://charts.example.com' }, { repo: 'https://github.com/acme/deploy.git', path: 'apps' }] } }))
      .toBe('Deployed by Argo CD Application argocd/shop, from charts.example.com, github.com/acme/deploy (apps). More than one Argo CD or Flux object manages it.')
    expect(ownerTip({ ...helm, manager: undefined, team: undefined, managerUnknown: true })).toBe("Radar couldn't tell what deploys it.")
    expect(ownerTip(undefined)).toBeUndefined()
    expect(managerLabel({ tool: 'fluxcd', kind: 'Kustomization', namespace: 'f', name: 'x' })).toBe('Flux Kustomization')
  })

  it('never uses an em dash in its copy', () => {
    expect(ownerTip(helm)).not.toContain('—')
  })
})
