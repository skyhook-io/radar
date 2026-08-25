import type { SelectMenuOption } from '@skyhook-io/k8s-ui'

const currencyNames = new Intl.DisplayNames(['en'], { type: 'currency' })
const currentISO4217CurrenciesMissingFromIntl: Record<string, string> = {
  UYW: 'Unidad Previsional',
  VED: 'Bolívar Soberano',
  XAD: 'Arab Accounting Dinar',
}
const currencyCodes = [
  ...new Set([
    ...Intl.supportedValuesOf('currency'),
    ...Object.keys(currentISO4217CurrenciesMissingFromIntl),
  ]),
]

export const CURRENCY_OPTIONS: SelectMenuOption[] = [
  { value: '', label: 'Auto (detect from OpenCost)' },
  ...currencyCodes
    .filter((code) => code !== 'XTS' && code !== 'XXX')
    .map((code) => {
      const name = currentISO4217CurrenciesMissingFromIntl[code] ?? currencyNames.of(code)
      return {
        value: code,
        label: name && name !== code ? `${name} (${code})` : code,
      }
    })
    .sort((a, b) => a.label.localeCompare(b.label, 'en')),
]
