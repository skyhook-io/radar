import { describe, expect, it } from 'vitest'
import type { ResourceRef } from '../types/core'
import { dedupeResourceRefs, omitResourceRefs, resourceRefKey } from './resource-refs'

describe('resource reference identity', () => {
  const core: ResourceRef = { kind: 'Service', namespace: 'team', name: 'app' }
  const knative: ResourceRef = { ...core, group: 'serving.knative.dev' }

  it('deduplicates core aliases while preserving an identically named custom resource', () => {
    const refs = [core, { ...core, group: '' }, knative]
    expect(dedupeResourceRefs(refs)).toEqual([core, knative])
    expect(refs).toHaveLength(3)
    expect(resourceRefKey(core)).not.toBe(resourceRefKey(knative))
  })

  it('omits only the exact mirrored identity without mutating either list', () => {
    const refs = [core, knative]
    const excluded = [{ ...core, group: '' }]
    expect(omitResourceRefs(refs, excluded)).toEqual([knative])
    expect(refs).toEqual([core, knative])
    expect(excluded).toHaveLength(1)
  })
})
