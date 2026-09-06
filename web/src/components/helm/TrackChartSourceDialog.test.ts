import { describe, expect, it } from 'vitest'
import {
  canAddHelmRepository,
  getSourceIssueCopy,
  HELM_REPOSITORY_FORM_GRID_CLASS,
  isValidHelmRepositoryURL,
  isValidOCIPrefix,
  ociPrefixForCandidate,
  SOURCE_INPUT_BASE_CLASS,
  uniqueSourceCandidate,
} from './TrackChartSourceDialog'

describe('chart source forms', () => {
  it('keeps the classic row inside a responsive, shrinkable grid', () => {
    expect(HELM_REPOSITORY_FORM_GRID_CLASS).toContain('grid-cols-1')
    expect(HELM_REPOSITORY_FORM_GRID_CLASS).toContain('md:grid-cols-')
    expect(HELM_REPOSITORY_FORM_GRID_CLASS).toContain('minmax(0,')
  })

  it('makes every source input visually unmistakable and accessible', () => {
    expect(SOURCE_INPUT_BASE_CLASS).toContain('border')
    expect(SOURCE_INPUT_BASE_CLASS).toContain('bg-theme-elevated')
    expect(SOURCE_INPUT_BASE_CLASS).toContain('placeholder:text-theme-text-disabled')
    expect(SOURCE_INPUT_BASE_CLASS).toContain('focus:ring-2')
    expect(SOURCE_INPUT_BASE_CLASS).toContain('focus:border-accent')
    expect(SOURCE_INPUT_BASE_CLASS).toContain('disabled:opacity-50')
    expect(SOURCE_INPUT_BASE_CLASS).toContain('disabled:cursor-not-allowed')
  })

  it('accepts HTTP only in the classic form and OCI only in the OCI form', () => {
    expect(isValidHelmRepositoryURL('https://charts.example.test/')).toBe(true)
    expect(isValidHelmRepositoryURL('oci://ghcr.io/example')).toBe(false)
    expect(isValidOCIPrefix('https://charts.example.test/')).toBe(false)
    expect(isValidOCIPrefix('oci://ghcr.io/example')).toBe(true)
  })

  it('validates classic and OCI actions independently', () => {
    expect(canAddHelmRepository('example-repo', 'https://charts.example.test/')).toBe(true)
    expect(isValidOCIPrefix('https://wrong-field.example')).toBe(false)

    expect(canAddHelmRepository('', 'not-a-url')).toBe(false)
    expect(isValidOCIPrefix('oci://ghcr.io/example')).toBe(true)
  })

  it('automatically selects only an unambiguous recovered source', () => {
    const classic = { type: 'repository' as const, reference: 'example-repo' }
    expect(uniqueSourceCandidate([classic])).toEqual(classic)
    expect(uniqueSourceCandidate([classic, { type: 'oci', reference: 'oci://ghcr.io/example/chart' }])).toBeUndefined()
  })

  it('derives a registrable OCI prefix from a verified full chart reference', () => {
    expect(ociPrefixForCandidate('oci://ghcr.io/example/charts/app', 'app')).toBe('oci://ghcr.io/example/charts')
    expect(ociPrefixForCandidate('oci://ghcr.io/example/charts', 'app')).toBe('oci://ghcr.io/example/charts')
  })
})

describe('getSourceIssueCopy', () => {
  it('offers both classic repository and OCI recovery for untracked charts', () => {
    const copy = getSourceIssueCopy('untracked')
    expect(copy?.body).toContain('classic repository')
    expect(copy?.body).toContain('OCI source')
  })

  it('requires explicit selection for ambiguous classic and cross-source matches', () => {
    expect(getSourceIssueCopy('ambiguous_repository')?.body).toContain('Select')
    expect(getSourceIssueCopy('ambiguous_source')?.body).toContain('Select')
  })
})
