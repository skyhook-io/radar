import { describe, expect, it } from 'vitest'
import { namespacesNoScheduleIncludes, scheduleIncludes, veleroGlobMatch, veleroSelectionProblem } from './coverage'

const sel = (includedNamespaces: string[], excludedNamespaces: string[] = []) => ({ includedNamespaces, excludedNamespaces })

describe('veleroGlobMatch', () => {
  it.each([
    ['shop', 'shop', true],
    ['shop', 'shop-2', false],
    ['shop-*', 'shop-eu', true],
    ['shop-?', 'shop-1', true],
    ['shop-?', 'shop-12', false],
    ['db-[ab]', 'db-a', true],
    ['db-[a-c]', 'db-b', true],
    ['ns-[ab-d]-prod', 'ns-c-prod', false],
    ['ns-[ab-d]-prod', 'ns---prod', true],
    ['*-prod-*', 'eu-prod-1', true],
    ['*a*', 'bbb', false],
    ['a.b', 'axb', false],
    // Patterns Velero refuses, so a schedule using them backs up nothing.
    ['[unclosed', '[unclosed', true],
    ['db-[]', 'db-a', false],
    ['db-[!ab]', 'db-c', false],
    ['{shop,db}-prod', 'db-prod', false],
    ['[z-a]*', 'shop', false],
  ])('%s against %s is %s', (pattern, name, want) => {
    expect(veleroGlobMatch(pattern, name)).toBe(want)
  })
})

describe('veleroGlobMatch on hostile patterns', () => {
  it('matches many stars against a long name without backtracking', () => {
    const start = performance.now()
    expect(veleroGlobMatch('*a'.repeat(24) + 'b', 'a'.repeat(63))).toBe(false)
    expect(performance.now() - start).toBeLessThan(200)
  })
  it('matches a pattern longer than a namespace name only as written', () => {
    const pattern = '*'.repeat(10_000) + '?'
    expect(veleroGlobMatch(pattern, 'shop')).toBe(false)
    expect(veleroGlobMatch(pattern, pattern)).toBe(true)
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

describe('veleroSelectionProblem', () => {
  it.each([
    [['*', 'payments'], [], 'it includes “*” beside other namespaces'],
    [[], ['*'], 'it excludes “*”'],
    [['shop'], ['shop'], 'it both includes and excludes shop'],
    [['Shop'], [], 'Shop is not a namespace name or pattern Velero accepts'],
    [['shop-{a,b}'], [], 'shop-{a,b} is not a namespace name or pattern Velero accepts'],
    [['prod-**'], [], 'prod-** is not a namespace name or pattern Velero accepts'],
    [['db-[]'], [], 'db-[] is not a namespace name or pattern Velero accepts'],
    [['db-a]'], [], 'db-a] is not a namespace name or pattern Velero accepts'],
  ])('refuses %j excluding %j', (inc, exc, problem) => {
    expect(veleroSelectionProblem(sel(inc, exc))).toBe(problem)
    expect(scheduleIncludes(sel(inc, exc), 'shop')).toBe(false)
  })

  it.each([[[], []], [['*'], ['kube-*']], [['shop-*', 'db-[ab]'], ['']], [['*', '*'], []], [['shop', 'shop'], ['db']]])('accepts %j excluding %j', (inc, exc) => {
    expect(veleroSelectionProblem(sel(inc, exc))).toBeUndefined()
  })
})
