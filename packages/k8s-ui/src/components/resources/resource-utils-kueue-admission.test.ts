import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { kueueAdmissionStateLabel, kueueAdmissionStatePresentation } from './resource-utils-kueue'

const vector = JSON.parse(readFileSync(fileURLToPath(new URL('../../../../../pkg/schedulinginsight/testdata/kueue_admission_codes.json', import.meta.url)), 'utf8'))

describe('Kueue admission-state presentation', () => {
  it('has a label for every code the server can send', () => {
    for (const code of vector.codes as string[]) expect(kueueAdmissionStateLabel(code), code).not.toBe(code)
  })

  it('has a tone for every attention class and stays neutral for stale or unknown classes', () => {
    for (const attention of vector.attention as string[]) expect(kueueAdmissionStatePresentation({ state: 'insufficient_quota', attention }).severity).toBeTruthy()
    expect(kueueAdmissionStatePresentation({ state: 'local_queue_missing', attention: 'action_needed' }).severity).toBe('alert')
    expect(kueueAdmissionStatePresentation({ state: 'local_queue_missing', attention: 'action_needed', stale: true }).severity).toBe('neutral')
    expect(kueueAdmissionStatePresentation({ state: 'a_future_code', attention: 'a_future_class' })).toEqual({ label: 'a_future_code', severity: 'neutral' })
  })
})
