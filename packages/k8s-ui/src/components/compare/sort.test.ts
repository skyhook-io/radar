import { describe, it, expect } from 'vitest'
import { sortCandidates, filterCandidates } from './sort'

const c = (namespace: string, name: string) => ({ namespace, name })

describe('sortCandidates', () => {
  it('puts same-namespace candidates first', () => {
    const result = sortCandidates(
      [c('other', 'aa'), c('prod', 'zz'), c('other', 'bb'), c('prod', 'aa')],
      { namespace: 'prod', name: 'src' },
    )
    expect(result.map(r => r.namespace)).toEqual(['prod', 'prod', 'other', 'other'])
  })

  it('alphabetizes within the same-namespace group', () => {
    const result = sortCandidates(
      [c('prod', 'zz'), c('prod', 'aa'), c('prod', 'mm')],
      { namespace: 'prod', name: 'src' },
    )
    expect(result.map(r => r.name)).toEqual(['aa', 'mm', 'zz'])
  })

  it('alphabetizes within the other-namespace group too', () => {
    const result = sortCandidates(
      [c('other', 'zz'), c('other', 'aa')],
      { namespace: 'prod', name: 'src' },
    )
    expect(result.map(r => r.name)).toEqual(['aa', 'zz'])
  })

  it('excludes the source itself', () => {
    const result = sortCandidates(
      [c('prod', 'src'), c('prod', 'api')],
      { namespace: 'prod', name: 'src' },
    )
    expect(result).toEqual([c('prod', 'api')])
  })

  it('tie-breaks identical names on namespace', () => {
    const result = sortCandidates(
      [c('zeta', 'api'), c('alpha', 'api')],
      { namespace: 'prod', name: 'src' },
    )
    expect(result.map(r => r.namespace)).toEqual(['alpha', 'zeta'])
  })

  it('does not mutate input', () => {
    const input = [c('prod', 'zz'), c('prod', 'aa')]
    sortCandidates(input, { namespace: 'prod', name: 'src' })
    expect(input).toEqual([c('prod', 'zz'), c('prod', 'aa')])
  })

  it('handles empty candidate list', () => {
    expect(sortCandidates([], { namespace: 'prod', name: 'src' })).toEqual([])
  })
})

describe('filterCandidates', () => {
  it('returns full list for empty query', () => {
    const list = [c('prod', 'api'), c('staging', 'web')]
    expect(filterCandidates(list, '')).toEqual(list)
  })

  it('returns full list for whitespace-only query', () => {
    const list = [c('prod', 'api')]
    expect(filterCandidates(list, '   ')).toEqual(list)
  })

  it('filters by name substring (case insensitive)', () => {
    const result = filterCandidates([c('prod', 'mongo-api'), c('prod', 'web')], 'MONGO')
    expect(result).toEqual([c('prod', 'mongo-api')])
  })

  it('filters by namespace substring', () => {
    const result = filterCandidates([c('mongodb', 'api'), c('redis', 'api')], 'mongo')
    expect(result).toEqual([c('mongodb', 'api')])
  })
})
