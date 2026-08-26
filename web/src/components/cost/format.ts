export const COST_HOURS_PER_DAY = 24
export const COST_HOURS_PER_MONTH = 730
export const DEFAULT_COST_CURRENCY = 'USD'

type CurrencyFormat = { formatter: Intl.NumberFormat; prefix: string }

const currencyFormatters = new Map<string, CurrencyFormat>()

function currencyFormatter(currency: string | undefined, digits?: number): CurrencyFormat {
  const normalized = (currency ?? '').trim().toUpperCase() || DEFAULT_COST_CURRENCY
  const key = `${normalized}:${digits ?? 'default'}`
  const cached = currencyFormatters.get(key)
  if (cached) return cached

  try {
    const options: Intl.NumberFormatOptions = {
      style: 'currency',
      currency: normalized,
    }
    if (digits !== undefined) {
      options.minimumFractionDigits = digits
      options.maximumFractionDigits = digits
    }
    const formatter = new Intl.NumberFormat('en-US', options)
    const result = { formatter, prefix: '' }
    currencyFormatters.set(key, result)
    return result
  } catch {
    const fallbackDigits = digits ?? 2
    const options: Intl.NumberFormatOptions = {
      minimumFractionDigits: fallbackDigits,
      maximumFractionDigits: fallbackDigits,
    }
    const result = {
      formatter: new Intl.NumberFormat('en-US', options),
      prefix: `${normalized} `,
    }
    currencyFormatters.set(key, result)
    return result
  }
}

function formatCurrency(value: number, currency: string | undefined, digits?: number): string {
  const { formatter, prefix } = currencyFormatter(currency, digits)
  return `${prefix}${formatter.format(value)}`
}

export function formatCostAxis(value: number, currency: string | undefined): string {
  if (!Number.isFinite(value) || value <= 0) return formatCurrency(0, currency, 0)
  if (value >= 1000) return `${formatCurrency(value / 1000, currency, 0)}k`
  if (value >= 1) return formatCurrency(value, currency, 1)
  if (value >= 0.01) return formatCurrency(value, currency, 2)
  if (value >= 0.0001) return formatCurrency(value, currency, 4)
  if (value >= 0.00001) return formatCurrency(value, currency, 5)
  return `<${formatCurrency(0.00001, currency, 5)}`
}

export function formatCost(value: number, currency: string | undefined): string {
  if (!Number.isFinite(value) || value <= 0) return formatCurrency(0, currency)
  if (value >= 1000) return `${formatCurrency(value / 1000, currency, 1)}k`
  if (value >= 1) return formatCurrency(value, currency)
  if (value >= 0.01) return formatCurrency(value, currency, 3)
  if (value >= 0.0001) return formatCurrency(value, currency, 4)
  return formatCostAxis(value, currency)
}

export function formatCostPerHour(value: number, currency: string | undefined): string {
  return `${formatCost(value, currency)}/hr`
}

export function formatHistoricalSpend(
  pointCount: number,
  windowTotalCost: number,
  unavailable: boolean,
  currency: string | undefined,
): string {
  if (unavailable || pointCount < 2) return '—'
  return windowTotalCost > 0
    ? `~${formatCost(windowTotalCost, currency)}`
    : formatCost(0, currency)
}

export function formatProjectedDailyCost(hourlyCost: number, currency: string | undefined): string {
  return `~${formatCost(hourlyCost * COST_HOURS_PER_DAY, currency)}`
}

export function formatProjectedDailyRate(hourlyCost: number, currency: string | undefined): string {
  return `${formatProjectedDailyCost(hourlyCost, currency)}/day`
}

export function formatProjectedMonthlyCost(hourlyCost: number, currency: string | undefined): string {
  return `~${formatCost(hourlyCost * COST_HOURS_PER_MONTH, currency)}`
}

export function formatProjectedMonthlyRate(hourlyCost: number, currency: string | undefined): string {
  return `${formatProjectedMonthlyCost(hourlyCost, currency)}/mo`
}
