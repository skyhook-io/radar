import { describe, expect, it } from 'vitest'
import {
  canApplyYamlPreview,
  reviewedResourceVersionsForPreview,
  type YamlPreviewResult,
} from './YamlReview'

const accepted: YamlPreviewResult = { index: 0, status: 'accepted' }

describe('canApplyYamlPreview', () => {
  it('allows fully accepted previews', () => {
    expect(canApplyYamlPreview([accepted], false)).toBe(true)
  })

  it('blocks authoritative rejections even after acknowledgement', () => {
    expect(canApplyYamlPreview([accepted, { index: 1, status: 'rejected' }], true)).toBe(false)
  })

  it('requires acknowledgement for dependency or dry-run unavailable documents', () => {
    const documents = [accepted, { index: 1, status: 'unavailable' as const }]
    expect(canApplyYamlPreview(documents, false)).toBe(false)
    expect(canApplyYamlPreview(documents, true)).toBe(true)
  })

  it('blocks apply until the review diff is visible', () => {
    expect(canApplyYamlPreview([accepted], false, false)).toBe(false)
  })
})

describe('reviewedResourceVersionsForPreview', () => {
  it('guards reviewed creates by recording expected absence', () => {
    expect(
      reviewedResourceVersionsForPreview([
        { index: 0, status: 'accepted', action: 'create' },
        {
          index: 1,
          status: 'accepted',
          action: 'update',
          reviewedResourceVersion: '42',
        },
        { index: 2, status: 'unavailable', action: 'unknown' },
      ]),
    ).toEqual({ 0: '', 1: '42' })
  })
})
