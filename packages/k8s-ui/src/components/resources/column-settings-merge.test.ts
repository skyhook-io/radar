import { describe, expect, it } from 'vitest'
import { mergeSavedVisibleColumns, type Column, type ExtraColumn } from './ResourcesView'

// A saved blob records only what is VISIBLE, so a curated key's absence is
// ambiguous: the user hid it, or it did not exist when they saved. `known` — the
// key set they were offered at save time — is what separates the two. Without
// it, every column added to a curated kind is invisible to anyone who has ever
// touched that kind's column picker.

const col = (key: string, defaultVisible?: boolean): Column =>
  defaultVisible === undefined ? { key, label: key } : { key, label: key, defaultVisible }

const printer = (key: string, defaultVisible?: boolean): ExtraColumn =>
  ({ ...col(key, defaultVisible), render: () => null })

describe('mergeSavedVisibleColumns', () => {
  it('shows a curated column added since the blob was written', () => {
    const effective = [col('name'), col('status'), col('lastSuccess')]
    const got = mergeSavedVisibleColumns(['name', 'status'], [], [], effective, ['name', 'status'])
    expect(got.has('lastSuccess')).toBe(true)
  })

  it('keeps a column the user actually hid hidden', () => {
    // 'status' was offered and is absent from visible — a deliberate hide.
    const effective = [col('name'), col('status')]
    const got = mergeSavedVisibleColumns(['name'], [], [], effective, ['name', 'status'])
    expect(got.has('status')).toBe(false)
  })

  it('respects defaultVisible:false on a newly added column', () => {
    const effective = [col('name'), col('podIP', false)]
    const got = mergeSavedVisibleColumns(['name'], [], [], effective, ['name'])
    expect(got.has('podIP')).toBe(false)
  })

  it('leaves blobs written before `known` on the old behaviour', () => {
    // No record of what was offered, so absence stays ambiguous and we don't
    // guess — the next save adopts the current set as the baseline.
    const effective = [col('name'), col('status'), col('lastSuccess')]
    const got = mergeSavedVisibleColumns(['name', 'status'], [], [], effective, undefined)
    expect(got.has('lastSuccess')).toBe(false)
    expect(Array.from(got).sort()).toEqual(['name', 'status'])
  })

  it('does nothing without the effective column set', () => {
    const got = mergeSavedVisibleColumns(['name'], [], [], undefined, ['name'])
    expect(Array.from(got)).toEqual(['name'])
  })

  it('still seeds host extras unconditionally', () => {
    const got = mergeSavedVisibleColumns(['name'], ['cluster'], [], [col('name')], ['name'])
    expect(got.has('cluster')).toBe(true)
  })

  it('still seeds printer columns only when the blob names none', () => {
    const cols = [printer('printer:Phase'), printer('printer:Ready')]
    expect(mergeSavedVisibleColumns(['name'], [], cols).has('printer:Phase')).toBe(true)
    // Once the blob names one, the user has seen the set — absence is a hide.
    expect(mergeSavedVisibleColumns(['name', 'printer:Ready'], [], cols).has('printer:Phase')).toBe(false)
  })

  it('seeds a new curated column alongside printer columns without disturbing them', () => {
    const cols = [printer('printer:Phase')]
    const effective = [col('name'), col('lastSuccess'), printer('printer:Phase')]
    const got = mergeSavedVisibleColumns(['name', 'printer:Phase'], [], cols, effective, ['name', 'printer:Phase'])
    expect(got.has('lastSuccess')).toBe(true)
    expect(got.has('printer:Phase')).toBe(true)
  })
})
