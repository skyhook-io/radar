import type { SelectMenuOption } from '@skyhook-io/k8s-ui'

const currencyNames = new Intl.DisplayNames(['en'], { type: 'currency' })

export const CURRENCY_OPTIONS: SelectMenuOption[] = [
  { value: '', label: 'Auto (detect from OpenCost)' },
  ...Intl.supportedValuesOf('currency')
    .filter((code) => code !== 'XTS' && code !== 'XXX')
    .map((code) => {
      const name = currencyNames.of(code);
      return {
        value: code,
        label: name && name !== code ? `${name} (${code})` : code,
      }
    })
    .sort((a, b) => a.label.localeCompare(b.label, 'en')),
]
