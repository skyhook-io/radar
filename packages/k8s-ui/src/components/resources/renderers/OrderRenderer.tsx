import { ShoppingCart, AlertTriangle, Globe, Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../../ui/drawer-components'

function getOrderStateBadge(state: string): { color: string; text: string } {
  switch (state?.toLowerCase()) {
    case 'valid':
      return { text: 'Valid', color: 'bg-green-500/20 text-green-400' }
    case 'ready':
      return { text: 'Ready', color: 'bg-blue-500/20 text-blue-400' }
    case 'pending':
      return { text: 'Pending', color: 'bg-yellow-500/20 text-yellow-400' }
    case 'invalid':
      return { text: 'Invalid', color: 'bg-red-500/20 text-red-400' }
    case 'expired':
      return { text: 'Expired', color: 'bg-red-500/20 text-red-400' }
    case 'errored':
      return { text: 'Errored', color: 'bg-red-500/20 text-red-400' }
    default:
      return { text: state || 'Unknown', color: 'bg-gray-500/20 text-gray-400' }
  }
}

export function OrderRenderer({ data }: { data: any }) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const state = status.state || ''
  const dnsNames = spec.dnsNames || []
  const issuerRef = spec.issuerRef || {}
  const authorizations = status.authorizations || []

  const isError = state === 'invalid' || state === 'expired' || state === 'errored'

  return (
    <>
      {/* Problem detection alert */}
      {isError && (
        <div className="mb-4 p-3 bg-red-500/10 border border-red-500/30 rounded-lg">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-red-400 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-red-400">Order {state.charAt(0).toUpperCase() + state.slice(1)}</div>
              {status.reason && (
                <div className="text-xs text-red-300/80 mt-1">{status.reason}</div>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Status */}
      <Section title="Status" icon={Shield}>
        <PropertyList>
          <Property
            label="State"
            value={
              <span className={clsx('badge', getOrderStateBadge(state).color)}>
                {getOrderStateBadge(state).text}
              </span>
            }
          />
          {status.url && <Property label="ACME URL" value={status.url} />}
        </PropertyList>
      </Section>

      {/* Domains */}
      {dnsNames.length > 0 && (
        <Section title="Domains" icon={Globe}>
          <div className="flex flex-wrap gap-1.5">
            {dnsNames.map((name: string) => (
              <span key={name} className="badge bg-theme-elevated text-theme-text-secondary">
                {name}
              </span>
            ))}
          </div>
        </Section>
      )}

      {/* Issuer */}
      {issuerRef.name && (
        <Section title="Issuer" icon={Shield}>
          <PropertyList>
            <Property label="Name" value={issuerRef.name} />
            <Property label="Kind" value={issuerRef.kind || 'ClusterIssuer'} />
            {issuerRef.group && <Property label="Group" value={issuerRef.group} />}
          </PropertyList>
        </Section>
      )}

      {/* Authorizations */}
      {authorizations.length > 0 && (
        <Section title={`Authorizations (${authorizations.length})`} icon={ShoppingCart}>
          <div className="space-y-1">
            {authorizations.map((auth: any, i: number) => (
              <div key={i} className="text-xs text-theme-text-secondary card-inner">
                {auth.url || `Authorization ${i + 1}`}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
