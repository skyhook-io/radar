import { Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, KeyValueBadgeList } from '../../ui/drawer-components'
import {
  getPeerAuthenticationMode,
  getPeerAuthenticationSelector,
  getPeerAuthenticationPortLevelMtls,
} from '../resource-utils-istio'

const modeColors: Record<string, string> = {
  STRICT: 'bg-green-500/20 text-green-400',
  PERMISSIVE: 'bg-yellow-500/20 text-yellow-400',
  DISABLE: 'bg-red-500/20 text-red-400',
  UNSET: 'bg-gray-500/20 text-gray-400',
}

interface IstioPeerAuthenticationRendererProps {
  data: any
}

export function IstioPeerAuthenticationRenderer({ data }: IstioPeerAuthenticationRendererProps) {
  const mode = getPeerAuthenticationMode(data)
  const selector = getPeerAuthenticationSelector(data)
  const portLevelMtls = getPeerAuthenticationPortLevelMtls(data)
  const hasSelector = Object.keys(selector).length > 0
  const hasPortOverrides = Object.keys(portLevelMtls).length > 0

  return (
    <>
      {/* mTLS Configuration */}
      <Section title="Peer Authentication" icon={Shield} defaultExpanded>
        <PropertyList>
          <Property label="mTLS Mode" value={
            <span className={clsx(
              'badge',
              modeColors[mode] || modeColors.UNSET
            )}>
              {mode}
            </span>
          } />
          <Property label="Scope" value={
            hasSelector ? 'Workload-scoped' : 'Namespace-wide'
          } />
        </PropertyList>

        {/* Mode description */}
        <div className="mt-2 text-xs text-theme-text-tertiary">
          {mode === 'STRICT' && 'All connections must use mTLS. Plaintext connections will be rejected.'}
          {mode === 'PERMISSIVE' && 'Accepts both plaintext and mTLS connections. Useful during migration.'}
          {mode === 'DISABLE' && 'mTLS is disabled. All connections will be plaintext.'}
          {mode === 'UNSET' && 'Inherits mTLS mode from parent (mesh or namespace).'}
        </div>
      </Section>

      {/* Selector */}
      {hasSelector && (
        <Section title="Workload Selector" defaultExpanded>
          <KeyValueBadgeList items={selector} />
        </Section>
      )}

      {/* Port-level mTLS overrides */}
      {hasPortOverrides && (
        <Section title={`Port-Level mTLS (${Object.keys(portLevelMtls).length})`} defaultExpanded>
          <div className="space-y-1">
            {Object.entries(portLevelMtls).map(([port, config]) => (
              <div key={port} className="flex items-center gap-2 text-sm">
                <span className="text-theme-text-secondary font-mono">Port {port}</span>
                <span className={clsx(
                  'badge-sm',
                  modeColors[config.mode] || modeColors.UNSET
                )}>
                  {config.mode}
                </span>
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
