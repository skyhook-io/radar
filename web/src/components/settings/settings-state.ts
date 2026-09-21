export type SettingsSectionId =
  | 'overview'
  | 'perms'
  | 'connection'
  | 'prometheus'
  | 'cost'
  | 'argocd'
  | 'ai'
  | 'advanced'

export function shouldOfferCostReview(
  costIntegrationDirty: boolean,
  section: SettingsSectionId,
): boolean {
  return costIntegrationDirty && section !== 'cost'
}

export function shouldShowSettingsFooter(input: {
  canEditConfig: boolean
  confirmingClose: boolean
  configDirty: boolean
  costIntegrationDirty: boolean
  section: SettingsSectionId
  hasSaveMessage: boolean
}): boolean {
  return (
    input.canEditConfig &&
    (input.confirmingClose ||
      input.configDirty ||
      shouldOfferCostReview(input.costIntegrationDirty, input.section) ||
      input.hasSaveMessage)
  )
}

export function costSourceApplyLabel(
  source: 'auto' | 'prometheus' | 'kubecost',
): string {
  return source === 'prometheus' ? 'Apply source' : 'Test & apply source'
}

export function prometheusHeadersFromRows(
  rows: { key: string; value: string }[] | null,
): Record<string, string> | undefined {
  if (rows === null) return undefined
  if (rows.length === 0) return {}
  const entries: [string, string][] = []
  const seen = new Set<string>()
  for (const row of rows) {
    const key = row.key.trim()
    if (!key && !row.value) continue
    if (!key || !row.value) throw new Error('Enter both a name and value for each header, or remove the incomplete row.')
    if (seen.has(key.toLowerCase())) throw new Error(`Header "${key}" is entered more than once.`)
    seen.add(key.toLowerCase())
    entries.push([key, row.value])
  }
  return entries.length ? Object.fromEntries(entries) : undefined
}
