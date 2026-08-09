import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { ApiError } from '../../api/client'
import { UPGRADE_IMPACT_DOCS_URL, UPGRADE_IMPACT_MIN_RADAR_VERSION, UpgradeReadinessError, groupFindings, incompleteUpgradeCheckCount, issueSpecificReferences, summaryMeta, upgradeEvaluationSummary } from './UpgradeReadinessView'

describe('UpgradeReadinessError', () => {
  it('maps an unmatched endpoint 404 to the v1.9 upgrade message', () => {
    const html = renderToStaticMarkup(UpgradeReadinessError({ error: new ApiError('Unknown error', 404) }))

    expect(html).toContain('Upgrade impact needs a newer Radar')
    expect(html).toContain(UPGRADE_IMPACT_MIN_RADAR_VERSION)
    expect(html).toContain('Upgrade the in-cluster Radar to enable it')
    expect(html).not.toContain('Unable to analyze upgrade impact')
  })

  it('keeps a real resource 404 on the generic error state', () => {
    const html = renderToStaticMarkup(UpgradeReadinessError({ error: new ApiError('Cluster not found', 404) }))

    expect(html).toContain('Unable to analyze upgrade impact')
    expect(html).toContain('Cluster not found')
    expect(html).not.toContain('needs a newer Radar')
  })

  it('keeps ordinary failures on the generic error state', () => {
    const html = renderToStaticMarkup(UpgradeReadinessError({ error: new ApiError('boom', 500) }))

    expect(html).toContain('Unable to analyze upgrade impact')
    expect(html).toContain('boom')
    expect(html).not.toContain('needs a newer Radar')
  })
})

describe('upgradeEvaluationSummary', () => {
  it('distinguishes applicable, incomplete, and not-applicable checks', () => {
    expect(upgradeEvaluationSummary(18, {
      blocked: 1,
      warnings: 0,
      reviews: 0,
      passed: 14,
      unknown: 1,
      notApplicable: 2,
      findings: 1,
    })).toBe('18 evaluated · 16 applicable · 1 with partial evidence · 2 not applicable')
  })

  it('does not imply incomplete coverage when every applicable check completed', () => {
    expect(upgradeEvaluationSummary(10, {
      blocked: 0,
      warnings: 0,
      reviews: 0,
      passed: 10,
      unknown: 0,
      notApplicable: 0,
      findings: 0,
    })).toBe('10 evaluated · 10 applicable · 0 not applicable')
  })

  it('links to the documented catalog rather than inventing a second catalog in the UI', () => {
    expect(UPGRADE_IMPACT_DOCS_URL).toContain('/features/upgrade-impact')
  })

  it('counts incomplete evidence even when a blocker takes status precedence', () => {
    expect(incompleteUpgradeCheckCount([
      { status: 'blocked', caveat: 'one PDB has stale status' },
      { status: 'unknown' },
      { status: 'passed' },
    ])).toBe(2)
  })
})

describe('groupFindings', () => {
  it('groups repeated remediation inside a check without merging distinct action levels', () => {
    const base = {
      ruleID: 'admission-webhook-readiness',
      title: 'Backend unavailable',
      resource: { kind: 'ValidatingWebhookConfiguration', namespace: '', name: 'policy' },
      evidence: { source: 'live', path: 'webhooks[0].clientConfig.service' },
      impact: 'Admission is unavailable.',
      remediation: 'Restore the backend.',
      references: [],
    }
    const groups = groupFindings([
      { ...base, level: 'blocker', evidence: { ...base.evidence, detail: 'default/a' } },
      { ...base, level: 'blocker', evidence: { ...base.evidence, detail: 'default/b' } },
      { ...base, level: 'warning', evidence: { ...base.evidence, detail: 'default/c' } },
    ])
    expect(groups.map((group) => group.total)).toEqual([2, 1])
  })

  it('counts findings even when multiple findings do not map one-to-one to resources', () => {
    const base = {
      ruleID: 'renamed-control-plane-metrics',
      title: 'Metric renamed',
      level: 'review' as const,
      evidence: { source: 'PrometheusRule', path: 'spec.groups' },
      impact: 'A query stops matching.',
      remediation: 'Update the query.',
      references: [],
    }
    const groups = groupFindings([
      { ...base, evidence: { ...base.evidence, detail: 'old_a' } },
      { ...base, evidence: { ...base.evidence, detail: 'old_b' } },
    ])
    expect(groups[0].total).toBe(2)
  })
})

describe('summaryMeta', () => {
  it('keeps partial coverage explicit even when a confirmed blocker takes precedence', () => {
    const meta = summaryMeta('blocked', '1.36', '1.36', {
      blocked: 1,
      warnings: 0,
      reviews: 0,
      unknown: 2,
    }, 'partial')
    expect(meta.body).toContain('Evidence coverage is also incomplete')
  })
})

describe('issueSpecificReferences', () => {
  it('keeps check-wide sources at the parent while preserving issue-specific sources', () => {
    const checkReferences = [{ title: 'Admission webhook good practices', url: 'https://example.com/admission' }]
    const findingReferences = [
      ...checkReferences,
      { title: 'Matching requests: matchPolicy', url: 'https://example.com/admission#match-policy' },
    ]

    expect(issueSpecificReferences(checkReferences, findingReferences)).toEqual([findingReferences[1]])
  })
})
