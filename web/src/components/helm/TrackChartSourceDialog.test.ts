import { describe, expect, it } from 'vitest'
import { getSourceIssueCopy, isValidHelmRepositoryURL, normalizedRepositoryName, recordedRepositoryName, repositoryDisplayURL, sourceStateLabel, sourceStatusLine } from './TrackChartSourceDialog'

describe('chart source recovery presentation', () => {
  it('keeps recorded, configured, and available states distinct', () => {
    expect(sourceStateLabel(false, false, false)).toBe('Not recorded')
    expect(sourceStateLabel(true, false, false)).toBe('Recorded · not configured here')
    expect(sourceStateLabel(true, true, false)).toBe('Recorded and configured · source unavailable')
    expect(sourceStateLabel(true, true, true)).toBe('Recorded · configured · available')
  })

  it('claims nothing about the recorded source until it has been read', () => {
    expect(sourceStatusLine(undefined, undefined)).toBe('Checking the recorded source...')
    expect(sourceStatusLine(undefined, new Error('failed to load repo file'))).toBe("Could not read this release's source: failed to load repo file")
    expect(sourceStatusLine({ configured: false, available: false }, new Error('refetch failed'))).toBe('Not recorded')
    expect(sourceStatusLine({ recorded: { type: 'repository', reference: 'repo', url: 'https://charts.example.test' }, configured: false, available: false }, undefined)).toBe('Recorded · not configured here')
  })

  it('makes ambiguity explicit rather than selecting a source', () => {
    expect(getSourceIssueCopy('ambiguous_source')?.title).toBe('Chart source unresolved')
    expect(getSourceIssueCopy('ambiguous_source')?.body).toContain('choose a source for the next upgrade')
  })

  it('accepts only HTTP repository URLs', () => {
    expect(isValidHelmRepositoryURL('https://charts.example.test')).toBe(true)
    expect(isValidHelmRepositoryURL('oci://registry.example.test/charts')).toBe(false)
  })

  it('picks a free local name for the recorded repository', () => {
    expect(recordedRepositoryName('podinfo', [])).toBe('podinfo')
    expect(recordedRepositoryName('podinfo', [{ name: 'Podinfo', url: 'https://impostor.example.test' }])).toBe('podinfo-2')
    expect(recordedRepositoryName('podinfo', [{ name: 'podinfo', url: 'a' }, { name: 'podinfo-2', url: 'b' }])).toBe('podinfo-3')
  })

  it('checks names the way the server stores them', () => {
    expect(normalizedRepositoryName(' My Charts! ')).toBe('my-charts')
    const alias40 = 'a'.repeat(40)
    expect(recordedRepositoryName(alias40, [{ name: alias40, url: 'a' }])).toBe(`${'a'.repeat(38)}-2`)
    // A longer alias is stored cut to 40 characters, so that is the name that collides.
    const alias45 = `${'b'.repeat(40)}extra`
    expect(recordedRepositoryName(alias45, [])).toBe('b'.repeat(40))
    expect(recordedRepositoryName(alias45, [{ name: 'b'.repeat(40), url: 'a' }])).toBe(`${'b'.repeat(38)}-2`)
    expect(recordedRepositoryName('My_Repo', [{ name: 'my_repo', url: 'a' }])).toBe('my_repo-2')
    expect(recordedRepositoryName('___', [])).toBe('')
  })

  it('shortens repository URLs for labels', () => {
    expect(repositoryDisplayURL('https://stefanprodan.github.io/podinfo/')).toBe('stefanprodan.github.io/podinfo')
    expect(repositoryDisplayURL('https://charts.example.test')).toBe('charts.example.test')
  })
})
