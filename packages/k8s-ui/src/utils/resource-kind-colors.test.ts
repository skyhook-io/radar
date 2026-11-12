import { describe, expect, it } from 'vitest'
import { getResourceKindIconColor } from './resource-kind-colors'

describe('resource icon group identity', () => {
  it.each(['Service', 'Configuration', 'Revision', 'Route'])('matches Knative %s to its Topology identity', kind => {
    expect(getResourceKindIconColor(kind, 'serving.knative.dev')).toBe(getResourceKindIconColor(`Knative${kind}`))
    expect(getResourceKindIconColor(kind, 'example.io')).toBe('text-gray-400')
  })

  it('keeps Cluster API separate from other Cluster kinds', () => {
    expect(getResourceKindIconColor('Cluster', 'cluster.x-k8s.io')).toBe(getResourceKindIconColor('CAPICluster'))
    expect(getResourceKindIconColor('Cluster', 'postgresql.cnpg.io')).toBe('text-gray-400')
    expect(getResourceKindIconColor('Cluster', 'example.io')).toBe('text-gray-400')
  })

  it.each(['NetworkPolicy', 'GlobalNetworkPolicy', 'StagedNetworkPolicy', 'StagedGlobalNetworkPolicy', 'StagedKubernetesNetworkPolicy'])('matches both Calico groups for %s', kind => {
    for (const group of ['projectcalico.org', 'crd.projectcalico.org']) {
      expect(getResourceKindIconColor(kind, group)).toBe(getResourceKindIconColor(`Calico${kind}`))
    }
    expect(getResourceKindIconColor(kind, 'example.io')).toBe('text-gray-400')
  })

  it('does not borrow core colors for unrelated CRDs', () => {
    expect(getResourceKindIconColor('Service', '')).toBe('text-blue-400')
    expect(getResourceKindIconColor('Deployment', 'apps')).toBe('text-emerald-400')
    expect(getResourceKindIconColor('Deployment', 'example.io')).toBe('text-gray-400')
    expect(getResourceKindIconColor('Widget', 'example.io')).toBe('text-gray-400')
  })

  it('matches supported scheduler PodGroups without coloring unrelated homonyms', () => {
    for (const group of ['scheduling.volcano.sh', 'scheduling.run.ai']) {
      expect(getResourceKindIconColor('PodGroup', group)).toBe(getResourceKindIconColor('PodGroup'))
    }
    expect(getResourceKindIconColor('PodGroup', 'example.io')).toBe('text-gray-400')
  })
})
