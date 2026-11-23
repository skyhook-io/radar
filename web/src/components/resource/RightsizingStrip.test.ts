import { describe, expect, it } from 'vitest'
import {
  RIGHTSIZING_METHODOLOGY,
  RIGHTSIZING_DOCS_URL,
  RIGHTSIZING_SUMMARY,
} from './RightsizingStrip'

describe('Prometheus rightsizing', () => {
  it('sets the workload-level scope and methodology without promising savings or changes', () => {
    expect(RIGHTSIZING_SUMMARY).toContain('this workload')
    expect(RIGHTSIZING_SUMMARY).toContain('not a savings estimate or automatic change')
    expect(RIGHTSIZING_METHODOLOGY).toContain('CPU P95 and memory maximum')
    expect(RIGHTSIZING_METHODOLOGY).toContain('Reductions are staged')
    expect(RIGHTSIZING_METHODOLOGY).toContain('Radar does not change requests')
    expect(RIGHTSIZING_DOCS_URL).toContain('/features/rightsizing')
  })
})
