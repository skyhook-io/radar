import { describe, it, expect } from 'vitest'
import { Database, Network, Server, ShieldCheck, Puzzle } from 'lucide-react'
import { getResourceIcon, getTopologyIcon, DEFAULT_RESOURCE_ICON } from './resource-icons'

describe('getResourceIcon', () => {
  it('disambiguates colliding kinds by API group', () => {
    // `Cluster` is shipped by CNPG, Cluster API, KubeBlocks and others. Without
    // the group there is nothing to pick on, so the browser passes it.
    expect(getResourceIcon('Cluster', 'postgresql.cnpg.io')).toBe(Database)
    expect(getResourceIcon('Cluster', 'cluster.x-k8s.io')).toBe(Server)
  })

  it('falls back to the default for an unmapped group', () => {
    // A third operator's `clusters` CRD must not inherit a Postgres icon.
    expect(getResourceIcon('Cluster', 'apps.kubeblocks.io')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('Backup', 'kubevirt.io')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('HostEndpoint', 'networking.example.io')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('IPPool', 'networking.example.io')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('IPPool', 'extension.projectcalico.org')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('Tier', 'extension.projectcalico.org')).toBe(DEFAULT_RESOURCE_ICON)
  })

  it('resolves Calico IPPool only for Calico API groups', () => {
    expect(getResourceIcon('IPPool', 'crd.projectcalico.org')).toBe(Network)
    expect(getResourceIcon('IPPool', 'projectcalico.org')).toBe(Network)
    expect(getResourceIcon('IPPool')).toBe(DEFAULT_RESOURCE_ICON)
  })

  it('resolves Calico HostEndpoint only for Calico API groups', () => {
    expect(getResourceIcon('HostEndpoint', 'crd.projectcalico.org')).toBe(Network)
    expect(getResourceIcon('HostEndpoint', 'projectcalico.org')).toBe(Network)
    expect(getResourceIcon('HostEndpoint')).toBe(DEFAULT_RESOURCE_ICON)
  })

  it('resolves Calico Tier only for Calico API groups', () => {
    expect(getResourceIcon('Tier', 'crd.projectcalico.org')).toBe(Network)
    expect(getResourceIcon('Tier', 'projectcalico.org')).toBe(Network)
    expect(getResourceIcon('Tier')).toBe(DEFAULT_RESOURCE_ICON)
  })

  it('disambiguates NetworkPolicy and Calico policy kinds by API group', () => {
    expect(getResourceIcon('NetworkPolicy', 'networking.k8s.io')).toBe(ShieldCheck)
    expect(getResourceIcon('NetworkPolicy', 'projectcalico.org')).toBe(ShieldCheck)
    expect(getResourceIcon('NetworkPolicy', 'crd.projectcalico.org')).toBe(ShieldCheck)
    expect(getResourceIcon('NetworkPolicy', 'networking.example.io')).toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('GlobalNetworkPolicy', 'projectcalico.org')).toBe(ShieldCheck)
    expect(getResourceIcon('StagedNetworkPolicy', 'crd.projectcalico.org')).toBe(ShieldCheck)
    expect(getResourceIcon('StagedGlobalNetworkPolicy', 'projectcalico.org')).toBe(ShieldCheck)
    expect(getResourceIcon('StagedKubernetesNetworkPolicy', 'crd.projectcalico.org')).toBe(ShieldCheck)
    expect(getResourceIcon('GlobalNetworkPolicy', 'networking.example.io')).toBe(DEFAULT_RESOURCE_ICON)
  })

  it('uses ShieldCheck for all Calico policy topology nodes', () => {
    for (const kind of [
      'CalicoNetworkPolicy',
      'CalicoGlobalNetworkPolicy',
      'CalicoStagedNetworkPolicy',
      'CalicoStagedGlobalNetworkPolicy',
      'CalicoStagedKubernetesNetworkPolicy',
    ]) {
      expect(getTopologyIcon(kind)).toBe(ShieldCheck)
      expect(getResourceIcon(kind)).toBe(ShieldCheck)
    }
  })

  it('ignores the group for kinds that do not collide', () => {
    expect(getResourceIcon('Pod')).not.toBe(Puzzle)
    expect(getResourceIcon('Pod', 'postgresql.cnpg.io')).toBe(getResourceIcon('Pod'))
  })

  it('resolves unambiguous CNPG kinds without a group', () => {
    // Pooler is CNPG-only, so it keys directly and works everywhere.
    expect(getResourceIcon('Pooler')).not.toBe(DEFAULT_RESOURCE_ICON)
    expect(getResourceIcon('Pooler')).toBe(getResourceIcon('Pooler', 'postgresql.cnpg.io'))
  })

  it('returns the default for an entirely unknown kind', () => {
    expect(getResourceIcon('Widget')).toBe(DEFAULT_RESOURCE_ICON)
  })
})
