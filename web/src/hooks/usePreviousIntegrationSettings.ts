import { useQuery } from '@tanstack/react-query'
import { fetchJSON } from '../api/client'
import { getApiBase } from '../api/config'
import { useConnection } from '../context/ConnectionContext'
import { useContextSwitch } from '../context/ContextSwitchContext'
import { useNavCustomization } from '../context/NavCustomization'
import { useCapabilitiesContext } from '../contexts/CapabilitiesContext'
import type { IntegrationKind, IntegrationProfiles } from '../components/settings/LocalConnectionSettings'

export interface IntegrationSettingsResponse {
  management: 'local' | 'operator' | 'cloud'
  integrationProfiles?: IntegrationProfiles
}

export interface PreviousIntegrationSettings {
  metrics: boolean
  argocd: boolean
  cost: boolean
  explicitCostBackend: boolean
}

const noOffers: PreviousIntegrationSettings = { metrics: false, argocd: false, cost: false, explicitCostBackend: false }
export const previousIntegrationSettingsKey = 'previous-integration-settings'

export function previousIntegrationOffers(response: IntegrationSettingsResponse, context: string): PreviousIntegrationSettings {
  if (response.management !== 'local' || !context) return noOffers
  const eligible = (kind: IntegrationKind) => {
    const profile = response.integrationProfiles?.[kind]
    return profile?.target.context === context && profile.state === 'auto' && !!profile.legacy && !profile.legacy.error
  }
  const cost = eligible('cost')
  const legacyCost = response.integrationProfiles?.cost.legacy
  return {
    metrics: eligible('metrics'), argocd: eligible('argocd'), cost,
    explicitCostBackend: cost && !!legacyCost?.url && (legacyCost.mode === 'auto' || legacyCost.mode === 'kubecost'),
  }
}

export function usePreviousIntegrationSettings(relevant: boolean): PreviousIntegrationSettings {
  const { connection } = useConnection()
  const { isSwitching } = useContextSwitch()
  const { embedded } = useNavCustomization()
  const { configManagement } = useCapabilitiesContext()
  const enabled = relevant && !embedded && configManagement === 'local' && connection.state === 'connected' && !isSwitching && !!connection.context
  const query = useQuery({
    queryKey: [previousIntegrationSettingsKey, getApiBase(), connection.context],
    queryFn: async ({ signal }) => previousIntegrationOffers(await fetchJSON<IntegrationSettingsResponse>('/config', signal), connection.context),
    enabled,
    retry: false,
    staleTime: 30_000,
  })
  return enabled && !query.isError ? query.data ?? noOffers : noOffers
}

export function previousSettingsAction(kind: IntegrationKind): { label: string; note: string } {
  const name = kind === 'argocd' ? 'Argo CD' : kind === 'metrics' ? 'metrics' : 'cost'
  return { label: 'Review previous settings', note: `Previous ${name} settings are available to review for this cluster.` }
}
