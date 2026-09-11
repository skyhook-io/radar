import { describe, expect, it } from 'vitest'
import { mergeSavedVisibleColumns, selectedKindIdentityOf } from './ResourcesView'

const printer = (key: string, defaultVisible = true) => ({ key, label: key, defaultVisible }) as any

describe('mergeSavedVisibleColumns', () => {
  it('keeps the saved set when it already knows the printer columns', () => {
    const merged = mergeSavedVisibleColumns(
      ['name', 'printer:Phase'],
      [],
      [printer('printer:Phase'), printer('printer:Host')],
    )
    // Host is absent from a blob that names another printer column, so the user
    // hid it on purpose — seeding it back would make it un-hideable.
    expect(merged.has('printer:Host')).toBe(false)
    expect(merged.has('printer:Phase')).toBe(true)
  })

  it('seeds printer defaults into a blob that predates them', () => {
    const merged = mergeSavedVisibleColumns(
      ['name', 'namespace', 'age'],
      [],
      [printer('printer:Phase'), printer('printer:Host')],
    )
    // Without this the table would render Name + Namespace + Age and nothing
    // else for every user who had ever touched this kind's columns.
    expect(merged.has('printer:Phase')).toBe(true)
    expect(merged.has('printer:Host')).toBe(true)
    expect(merged.has('name')).toBe(true)
  })

  it('does not seed a wide-tier column, which is hidden by default', () => {
    const merged = mergeSavedVisibleColumns(['name'], [], [printer('printer:Wide', false)])
    expect(merged.has('printer:Wide')).toBe(false)
  })

  it('always seeds host extras, which the embedding app injects', () => {
    const merged = mergeSavedVisibleColumns(['name'], ['cluster'], [])
    expect(merged.has('cluster')).toBe(true)
  })

  it('is a no-op for a kind with no printer columns', () => {
    expect([...mergeSavedVisibleColumns(['name', 'status'], [], [])].sort()).toEqual(['name', 'status'])
  })
})

describe('selectedKindIdentityOf', () => {
  // Two CRDs can ship the same plural in different API groups. Keyed on the
  // plural alone, a `printer:*` sort or filter survives the switch onto a table
  // with no such column, and every row silently fails the filter.
  it('separates the same plural in different groups', () => {
    expect(selectedKindIdentityOf({ name: 'widgets', group: 'a.io' }))
      .not.toBe(selectedKindIdentityOf({ name: 'widgets', group: 'b.io' }))
  })

  it('is stable for the same selection', () => {
    expect(selectedKindIdentityOf({ name: 'widgets', group: 'a.io' }))
      .toBe(selectedKindIdentityOf({ name: 'widgets', group: 'a.io' }))
  })

  it('treats an absent group and an empty group alike', () => {
    expect(selectedKindIdentityOf({ name: 'pods' })).toBe(selectedKindIdentityOf({ name: 'pods', group: '' }))
  })
})
