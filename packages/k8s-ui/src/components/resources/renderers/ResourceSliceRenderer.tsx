import { HardDrive, Cpu } from 'lucide-react'
import { Section, PropertyList, Property, ResourceLink } from '../../ui/drawer-components'

interface ResourceSliceRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

const DEVICE_DISPLAY_LIMIT = 20

export function ResourceSliceRenderer({ data, onNavigate }: ResourceSliceRendererProps) {
  const spec = data.spec || {}
  const devices = spec.devices || []
  const visible = devices.slice(0, DEVICE_DISPLAY_LIMIT)

  return (
    <>
      <Section title="Slice Info" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Driver" value={spec.driver} />
          <Property label="Pool" value={spec.pool?.name} />
          {spec.nodeName && (
            <Property
              label="Node"
              value={<ResourceLink name={spec.nodeName} kind="nodes" namespace="" onNavigate={onNavigate} />}
            />
          )}
          {spec.allNodes && <Property label="Scope" value="All nodes" />}
        </PropertyList>
      </Section>

      <Section title={`Devices (${devices.length})`} icon={Cpu} defaultExpanded>
        {devices.length > 0 ? (
          <div className="space-y-2">
            {visible.map((d: any, i: number) => {
              // v1 puts attributes/capacity at the device level; v1beta1 nested them under "basic"
              const attrs = d?.attributes || d?.basic?.attributes || {}
              const capacity = d?.capacity || d?.basic?.capacity || {}
              const attrEntries = Object.entries(attrs).slice(0, 6)
              return (
                <div key={d?.name || i} className="card-inner">
                  <div className="text-sm text-theme-text-primary font-medium">{d?.name || `device-${i}`}</div>
                  {attrEntries.length > 0 && (
                    <div className="mt-1 space-y-0.5">
                      {attrEntries.map(([k, v]: [string, any]) => (
                        <div key={k} className="text-xs text-theme-text-secondary">
                          {k}: {String(v?.string ?? v?.int ?? v?.bool ?? v?.version ?? '-')}
                        </div>
                      ))}
                    </div>
                  )}
                  {Object.keys(capacity).length > 0 && (
                    <div className="mt-1 text-xs text-theme-text-tertiary">
                      capacity: {Object.entries(capacity).map(([k, v]: [string, any]) => `${k}=${v?.value ?? v}`).join(', ')}
                    </div>
                  )}
                </div>
              )
            })}
            {devices.length > DEVICE_DISPLAY_LIMIT && (
              <div className="text-xs text-theme-text-tertiary">
                +{devices.length - DEVICE_DISPLAY_LIMIT} more devices — see YAML for the full list
              </div>
            )}
          </div>
        ) : (
          <div className="text-sm text-theme-text-tertiary">No devices published in this slice</div>
        )}
      </Section>
    </>
  )
}
