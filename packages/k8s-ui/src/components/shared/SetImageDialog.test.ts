import { describe, expect, it } from 'vitest'

import type { WorkloadContainerImage } from '../../types/core'
import {
  canSubmitImageUpdates,
  changedImageUpdates,
  describeImageUpdateBehavior,
  reconcileRefreshedImageDrafts,
} from './SetImageDialog'

const containers: WorkloadContainerImage[] = [
  { type: 'container', name: 'app', image: 'repo/app:v1' },
  { type: 'container', name: 'sidecar', image: 'repo/sidecar:v1' },
  { type: 'initContainer', name: 'migrate', image: 'repo/migrate:v1' },
]

describe('changedImageUpdates', () => {
  it('submits only changed rows with their reviewed previous images', () => {
    expect(
      changedImageUpdates(containers, {
        ['container\u0000app']: 'repo/app:v2',
        ['container\u0000sidecar']: 'repo/sidecar:v1',
        ['initContainer\u0000migrate']: ' repo/migrate:v2 ',
      }),
    ).toEqual([
      {
        type: 'container',
        name: 'app',
        previousImage: 'repo/app:v1',
        image: 'repo/app:v2',
      },
      {
        type: 'initContainer',
        name: 'migrate',
        previousImage: 'repo/migrate:v1',
        image: 'repo/migrate:v2',
      },
    ])
  })

  it('does not turn an empty draft into a mutation', () => {
    expect(
      changedImageUpdates(containers.slice(0, 1), {
        ['container\u0000app']: '   ',
      }),
    ).toEqual([])
  })
})

describe('describeImageUpdateBehavior', () => {
  it('does not promise an immediate rollout for paused, OnDelete, or partitioned controllers', () => {
    expect(describeImageUpdateBehavior({ type: 'paused' })).toContain(
      'no rollout starts',
    )
    expect(describeImageUpdateBehavior({ type: 'onDelete' })).toContain(
      'until they are deleted',
    )
    expect(
      describeImageUpdateBehavior({ type: 'partitioned', partition: 3 }),
    ).toContain('ordinal 3 or higher')
  })

  it('describes Argo promotion behavior from the Rollout strategy', () => {
    expect(
      describeImageUpdateBehavior({ type: 'canary', gated: true }),
    ).toContain('configured steps')
    expect(
      describeImageUpdateBehavior({ type: 'canary', gated: false }),
    ).toContain('100% without a pause')
    expect(
      describeImageUpdateBehavior({ type: 'blueGreen', autoPromote: true }),
    ).toContain('automatically')
    expect(
      describeImageUpdateBehavior({ type: 'blueGreen', autoPromote: false }),
    ).toContain('Promotion remains a separate action')
  })
})

describe('reconcileRefreshedImageDrafts', () => {
  it('preserves proposed images while adopting untouched concurrent changes', () => {
    expect(
      reconcileRefreshedImageDrafts(
        containers,
        [
          { type: 'container', name: 'app', image: 'repo/app:other' },
          { type: 'container', name: 'sidecar', image: 'repo/sidecar:other' },
          { type: 'initContainer', name: 'migrate', image: 'repo/migrate:v1' },
        ],
        {
          ['container\u0000app']: 'repo/app:proposed',
          ['container\u0000sidecar']: 'repo/sidecar:v1',
          ['initContainer\u0000migrate']: 'repo/migrate:v1',
        },
      ),
    ).toEqual({
      drafts: {
        ['container\u0000app']: 'repo/app:proposed',
        ['container\u0000sidecar']: 'repo/sidecar:other',
        ['initContainer\u0000migrate']: 'repo/migrate:v1',
      },
      changedCurrentKeys: ['container\u0000app', 'container\u0000sidecar'],
    })
  })
})

describe('canSubmitImageUpdates', () => {
  const ready = {
    updateCount: 1,
    hasEmptyImage: false,
    managed: false,
    ownershipResolved: true,
    acknowledged: false,
    busy: false,
  }

  it('requires break-glass acknowledgement for managed resources', () => {
    expect(canSubmitImageUpdates({ ...ready, managed: true })).toBe(false)
    expect(
      canSubmitImageUpdates({ ...ready, managed: true, acknowledged: true }),
    ).toBe(true)
  })

  it('blocks empty images and concurrent submissions', () => {
    expect(canSubmitImageUpdates({ ...ready, hasEmptyImage: true })).toBe(false)
    expect(canSubmitImageUpdates({ ...ready, busy: true })).toBe(false)
    expect(canSubmitImageUpdates({ ...ready, loadFailed: true })).toBe(false)
    expect(canSubmitImageUpdates({ ...ready, ownershipResolved: false })).toBe(false)
  })
})
