import { afterEach, describe, expect, it, vi } from 'vitest'
import { CURRENCY_OPTIONS, currencyOptionsForValue } from './currency-options'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('currency options', () => {
  it('offers Auto followed by named ISO 4217 currencies', () => {
    expect(CURRENCY_OPTIONS[0]).toEqual({
      value: '',
      label: 'Auto (detect from OpenCost/Kubecost)',
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

  it('supplements current codes when browser currency data lags', async () => {
    vi.spyOn(Intl, 'supportedValuesOf').mockReturnValue(['EUR', 'USD'])
    vi.resetModules()
    const { CURRENCY_OPTIONS: options } = await import('./currency-options')

    for (const code of ['MRU', 'SLE', 'UYW', 'VED', 'VES', 'XAD', 'XCG', 'ZWG']) {
      expect(options.some((option) => option.value === code)).toBe(true)
    }
  })

  it('shows an existing saved code instead of misrepresenting it as Auto', () => {
    expect(currencyOptionsForValue('EUR')).toBe(CURRENCY_OPTIONS)
    expect(currencyOptionsForValue('ADP')).toContainEqual({ value: 'ADP', label: 'ADP' })
  })
})
