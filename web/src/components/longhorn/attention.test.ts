import { describe, expect, it } from 'vitest'
import { longhornAttention, longhornAttentionCount } from './attention'
import { hoursAgo, volume, workspace } from './longhorn.fixtures'

const partial = (data: ReturnType<typeof workspace>) => ({ ...data, coverage: { ...data.coverage, volumes: { state: 'partial' as const } }, counts: data.counts && { ...data.counts, complete: false } })

describe('Longhorn Needs attention', () => {
  it('lists faulted volumes before degraded ones, the longest degraded first, and leaves detached out', () => {
    const data = workspace({
      volumes: [
        volume({ name: 'recent', health: 'degraded', healthyReplicas: 2, lastDegradedAt: hoursAgo(1) }),
        volume({ name: 'idle', health: 'detached' }),
        volume({ name: 'old', health: 'degraded', healthyReplicas: 1, lastDegradedAt: hoursAgo(5) }),
        volume({ name: 'broken', health: 'faulted', healthyReplicas: 0 }),
      ],
    })
    const rows = longhornAttention(data)
    expect(rows.map((r) => r.subject.name)).toEqual(['broken', 'old', 'recent'])
    expect(rows.map((r) => r.tone)).toEqual(['unhealthy', 'degraded', 'degraded'])
    expect(rows[1].title).toBe('Degraded for 5h')
    expect(rows[1].detail).toContain('1 of 3 replicas healthy')
    expect(rows[1].related?.refs[0]).toMatchObject({ kind: 'PersistentVolumeClaim', namespace: 'shop', name: 'data' })
  })

  it('names a claim the caller can’t read without naming it, and says when the use is unknown', () => {
    const [withheld, unknown] = longhornAttention(workspace({
      volumes: [
        volume({ name: 'a', health: 'faulted', use: { state: 'bound', persistentVolume: 'a', claimWithheld: true } }),
        volume({ name: 'b', health: 'degraded', healthyReplicas: null, healthyReplicasUnknown: 'engineUnread', use: { state: 'unknown' } }),
      ],
    }))
    expect(withheld.detail).toContain('bound to a claim you can’t read')
    expect(withheld.related).toBeUndefined()
    expect(unknown.detail).toContain('Radar can’t tell which claim uses it')
    expect(unknown.detail).not.toContain('replicas healthy')
    expect(unknown.title).toBe('Degraded; Longhorn didn’t record since when')
  })
})

describe('Longhorn sidebar count', () => {
  it('counts faulted and degraded volumes once each', () => {
    const data = workspace({ volumes: [volume({ name: 'a', health: 'faulted' }), volume({ name: 'b', health: 'degraded' }), volume({ name: 'c', health: 'detached' })] })
    expect(longhornAttentionCount(data)).toMatchObject({ count: 2, lowerBound: false })
  })

  it('is a lower bound over a partial read, and unknown rather than zero when it found none', () => {
    expect(longhornAttentionCount(partial(workspace({ volumes: [volume({ health: 'faulted' })] })))).toMatchObject({ count: 1, lowerBound: true })
    expect(longhornAttentionCount(partial(workspace()))).toMatchObject({ count: null, lowerBound: true })
    expect(longhornAttentionCount(workspace({ volumes: [], counts: null, coverage: { volumes: { state: 'denied' } } }))).toMatchObject({ count: null })
  })
})
