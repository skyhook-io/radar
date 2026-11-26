export type SettingsSectionId =
  | 'overview'
  | 'perms'
  | 'connection'
  | 'prometheus'
  | 'cost'
  | 'argocd'
  | 'substrate'
  | 'ai'
  | 'advanced'
  | 'privacy'

export type IntegrationSectionId = 'prometheus' | 'cost' | 'argocd'
// Tabs whose drafts save through their own form, not the startup footer.
export type PendingSectionId = IntegrationSectionId | 'substrate' | 'ai'

export const pendingSectionLabels: Record<PendingSectionId, string> = {
  prometheus: 'Metrics', cost: 'Cost', argocd: 'Argo CD', substrate: 'Agent Substrate', ai: 'AI investigations',
}

/**
 * Agent Substrate runs on few clusters, so its section shows only where it can
 * matter: the cluster serves Substrate's API, a connection is still saved for
 * this cluster (so it can be disconnected), or the form holds a draft.
 */
export function showsSubstrateSection(input: { supported: boolean; discovered: boolean; saved: boolean; dirty: boolean }): boolean {
  return input.dirty || (input.supported && (input.discovered || input.saved))
}

export function pendingSections(dirty: Record<PendingSectionId, boolean>): PendingSectionId[] {
  return (['prometheus', 'cost', 'argocd', 'substrate', 'ai'] as const).filter(section => dirty[section])
}

// AI preferences are open to every user, so an AI draft pending in another tab
// shows the footer even without owner access.
export function shouldShowSettingsFooter(input: {
  canEditConfig: boolean
  confirmingClose: boolean
  configDirty: boolean
  integrationDirty: boolean
  aiDirtyElsewhere: boolean
  hasSaveMessage: boolean
}): boolean {
  return (
    input.confirmingClose ||
    input.aiDirtyElsewhere ||
    (input.canEditConfig &&
      (input.configDirty ||
        input.integrationDirty ||
        input.hasSaveMessage))
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
