import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { isKueueWorkloadEligibleForPendingPosition } from './resource-utils-kueue'

const vectors = JSON.parse(readFileSync(fileURLToPath(new URL('../../../../../pkg/schedulinginsight/testdata/kueue_awaiting_admission.json', import.meta.url)), 'utf8'))

describe('isKueueWorkloadEligibleForPendingPosition', () => {
  it('has cases', () => expect(vectors.cases.length).toBeGreaterThan(0))
  for (const testCase of vectors.cases) {
    it(`matches Go: ${testCase.name}`, () => {
      expect(isKueueWorkloadEligibleForPendingPosition(testCase.workload)).toBe(testCase.awaiting)
    })
  }
})
