import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { getHPATableState, hpaStateLabel } from './resource-utils-hpa'
import type { HPADiagnosisState } from '../../types'

interface FixtureCase {
  name: string
  hpa: unknown
  expectedTableState: HPADiagnosisState
}

describe('getHPATableState', () => {
  for (const tc of loadCases()) {
    it(`classifies ${tc.name}`, () => {
      expect(getHPATableState(tc.hpa)).toBe(tc.expectedTableState)
    })
  }
})

describe('hpaStateLabel', () => {
  it('keeps max-bound wording terse for table cells', () => {
    expect(hpaStateLabel('limited_max')).toBe('Maxed')
  })
})

function loadCases(): FixtureCase[] {
  const path = resolve(process.cwd(), '../../testdata/hpa-diagnosis/cases.json')
  return JSON.parse(readFileSync(path, 'utf8')) as FixtureCase[]
}
