import { Shield, AlertTriangle, Globe, Key } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../../ui/drawer-components'
import { getSolverType } from './ClusterIssuerRenderer'

function getChallengeStateBadge(state: string): { color: string; text: string } {
  switch (state?.toLowerCase()) {
    case 'valid':
      return { text: 'Valid', color: 'bg-green-500/20 text-green-400' }
    case 'ready':
      return { text: 'Ready', color: 'bg-blue-500/20 text-blue-400' }
    case 'pending':
      return { text: 'Pending', color: 'bg-yellow-500/20 text-yellow-400' }
    case 'processing':
      return { text: 'Processing', color: 'bg-yellow-500/20 text-yellow-400' }
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

export function ChallengeRenderer({ data }: { data: any }) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const state = status.state || ''
  const presented = status.presented

  const isError = state === 'invalid' || state === 'expired' || state === 'errored'

  // Determine challenge type from spec
  const challengeType = spec.type || (spec.solver?.http01 ? 'HTTP-01' : spec.solver?.dns01 ? 'DNS-01' : 'Unknown')

  // Extract solver info
  const solver = spec.solver || {}
  const solverInfo = getSolverType(solver)

  return (
    <>
      {/* Problem detection alert */}
      {isError && (
        <div className="mb-4 p-3 bg-red-500/10 border border-red-500/30 rounded-lg">
          <div className="flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 text-red-400 mt-0.5 shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-medium text-red-400">Challenge {state.charAt(0).toUpperCase() + state.slice(1)}</div>
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
              <span className={clsx('badge', getChallengeStateBadge(state).color)}>
                {getChallengeStateBadge(state).text}
              </span>
            }
          />
          <Property
            label="Type"
            value={
              <span className="badge bg-theme-elevated text-theme-text-secondary">
                {challengeType}
              </span>
            }
          />
          <Property
            label="Presented"
            value={presented === true ? 'Yes' : presented === false ? 'No' : '-'}
          />
          {spec.dnsName && <Property label="Domain" value={spec.dnsName} />}
        </PropertyList>
      </Section>

      {/* ACME */}
      {(spec.url || spec.token) && (
        <Section title="ACME" icon={Globe}>
          <PropertyList>
            {spec.url && <Property label="Challenge URL" value={spec.url} />}
            {spec.token && <Property label="Token" value={spec.token.length > 32 ? spec.token.slice(0, 32) + '...' : spec.token} />}
          </PropertyList>
        </Section>
      )}

      {/* Solver */}
      {solverInfo.type !== 'Unknown' && (
        <Section title="Solver" icon={Key}>
          <PropertyList>
            <Property label="Type" value={solverInfo.type} />
            {solverInfo.detail && <Property label="Detail" value={solverInfo.detail} />}
          </PropertyList>
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
