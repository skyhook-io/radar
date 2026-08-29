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
