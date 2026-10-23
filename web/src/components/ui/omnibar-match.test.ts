import { describe, expect, it } from 'vitest'
import { contentMatchLabel, partialSearchNotice } from './omnibar-match'

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

describe('partialSearchNotice', () => {
  it('flags partly searched findings and unchecked namespaces', () => {
    expect(partialSearchNotice({ incomplete_findings: 'time_limit' })).toMatch(/not fully searched/)
    expect(partialSearchNotice({ access_incomplete: true })).toMatch(/not fully searched/)
  })
  it('is null for a whole result or an older Radar', () => {
    expect(partialSearchNotice({})).toBeNull()
    expect(partialSearchNotice(undefined)).toBeNull()
  })
})
