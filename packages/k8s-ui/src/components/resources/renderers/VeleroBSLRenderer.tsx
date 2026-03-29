import { HardDrive, Clock } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../../ui/drawer-components'
import {
  getBSLStatus,
  getBSLProvider,
  getBSLBucket,
  getBSLPrefix,
  getBSLRegion,
  getBSLDefault,
  getBSLAccessMode,
  getBSLLastValidation,
  getBSLLastSynced,
} from '../resource-utils-velero'

interface VeleroBSLRendererProps {
  data: any
}

export function VeleroBSLRenderer({ data }: VeleroBSLRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []
  const bslStatus = getBSLStatus(data)
  const config = data.spec?.config || {}

  return (
    <>
      {/* Status section */}
      <Section title="Status" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={
            <span className={clsx('badge', bslStatus.color)}>
              {bslStatus.text}
            </span>
          } />
          <Property label="Last Validation" value={getBSLLastValidation(data)} />
          <Property label="Last Synced" value={getBSLLastSynced(data)} />
          {status.lastSyncedRevision && (
            <Property label="Last Synced Revision" value={
              <span className="text-sm font-mono text-theme-text-secondary break-all">{status.lastSyncedRevision}</span>
            } />
          )}
        </PropertyList>
      </Section>

      {/* Provider section */}
      <Section title="Provider" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Provider" value={getBSLProvider(data)} />
          <Property label="Bucket" value={getBSLBucket(data)} />
          <Property label="Prefix" value={getBSLPrefix(data)} />
          <Property label="Region" value={getBSLRegion(data)} />
          <Property label="Access Mode" value={getBSLAccessMode(data)} />
          <Property label="Default" value={getBSLDefault(data) ? 'Yes' : 'No'} />
        </PropertyList>
        {Object.keys(config).length > 0 && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Config</div>
            <div className="flex flex-wrap gap-1">
              {Object.entries(config).map(([k, v]) => (
                <span key={k} className="badge-sm bg-theme-hover text-theme-text-secondary">
                  {k}: {String(v)}
                </span>
              ))}
            </div>
          </div>
        )}
      </Section>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
