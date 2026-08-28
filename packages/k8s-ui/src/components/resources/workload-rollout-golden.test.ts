import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { getWorkloadRolloutActivity, rolloutMayAdvanceAutomatically, type WorkloadRolloutPhase } from '../../utils/workload-rollout'

interface GoldenVector {
  name: string
  kind: string
  phase: WorkloadRolloutPhase
  active: boolean
  manual: boolean
  label: string
  detail: string
  desired: number
  updated: number
  ready: number
  available: number
  object: any
}

const here = dirname(fileURLToPath(import.meta.url))
const fixturePath = resolve(here, '../../../../../pkg/health/testdata/workload_rollout_vectors.json')
const vectors: GoldenVector[] = JSON.parse(readFileSync(fixturePath, 'utf8')).vectors

describe('workload rollout golden vectors', () => {
  it('loads the same fixture as pkg/health', () => {
    expect(vectors.length).toBeGreaterThan(0)
  })

  for (const vector of vectors) {
    it(`${vector.kind}: ${vector.name}`, () => {
      const result = getWorkloadRolloutActivity(vector.object, vector.kind)
      expect(result).toEqual({
        phase: vector.phase,
        active: vector.active,
        manual: vector.manual,
        label: vector.label,
        detail: vector.detail,
        desired: vector.desired,
        updated: vector.updated,
        ready: vector.ready,
        available: vector.available,
      })
    })
  }

  it('polls only states that can advance without operator action', () => {
    const base = { active: true, manual: false, label: 'Rolling out', desired: 1, updated: 0, ready: 1, available: 1 }
    expect(rolloutMayAdvanceAutomatically({ ...base, phase: 'progressing' })).toBe(true)
    expect(rolloutMayAdvanceAutomatically({ ...base, phase: 'waiting', label: 'Waiting for new revision' })).toBe(true)
    expect(rolloutMayAdvanceAutomatically({ ...base, phase: 'waiting', manual: true, label: 'Waiting for Pod restart' })).toBe(false)
    expect(rolloutMayAdvanceAutomatically({ ...base, phase: 'paused', label: 'Rollout paused' })).toBe(false)
  })
})
