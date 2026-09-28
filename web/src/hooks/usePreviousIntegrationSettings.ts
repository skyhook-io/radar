import { useQuery } from '@tanstack/react-query'
import { fetchJSON } from '../api/client'
import { getApiBase } from '../api/config'
import { useConnection } from '../context/ConnectionContext'
import { useContextSwitch } from '../context/ContextSwitchContext'
import { useNavCustomization } from '../context/NavCustomization'
import { useCapabilitiesContext } from '../contexts/CapabilitiesContext'
import type { ConnectionResponse, IntegrationKind, IntegrationProfiles } from '../components/settings/LocalConnectionSettings'

export interface IntegrationSettingsResponse {
  management: 'local' | 'operator' | 'cloud'
  integrationProfiles?: IntegrationProfiles
}

// 'previous' offers this context the old global settings; 'copy' points to
// another cluster's saved settings once those are the way to reuse them.
export type SettingsOffer = 'previous' | 'copy'

export interface PreviousIntegrationSettings {
  metrics?: SettingsOffer
  argocd?: SettingsOffer
  cost?: SettingsOffer
  explicitCostBackend: boolean
}

const noOffers: PreviousIntegrationSettings = { explicitCostBackend: false }
export const previousIntegrationSettingsKey = 'previous-integration-settings'

export function previousIntegrationOffers(
  response: IntegrationSettingsResponse,
  context: string,
  catalog?: ConnectionResponse['connections'],
): PreviousIntegrationSettings {
  if (response.management !== 'local' || !context) return noOffers
  const offer = (kind: IntegrationKind): SettingsOffer | undefined => {
    const profile = response.integrationProfiles?.[kind]
    if (profile?.target.context !== context || profile.state !== 'auto') return undefined
    if (profile.legacy && !profile.legacy.error && !profile.legacy.noticeDismissed) return 'previous'
    // Another cluster's Argo CD server manages that cluster's Applications, so
    // copying it is rarely the fix for this one; Settings still offers it.
    if (kind === 'argocd') return undefined
    return catalog?.some(entry => entry.integration === kind && !entry.error && entry.url && entry.binding !== profile.target.binding)
      ? 'copy' : undefined
  }
  const cost = offer('cost')
  const legacyCost = response.integrationProfiles?.cost.legacy
  return {
    metrics: offer('metrics'), argocd: offer('argocd'), cost,
    explicitCostBackend: cost === 'copy' ||
      (cost === 'previous' && !!legacyCost?.url && (legacyCost.mode === 'auto' || legacyCost.mode === 'kubecost')),
  }
}

async function loadOffers(context: string, signal: AbortSignal): Promise<PreviousIntegrationSettings> {
  const response = await fetchJSON<IntegrationSettingsResponse>('/config', signal)
  const offers = previousIntegrationOffers(response, context)
  const unoffered = (['metrics', 'argocd', 'cost'] as const).some(kind =>
    !offers[kind] && response.integrationProfiles?.[kind]?.state === 'auto')
  if (response.management !== 'local' || !unoffered) return offers
  const catalog = await fetchJSON<ConnectionResponse>('/integrations/connections', signal)
    .then(data => data.connections, () => undefined)
  return previousIntegrationOffers(response, context, catalog)
}

export function usePreviousIntegrationSettings(relevant: boolean): PreviousIntegrationSettings {
  const { connection } = useConnection()
  const { isSwitching } = useContextSwitch()
  const { embedded } = useNavCustomization()
  const { configManagement } = useCapabilitiesContext()
  const enabled = relevant && !embedded && configManagement === 'local' && connection.state === 'connected' && !isSwitching && !!connection.context
  const query = useQuery({
    queryKey: [previousIntegrationSettingsKey, getApiBase(), connection.context],
    queryFn: ({ signal }) => loadOffers(connection.context, signal),
    enabled,
    retry: false,
    staleTime: 30_000,
  })
  return enabled && !query.isError ? query.data ?? noOffers : noOffers
}

const settingsTabs: Record<IntegrationKind, string> = { metrics: 'Metrics', argocd: 'Argo CD', cost: 'Cost' }

// Only a note: call sites keep their neutral action, because another cluster's
// backend connects fine while showing the wrong cluster's data.
export function previousSettingsNote(kind: IntegrationKind, offer: SettingsOffer): string {
  return offer === 'copy'
    ? 'Settings saved for another cluster are available to copy if that backend also serves this cluster.'
    : `Connections are now saved per cluster. You can copy your previous settings in Settings → ${settingsTabs[kind]}.`
}
