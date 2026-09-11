import { afterEach, describe, expect, it } from 'vitest'
import {
  initNavigationMap,
  resetNavigationMap,
} from '@skyhook-io/k8s-ui/utils/navigation'
import { relatedResourcePath, resourcePath } from './navigation'

afterEach(resetNavigationMap)

describe('resource navigation paths', () => {
  it('routes the native PodGroup by its scheduling API group', () => {
    initNavigationMap([
      {
        group: 'scheduling.k8s.io',
        version: 'v1beta1',
        kind: 'PodGroup',
        name: 'podgroups',
        namespaced: true,
        isCrd: false,
        verbs: ['get', 'list'],
      },
    ])

    expect(
      resourcePath({
        kind: 'PodGroup',
        group: 'scheduling.k8s.io',
        namespace: 'default',
        name: 'batch',
      }),
    ).toBe(
      '/resources/podgroups?resource=default%2Fbatch&apiGroup=scheduling.k8s.io',
    )
  })

  it('preserves Radar virtual PodGroup navigation without an API group', () => {
    expect(
      relatedResourcePath({
        kind: 'PodGroup',
        namespace: 'default',
        name: 'batch',
      }),
    ).toBe('/workload/pods/default/batch')
  })
})
