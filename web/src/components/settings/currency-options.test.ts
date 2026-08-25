import { describe, expect, it } from 'vitest'
import { CURRENCY_OPTIONS } from './currency-options'

describe('currency options', () => {
  it('offers Auto followed by named ISO 4217 currencies', () => {
    expect(CURRENCY_OPTIONS[0]).toEqual({
      value: '',
      label: 'Auto (detect from OpenCost)',
    })
    expect(CURRENCY_OPTIONS).toContainEqual({
      value: 'EUR',
      label: 'Euro (EUR)',
    })
    expect(CURRENCY_OPTIONS).toContainEqual({
      value: 'USD',
      label: 'US Dollar (USD)',
    })
    for (const code of ['MRU', 'SLE', 'UYW', 'VED', 'VES', 'XAD', 'XCG', 'ZWG']) {
      expect(CURRENCY_OPTIONS.some((option) => option.value === code)).toBe(true)
    }
  })

  it('contains unique uppercase three-letter currency codes', () => {
    const codes = CURRENCY_OPTIONS.slice(1).map((option) => option.value)
    expect(new Set(codes).size).toBe(codes.length)
    expect(codes.every((code) => /^[A-Z]{3}$/.test(code))).toBe(true)
    expect(codes).not.toContain('XTS')
    expect(codes).not.toContain('XXX')
  })
})
