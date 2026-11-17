import { describe, expect, it } from 'vitest'
import { namespacesNoScheduleIncludes, scheduleIncludes, veleroGlobMatch } from './coverage'

const sel = (includedNamespaces: string[], excludedNamespaces: string[] = []) => ({ includedNamespaces, excludedNamespaces })

describe('veleroGlobMatch', () => {
  it.each([
    ['shop', 'shop', true],
    ['shop', 'shop-2', false],
    ['shop-*', 'shop-eu', true],
    ['shop-?', 'shop-1', true],
    ['shop-?', 'shop-12', false],
    ['db-[ab]', 'db-a', true],
    ['db-[!ab]', 'db-a', false],
    ['db-[a-c]', 'db-b', true],
    ['ns-[ab-d]-prod', 'ns-c-prod', false],
    ['ns-[ab-d]-prod', 'ns---prod', true],
    ['db-[!a-c]', 'db-d', true],
    ['{shop,db}-prod', 'db-prod', true],
    ['{shop,db}-prod', 'web-prod', false],
    ['a.b', 'axb', false],
    ['[unclosed', '[unclosed', true],
  ])('%s against %s is %s', (pattern, name, want) => {
    expect(veleroGlobMatch(pattern, name)).toBe(want)
  })
})

describe('scheduleIncludes', () => {
  it('reads an empty or * include list as every namespace, with exclusions winning', () => {
    expect(scheduleIncludes(sel([]), 'shop')).toBe(true)
    expect(scheduleIncludes(sel(['*']), 'shop')).toBe(true)
    expect(scheduleIncludes(sel([], ['shop']), 'shop')).toBe(false)
    expect(scheduleIncludes(sel(['shop-*'], ['shop-scratch']), 'shop-scratch')).toBe(false)
    expect(scheduleIncludes(sel(['shop-*'], ['shop-scratch']), 'shop-eu')).toBe(true)
  })
})

describe('namespacesNoScheduleIncludes', () => {
  const schedule = (included: string[], excluded: string[] = []) => ({ ...sel(included, excluded) }) as any
  it('lists what no schedule selects, leaving out Kubernetes’ own and Velero’s namespaces', () => {
    const got = namespacesNoScheduleIncludes(
      ['shop', 'db', 'web', 'kube-system', 'kube-flannel', 'velero'],
      [schedule(['shop']), schedule(['d*'])],
      ['velero'],
    )
    expect(got).toEqual({ uncovered: ['web'], leftOut: 3 })
  })
  it('lists every namespace when there is no schedule', () => {
    expect(namespacesNoScheduleIncludes(['b', 'a'], [], []).uncovered).toEqual(['a', 'b'])
  })
})
