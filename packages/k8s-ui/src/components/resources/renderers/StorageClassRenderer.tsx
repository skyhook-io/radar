import { Database, Settings } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'

interface StorageClassRendererProps {
  data: any
}

export function StorageClassRenderer({ data }: StorageClassRendererProps) {
  const parameters = data.parameters || {}
  const mountOptions = data.mountOptions || []
  const hasParameters = Object.keys(parameters).length > 0
  const hasMountOptions = mountOptions.length > 0

  return (
    <>
      <Section title="Configuration" icon={Database}>
        <PropertyList>
          <Property label="Provisioner" value={data.provisioner} />
          <Property
            label="Reclaim Policy"
            value={
              data.reclaimPolicy ? (
                <span className={data.reclaimPolicy === 'Delete' ? 'text-red-400' : 'text-green-400'}>
                  {data.reclaimPolicy}
                </span>
              ) : undefined
            }
          />
          <Property
            label="Volume Binding Mode"
            value={
              data.volumeBindingMode ? (
                <span title={
                  data.volumeBindingMode === 'WaitForFirstConsumer'
                    ? 'Volume binding is delayed until a Pod using this PVC is created'
                    : undefined
                }>
                  {data.volumeBindingMode}
                </span>
              ) : undefined
            }
          />
          <Property
            label="Allow Volume Expansion"
            value={
              data.allowVolumeExpansion != null ? (
                <span className={data.allowVolumeExpansion ? 'text-green-400' : undefined}>
                  {data.allowVolumeExpansion ? 'Yes' : 'No'}
                </span>
              ) : undefined
            }
          />
        </PropertyList>
      </Section>

      {hasParameters && (
        <Section title="Parameters" icon={Settings}>
          <PropertyList>
            {Object.entries(parameters).map(([key, value]) => (
              <Property key={key} label={key} value={value as string} />
            ))}
          </PropertyList>
        </Section>
      )}

      {hasMountOptions && (
        <Section title="Mount Options">
          <div className="flex flex-wrap gap-1.5">
            {mountOptions.map((option: string) => (
              <span
                key={option}
                className="badge status-neutral"
              >
                {option}
              </span>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
