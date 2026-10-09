import { describe, expect, it } from 'vitest'
import { hasFilterIntent, kindFiltersFromSearch, kindFiltersToSearch } from './ResourcesView'
import { serializeColumnFilters } from './resource-utils'

describe('kindFiltersFromSearch', () => {
  it('restores column filters, their exclude operator and problem filters', () => {
    const filters = serializeColumnFilters({ namespace: ['a', 'b'], status: ['Running'] }, { status: true })
    const search = `?${new URLSearchParams({ filters, problems: 'failed,pending', search: 'web' })}`

    expect(kindFiltersFromSearch(search)).toEqual({
      columnFilters: { namespace: ['a', 'b'], status: ['Running'] },
      columnFilterExcludes: { status: true },
      problemFilters: ['failed', 'pending'],
    })
  })

  it('starts empty for a kind with nothing remembered', () => {
    expect(kindFiltersFromSearch(undefined)).toEqual({ columnFilters: {}, columnFilterExcludes: {}, problemFilters: [] })
  })
})

describe('kindFiltersToSearch', () => {
  it('round-trips through kindFiltersFromSearch and keeps only the per-kind filters', () => {
    const filters = serializeColumnFilters({ namespace: ['a'], status: ['Running'] }, { status: true })
    const saved = kindFiltersToSearch(kindFiltersFromSearch(`?${new URLSearchParams({ filters, problems: 'failed', search: 'web', namespaces: 'x' })}`))

    expect(new URLSearchParams(saved).get('search')).toBeNull()
    expect(kindFiltersFromSearch(saved)).toEqual({
      columnFilters: { namespace: ['a'], status: ['Running'] },
      columnFilterExcludes: { status: true },
      problemFilters: ['failed'],
    })
  })

  it('is empty when there is nothing to save, ignoring an exclude flag with no values', () => {
    expect(kindFiltersToSearch({ columnFilters: { status: [] }, columnFilterExcludes: { status: true }, problemFilters: [] })).toBe('')
  })
})

describe('hasFilterIntent', () => {
  it('is false for a bare list URL, even with the namespace pick or a kind group', () => {
    expect(hasFilterIntent('')).toBe(false)
    expect(hasFilterIntent('?namespaces=a,b&apiGroup=serving.knative.dev&ai-run=r1')).toBe(false)
  })

  it.each(['filters=status:Running', 'problems=failed', 'search=web', 'regex=true', 'labels=app%3Dweb', 'ownerKind=DaemonSet', 'ownerName=x', 'resource=a/web', 'showInactive=true'])(
    'is true when the URL carries %s',
    (param) => {
      expect(hasFilterIntent(`?namespaces=a&${param}`)).toBe(true)
    },
  )
})
