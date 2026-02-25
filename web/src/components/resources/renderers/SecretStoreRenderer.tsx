import { ShieldCheck } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../drawer-components'
import {
  getSecretStoreStatus,
  getSecretStoreProviderType,
  getSecretStoreProviderKey,
  getSecretStoreProviderDetails,
  getSecretStoreRetrySettings,
  getSecretStoreController,
} from '../resource-utils-eso'

// Provider-specific color for visual distinction
function getProviderColor(providerKey: string): string {
  switch (providerKey) {
    case 'aws': return 'bg-orange-500/15 text-orange-400 border-orange-500/30'
    case 'azurekv': return 'bg-blue-500/15 text-blue-400 border-blue-500/30'
    case 'gcpsm': return 'bg-blue-500/15 text-blue-400 border-blue-500/30'
    case 'vault': return 'bg-purple-500/15 text-purple-400 border-purple-500/30'
    case 'kubernetes': return 'bg-cyan-500/15 text-cyan-400 border-cyan-500/30'
    case 'doppler': return 'bg-green-500/15 text-green-400 border-green-500/30'
    case 'onepassword': return 'bg-blue-500/15 text-blue-400 border-blue-500/30'
    case 'akeyless': return 'bg-indigo-500/15 text-indigo-400 border-indigo-500/30'
    default: return 'bg-theme-elevated text-theme-text-secondary border-theme-border'
  }
}

interface SecretStoreRendererProps {
  data: any
}

export function SecretStoreRenderer({ data }: SecretStoreRendererProps) {
  const conditions = data.status?.conditions || []

  const storeStatus = getSecretStoreStatus(data)
  const providerType = getSecretStoreProviderType(data)
  const providerKey = getSecretStoreProviderKey(data)
  const providerDetails = getSecretStoreProviderDetails(data)
  const retrySettings = getSecretStoreRetrySettings(data)
  const controller = getSecretStoreController(data)

  // Problem detection
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  const isNotReady = readyCond?.status === 'False'

  return (
    <>
      {/* Alert if not ready */}
      {isNotReady && (
        <AlertBanner
          variant="error"
          title="Store Not Ready"
          message={readyCond?.message || 'The SecretStore connection is not ready.'}
        />
      )}

      {/* Provider section */}
      <Section title="Provider" icon={ShieldCheck} defaultExpanded>
        <div className="mb-3">
          <span className={`inline-flex items-center px-2.5 py-1 rounded border text-sm font-medium ${getProviderColor(providerKey)}`}>
            {providerType}
          </span>
        </div>

        {providerDetails.length > 0 && (
          <PropertyList>
            {providerDetails.map((detail, i) => (
              <Property key={i} label={detail.label} value={detail.value} />
            ))}
          </PropertyList>
        )}

        {controller && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <PropertyList>
              <Property label="Controller" value={controller} />
            </PropertyList>
          </div>
        )}
      </Section>

      {/* Connection Status */}
      <Section title="Connection" defaultExpanded>
        <PropertyList>
          <Property label="Status" value={storeStatus.text} />
          {readyCond?.reason && readyCond.reason !== storeStatus.text && (
            <Property label="Reason" value={readyCond.reason} />
          )}
          {readyCond?.lastTransitionTime && (
            <Property label="Last Transition" value={new Date(readyCond.lastTransitionTime).toLocaleString()} />
          )}
        </PropertyList>
      </Section>

      {/* Retry Settings */}
      {retrySettings && (
        <Section title="Retry Settings" defaultExpanded={false}>
          <PropertyList>
            {retrySettings.maxRetries !== undefined && (
              <Property label="Max Retries" value={String(retrySettings.maxRetries)} />
            )}
            {retrySettings.retryInterval && (
              <Property label="Retry Interval" value={retrySettings.retryInterval} />
            )}
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
