import { HardDrive } from 'lucide-react'
import { Section, PropertyList, Property } from '../drawer-components'
import {
  getVSLProvider,
  getVSLConfig,
} from '../resource-utils-velero'

interface VeleroVSLRendererProps {
  data: any
}

export function VeleroVSLRenderer({ data }: VeleroVSLRendererProps) {
  const config = getVSLConfig(data)

  return (
    <>
      <Section title="Provider" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Provider" value={getVSLProvider(data)} />
        </PropertyList>
        {Object.keys(config).length > 0 && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Config</div>
            <div className="flex flex-wrap gap-1">
              {Object.entries(config).map(([k, v]) => (
                <span key={k} className="px-1.5 py-0.5 bg-theme-hover rounded text-xs text-theme-text-secondary">
                  {k}: {String(v)}
                </span>
              ))}
            </div>
          </div>
        )}
      </Section>
    </>
  )
}
