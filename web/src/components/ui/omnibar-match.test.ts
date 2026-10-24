import { describe, expect, it } from 'vitest'
import { contentMatchLabel, partialSearchNotices } from './omnibar-match'

const site = (s: string) => ({ token: 't', site: s, score: 12 })

describe('contentMatchLabel', () => {
  it('names a match inside Trivy findings', () => {
    expect(contentMatchLabel([site('content:report.vulnerabilities[3]')])).toBe('in findings')
  })
  it('keeps "in spec" for other content', () => {
    expect(contentMatchLabel([site('content:spec.template.spec.containers[0].env[1].value')])).toBe('in spec')
    expect(contentMatchLabel([site('content:report.checks[0]'), site('content:spec.x')])).toBe('in spec')
  })
  it('is empty when any token matched identity', () => {
    expect(contentMatchLabel([site('content:report.vulnerabilities[0]'), site('name')])).toBe('')
    expect(contentMatchLabel([])).toBe('')
    expect(contentMatchLabel(undefined)).toBe('')
  })
})

describe('partialSearchNotices', () => {
  it('says why each part of the search is missing', () => {
    expect(partialSearchNotices({ access_incomplete: true })).toEqual([
      "Couldn't confirm your access to scan reports in some namespaces, so they were left out. Search again to check more.",
    ])
    expect(partialSearchNotices({ incomplete_findings: 'time_limit' })).toEqual([
      "Search ran out of time before checking every scan report's findings. Narrow with ns: to cover the rest.",
    ])
  })
  it('shows both causes when both happened', () => {
    expect(partialSearchNotices({ access_incomplete: true, incomplete_findings: 'time_limit' })).toHaveLength(2)
  })
  it('is empty for a whole result or an older Radar', () => {
    expect(partialSearchNotices({})).toEqual([])
    expect(partialSearchNotices(undefined)).toEqual([])
  })
})
