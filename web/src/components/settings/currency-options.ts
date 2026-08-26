import type { SelectMenuOption } from '@skyhook-io/k8s-ui'

const currencyNames = new Intl.DisplayNames(['en'], { type: 'currency' })
const currentISO4217CurrenciesAddedSinceCLDR32: Record<string, string> = {
  MRU: 'Mauritanian Ouguiya',
  SLE: 'Sierra Leonean Leone',
  UYW: 'Uruguayan Nominal Wage Index Unit',
  VED: 'Bolívar Soberano',
  VES: 'Venezuelan Bolívar',
  XAD: 'Arab Accounting Dinar',
  XCG: 'Caribbean Guilder',
  ZWG: 'Zimbabwean Gold',
}
const currencyCodes = [
  ...new Set([
    ...Intl.supportedValuesOf('currency'),
    ...Object.keys(currentISO4217CurrenciesAddedSinceCLDR32),
  ]),
]

export const CURRENCY_OPTIONS: SelectMenuOption[] = [
  { value: '', label: 'Auto (detect from OpenCost/Kubecost)' },
  ...currencyCodes
    .filter((code) => code !== 'XTS' && code !== 'XXX')
    .map((code) => {
      const name = currentISO4217CurrenciesAddedSinceCLDR32[code] ?? currencyNames.of(code)
      return {
        value: code,
        label: name && name !== code ? `${name} (${code})` : code,
      }
    })
    .sort((a, b) => a.label.localeCompare(b.label, 'en')),
]

export function currencyOptionsForValue(value: string): SelectMenuOption[] {
  if (!value || CURRENCY_OPTIONS.some((option) => option.value === value)) return CURRENCY_OPTIONS
  return [...CURRENCY_OPTIONS, { value, label: value }]
}
