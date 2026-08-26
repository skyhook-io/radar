import { describe, expect, it } from 'vitest'
import {
  formatCostAxis,
  formatCostPerHour,
  formatHistoricalSpend,
  formatProjectedDailyRate,
  formatProjectedMonthlyCost,
  formatProjectedMonthlyRate,
} from './format'

describe('cost formatters', () => {
  it('formats projected run rates from hourly allocation', () => {
    expect(formatProjectedDailyRate(0.1, 'USD')).toBe('~$2.40/day')
    expect(formatProjectedMonthlyCost(1, 'USD')).toBe('~$730.00')
    expect(formatProjectedMonthlyRate(0.1, 'USD')).toBe('~$73.00/mo')
  })

  it('keeps hourly rates explicit', () => {
    expect(formatCostPerHour(0.1, 'USD')).toBe('$0.100/hr')
  })

  it('does not turn insufficient history into zero spend', () => {
    expect(formatHistoricalSpend(1, 0, false, 'USD')).toBe('—')
    expect(formatHistoricalSpend(2, 0, false, 'USD')).toBe('$0.00')
    expect(formatHistoricalSpend(2, 1.25, false, 'USD')).toBe('~$1.25')
    expect(formatHistoricalSpend(2, 1.25, true, 'USD')).toBe('—')
  })

  it('uses the configured currency and its major-unit precision', () => {
    expect(formatProjectedDailyRate(0.1, 'EUR')).toBe('~€2.40/day')
    expect(formatCostPerHour(0.1, 'GBP')).toBe('£0.100/hr')
    expect(formatProjectedMonthlyCost(1, 'JPY')).toBe('~¥730')
    expect(formatCostPerHour(0.1, 'JPY')).toBe('¥0.100/hr')
    expect(formatHistoricalSpend(2, 0, false, 'JPY')).toBe('¥0')
    expect(formatCostAxis(0.000001, 'GBP')).toBe('<£0.00001')
  })

  it('normalizes codes and labels malformed currencies without impersonating USD', () => {
    expect(formatProjectedMonthlyCost(1, ' eur ')).toBe('~€730.00')
    expect(formatProjectedMonthlyCost(1, 'not-a-code')).toBe('~NOT-A-CODE 730.00')
  })

  it('falls back to USD when an older backend omits currency', () => {
    expect(formatProjectedMonthlyCost(1, undefined)).toBe('~$730.00')
  })
})
