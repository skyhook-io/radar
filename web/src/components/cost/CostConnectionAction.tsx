import type { CostUnavailableReason } from '../../api/client'
import type { ReactNode } from 'react'
import { useNavCustomization } from '../../context/NavCustomization'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { usePreviousIntegrationSettings } from '../../hooks/usePreviousIntegrationSettings'
import { costConfigurationAction, isCostConfigurable } from './source'

export function CostConnectionAction({ reason, children }: { reason?: CostUnavailableReason | 'load_error'; children?: ReactNode }) {
  const { embedded } = useNavCustomization()
  const { configManagement } = useCapabilitiesContext()
  const available = !embedded && isCostConfigurable(reason)
  const offers = usePreviousIntegrationSettings(available)
  if (!available) return <>{children}</>
  const action = costConfigurationAction(reason as CostUnavailableReason, offers)
  return (
    <div className="flex flex-col items-center gap-2 text-center">
      {action.note && <p className="text-xs text-theme-text-secondary">{action.note}</p>}
      <div className="flex flex-wrap items-center justify-center gap-2">
        <button
          type="button"
          onClick={() => window.dispatchEvent(new CustomEvent('radar:open-settings', { detail: { section: action.section } }))}
          className="btn-brand px-3 py-1.5 text-xs font-medium"
        >
          {configManagement === 'operator' || configManagement === 'cloud' ? 'View connection settings' : action.label}
        </button>
        {children}
      </div>
    </div>
  )
}
